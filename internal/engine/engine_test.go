package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jaimesoad/mail-categorizer/internal/config"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/rules"
	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

func testEngine(t *testing.T, mutate ...func(*config.Config)) *Engine {
	t.Helper()
	cfg, err := config.LoadOrDefault("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Rules = append(cfg.Rules,
		rules.Rule{ID: "my-bank", Category: "finance.bank", Weight: 0.7, Reason: "El remitente coincide con un dominio bancario conocido.",
			When: rules.Condition{Feature: "sender.domain", Op: "in", Value: []any{"mybank.example"}}},
		rules.Rule{ID: "my-bank-tx", Category: "finance.bank", Weight: 0.6, Reason: "Contiene una transacción de un banco conocido.",
			When: rules.Condition{All: []rules.Condition{
				{Feature: "sender.domain", Op: "in", Value: []any{"mybank.example"}},
				{Feature: "content.contains_transaction", Value: "true"},
			}}},
		// ambiguous "payment received": equally bank-ish and purchase-ish (spec §39)
		rules.Rule{ID: "pay-bank", Category: "finance.bank", Weight: 0.43, When: rules.Condition{Feature: "subject.tokens", Value: "payment"}},
		rules.Rule{ID: "pay-purchase", Category: "finance.purchase", Weight: 0.41, When: rules.Condition{Feature: "subject.tokens", Value: "payment"}},
	)
	for _, m := range mutate {
		m(cfg)
	}
	built, err := cfg.Build()
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	e, err := New(context.Background(), db, built, WithClock(func() time.Time { clock = clock.Add(time.Second); return clock }))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func mail(id, from, subject, body string) email.Email {
	return email.Email{ID: id, From: email.ParseAddress(from), Subject: subject, BodyText: body, ReceivedAt: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
}

var bg = context.Background()

func TestKnownBankSenderIsClassifiedAutomatically(t *testing.T) {
	e := testEngine(t)
	out, err := e.Process(bg, mail("e1", "alerts@mybank.example", "Transaction completed", "A transaction of $85.40 was completed on your account."))
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != DecisionKnow || out.Category != "finance.bank" || out.NeedsQuestion {
		t.Fatalf("got decision=%s cat=%s score=%.2f cands=%+v", out.Decision, out.Category, out.Score, out.Candidates)
	}
	if out.Score < 0.85 {
		t.Errorf("score %.2f below threshold", out.Score)
	}
	if len(out.Explanation.Reasons) == 0 {
		t.Error("an automatic classification must be explainable")
	}
	t.Logf("explanation:\n%s", joinLines(out.Explanation.Lines()))
}

func joinLines(ls []string) string {
	s := ""
	for _, l := range ls {
		s += "  " + l + "\n"
	}
	return s
}

func TestAmbiguousEmailAsksThenLearnsAndStopsAsking(t *testing.T) {
	e := testEngine(t)
	// First email from store.example: genuinely ambiguous.
	out, err := e.Process(bg, mail("s1", "store@store.example", "Payment received", "Payment received for your order #884211. Thank you for your purchase."))
	if err != nil {
		t.Fatal(err)
	}
	if !out.NeedsQuestion || out.Question == nil {
		t.Fatalf("expected a question, got %+v", out)
	}
	t.Logf("first question: %s (gain %.2f)", out.Question.Text, out.Question.InformationGain)

	// Answer like a user who bought something.
	asked := 0
	for out.NeedsQuestion && asked < 5 {
		ans := "no"
		if out.Question.ID == "is_purchase" {
			ans = "yes"
		}
		if out, err = e.Answer(bg, "s1", out.Question.ID, ans); err != nil {
			t.Fatal(err)
		}
		asked++
	}
	if out.Category != "finance.purchase" || out.Decision != DecisionKnow {
		t.Fatalf("after answers: decision=%s cat=%s score=%.2f", out.Decision, out.Category, out.Score)
	}
	if !out.Learned {
		t.Error("a session resolved through answers must be learned")
	}
	t.Logf("resolved after %d question(s): %.2f", asked, out.Score)

	// The user confirms (strongest signal) a few more of the same sender.
	for i := 2; i <= 6; i++ {
		id := fmt.Sprintf("s%d", i)
		o, err := e.Process(bg, mail(id, "store@store.example", "Payment received", fmt.Sprintf("Payment received for your order #%d. Thank you for your purchase.", 884211+i)))
		if err != nil {
			t.Fatal(err)
		}
		if o.NeedsQuestion {
			o, err = e.Answer(bg, id, o.Question.ID, map[bool]string{true: "yes", false: "no"}[o.Question.ID == "is_purchase"])
			if err != nil {
				t.Fatal(err)
			}
		}
		if o.Decision != DecisionUser {
			if _, err := e.Feedback(bg, id, o.Category == "finance.purchase", "finance.purchase"); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Now the system should know this sender without asking.
	o, err := e.Process(bg, mail("s99", "store@store.example", "Payment received", "Payment received for your order #999999. Thank you for your purchase."))
	if err != nil {
		t.Fatal(err)
	}
	if o.NeedsQuestion {
		t.Fatalf("still asking after repeated confirmations: %+v", o.Candidates)
	}
	if o.Category != "finance.purchase" {
		t.Fatalf("got %s (%s %.2f)", o.Category, o.Decision, o.Score)
	}
	t.Logf("later email: decision=%s score=%.2f method=%s\n%s", o.Decision, o.Score, o.Method, joinLines(o.Explanation.Lines()))
}

func TestCorrectionLearnsNegativeKnowledge(t *testing.T) {
	e := testEngine(t)
	// Teach (wrongly) that shop.example is a bank, until the system commits to it.
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("w%d", i)
		if _, err := e.Process(bg, mail(id, "x@shop.example", "Hello "+id, "Something unrelated number "+id)); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Move(bg, id, "finance.bank"); err != nil {
			t.Fatal(err)
		}
	}
	pre, err := e.Process(bg, mail("wx", "x@shop.example", "Payment status", "weekly update 9"))
	if err != nil {
		t.Fatal(err)
	}
	if pre.Category != "finance.bank" || pre.NeedsQuestion {
		t.Fatalf("setup: the system should have committed to bank: %+v", pre)
	}
	// user corrects
	out, err := e.Feedback(bg, "wx", false, "finance.purchase")
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != DecisionUser || out.Category != "finance.purchase" || out.Score != 1 || out.Method != "user" {
		t.Fatalf("user label must be final with weight 1: %+v", out)
	}
	rs, _ := e.DB().ListRules(bg, "finance.bank", 50)
	neg := false
	for _, r := range rs {
		if r.Negative && r.Key == "sender.domain=shop.example" {
			neg = true
		}
	}
	if !neg {
		t.Fatalf("expected a negative rule 'shop.example is not a bank': %+v", rs)
	}
}

func TestNoEvidenceIsUnknownNotAGuess(t *testing.T) {
	e := testEngine(t, func(c *config.Config) { c.Questions = nil })
	out, err := e.Process(bg, mail("u1", "zed@gmail.example", "zzz", "qqq www eee"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != DecisionUnknown || out.Category != "unknown" || !out.NeedsManual || out.NeedsQuestion {
		t.Fatalf("got %+v", out)
	}
}

func TestQuestionLimitFallsBackToUnknownOrAncestor(t *testing.T) {
	e := testEngine(t, func(c *config.Config) { c.MaxQuestions = 1 })
	out, err := e.Process(bg, mail("l1", "store@store.example", "Payment received", "Payment received."))
	if err != nil || !out.NeedsQuestion {
		t.Fatalf("setup: %+v %v", out, err)
	}
	// answer something that does not settle the matter
	out, err = e.Answer(bg, "l1", out.Question.ID, "no")
	if err != nil {
		t.Fatal(err)
	}
	if out.NeedsQuestion {
		t.Fatal("max_questions_per_email exceeded")
	}
	if out.QuestionsAsked != 1 {
		t.Errorf("asked=%d", out.QuestionsAsked)
	}
}

func TestProcessIsIdempotentAndIDConflictDetected(t *testing.T) {
	e := testEngine(t)
	m := mail("i1", "alerts@mybank.example", "Transaction completed", "A transaction of $10 was completed.")
	a, _ := e.Process(bg, m)
	b, err := e.Process(bg, m)
	if err != nil || !b.AlreadyProcessed || b.SessionID != a.SessionID {
		t.Fatalf("not idempotent: %+v %v", b, err)
	}
	m2 := mail("i1", "other@x.example", "Different", "different content")
	if _, err := e.Process(bg, m2); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestSessionSurvivesRestart(t *testing.T) {
	e := testEngine(t)
	out, _ := e.Process(bg, mail("r1", "store@store.example", "Payment received", "Payment received."))
	if !out.NeedsQuestion {
		t.Fatal("setup")
	}
	e2, err := New(bg, e.DB(), e.built, WithClock(time.Now)) // a fresh engine over the same DB
	if err != nil {
		t.Fatal(err)
	}
	again, err := e2.NextQuestion(bg, "r1")
	if err != nil || again.Question == nil || again.Question.ID != out.Question.ID {
		t.Fatalf("pending question lost: %+v %v", again, err)
	}
}

func TestAnswerValidation(t *testing.T) {
	e := testEngine(t)
	out, _ := e.Process(bg, mail("v1", "store@store.example", "Payment received", "Payment received."))
	if _, err := e.Answer(bg, "v1", out.Question.ID, "maybe"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad answer: %v", err)
	}
	if _, err := e.Answer(bg, "v1", "nope", "yes"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad question: %v", err)
	}
	if _, err := e.Answer(bg, "ghost", out.Question.ID, "yes"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing email: %v", err)
	}
	if _, err := e.Feedback(bg, "v1", false, "no-such-category"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad category: %v", err)
	}
	if _, err := e.Feedback(bg, "v1", true, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("confirming nothing: %v", err)
	}
	// double answer
	o2, err := e.Answer(bg, "v1", out.Question.ID, "yes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Answer(bg, "v1", out.Question.ID, "yes"); !errors.Is(err, ErrConflict) {
		t.Errorf("double answer: %v (state %s)", err, o2.Decision)
	}
}

func TestSkipNeverRepeatsQuestion(t *testing.T) {
	e := testEngine(t)
	out, _ := e.Process(bg, mail("k1", "store@store.example", "Payment received", "Payment received."))
	first := out.Question.ID
	out, err := e.Skip(bg, "k1", first)
	if err != nil {
		t.Fatal(err)
	}
	if out.NeedsQuestion && out.Question.ID == first {
		t.Fatal("skipped question was asked again")
	}
}

func TestDeterminism(t *testing.T) {
	run := func() string {
		e := testEngine(t)
		out, err := e.Process(bg, mail("d1", "store@store.example", "Payment received", "Payment received for your order #1234."))
		if err != nil {
			t.Fatal(err)
		}
		out.SessionID = ""
		b, _ := json.Marshal(out)
		return string(b)
	}
	if a, b := run(), run(); a != b {
		t.Fatalf("non deterministic:\n%s\n%s", a, b)
	}
}

func TestPrivacyStoreBodyOff(t *testing.T) {
	e := testEngine(t, func(c *config.Config) { c.Privacy.StoreBody = false })
	if _, err := e.Process(bg, mail("p1", "a@b.example", "secret subject", "very secret body text")); err != nil {
		t.Fatal(err)
	}
	em, _ := e.DB().GetEmail(bg, "p1")
	if em.BodyText != "" {
		t.Fatal("body was persisted although privacy.store_body=false")
	}
}

func TestPurgeKeepsKnowledge(t *testing.T) {
	e := testEngine(t)
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("g%d", i)
		e.Process(bg, mail(id, "news@letters.example", "Weekly digest", "newsletter content number "+id))
		e.Move(bg, id, "newsletters")
	}
	rulesBefore, _ := e.DB().ListRules(bg, "newsletters", 10)
	if err := e.DB().PurgeAllContentForTest(bg); err != nil {
		t.Fatal(err)
	}
	em, _ := e.DB().GetEmail(bg, "g0")
	if em.Subject != "" || em.BodyText != "" {
		t.Fatal("content not purged")
	}
	rulesAfter, _ := e.DB().ListRules(bg, "newsletters", 10)
	if len(rulesAfter) == 0 || len(rulesAfter) != len(rulesBefore) {
		t.Fatal("purging content must keep learned knowledge")
	}
	if _, err := e.Get(bg, "g0"); err != nil {
		t.Fatal("classification must survive purge")
	}
}
