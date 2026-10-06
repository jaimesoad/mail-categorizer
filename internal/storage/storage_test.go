package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/features"
	"github.com/jaimesoad/mail-categorizer/internal/learning"
	"github.com/jaimesoad/mail-categorizer/internal/questions"
)

var ctx = context.Background()
var now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func open(t *testing.T) *DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.SyncCategories(ctx, []categories.Category{
		{ID: "finance", Name: "Finance", Enabled: true},
		{ID: "finance.bank", ParentID: "finance", Name: "Bank", Enabled: true},
		{ID: "unknown", Name: "Unknown", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	return db
}

func mail(id, hash string) email.Email {
	return email.Email{ID: id, MessageID: id + "@x", RawHash: hash, From: email.Address{Email: "a@bank.example", Name: "A"}, Subject: "s " + id, BodyText: "body " + id}
}

func TestMigrationsAreIdempotentAndFileBacked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.SyncCategories(ctx, []categories.Category{{ID: "a", Name: "A", Enabled: true}})
	db.Close()
	db2, err := Open(path) // reopen: migrations must not re-run destructively
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	cats, _ := db2.StoredCategories(ctx)
	if len(cats) != 1 || cats[0].ID != "a" {
		t.Fatalf("data lost across reopen: %+v", cats)
	}
	var v string
	db2.QueryRowContext(ctx, `SELECT value FROM schema_meta WHERE key='version'`).Scan(&v)
	if v != "1" {
		t.Errorf("version = %q", v)
	}
}

func TestInsertEmailDedupeConflictAndFeatures(t *testing.T) {
	db := open(t)
	fs := []features.Feature{{Name: "sender.domain", Value: "bank.example", Confidence: 1, Source: features.SourceHeader}}
	id, existed, err := db.InsertEmail(ctx, mail("e1", "h1"), fs, now)
	if err != nil || existed || id != "e1" {
		t.Fatalf("%v %v %v", id, existed, err)
	}
	id, existed, err = db.InsertEmail(ctx, mail("other-id", "h1"), nil, now) // same hash
	if err != nil || !existed || id != "e1" {
		t.Fatalf("dedupe by hash: %v %v %v", id, existed, err)
	}
	if _, _, err = db.InsertEmail(ctx, mail("e1", "h2"), nil, now); !errors.Is(err, ErrIDConflict) {
		t.Fatalf("id conflict: %v", err)
	}
	set, _ := db.Features(ctx, "e1")
	if !set.Has("sender.domain", "bank.example") {
		t.Fatal("features not stored")
	}
	got, err := db.GetEmail(ctx, "e1")
	if err != nil || got.Subject != "s e1" || got.From.Name != "A" {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err := db.GetEmail(ctx, "zzz"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing email must be ErrNotFound")
	}
}

func TestClassificationFinalAndLabels(t *testing.T) {
	db := open(t)
	db.InsertEmail(ctx, mail("e1", "h1"), nil, now)
	err := db.SaveClassification(ctx, "e1", []Classification{
		{CategoryID: "finance.bank", Score: 0.9, Method: "rule", Final: true, Explanation: "{}"},
		{CategoryID: "unknown", Score: 0.1, Method: "rule"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	f, err := db.FinalClassification(ctx, "e1")
	if err != nil || f.CategoryID != "finance.bank" || !f.Final {
		t.Fatalf("%+v %v", f, err)
	}
	if labels, _ := db.Labels(ctx); len(labels) != 0 {
		t.Fatal("an automatic classification is not a trusted label")
	}
	db.AddInteraction(ctx, Interaction{EmailID: "e1", Kind: KindConfirm, ResultingCategory: "finance.bank"}, now)
	if labels, _ := db.Labels(ctx); labels["e1"].Category != "finance.bank" || labels["e1"].Source != "user" {
		t.Fatalf("labels: %+v", labels)
	}
	db.AddInteraction(ctx, Interaction{EmailID: "e1", Kind: KindCorrect, ResultingCategory: "finance"}, now.Add(time.Second))
	if labels, _ := db.Labels(ctx); labels["e1"].Category != "finance" {
		t.Fatal("the latest user verdict must win")
	}
	// saving again replaces candidates
	db.SaveClassification(ctx, "e1", []Classification{{CategoryID: "finance", Score: 1, Method: "user", Final: true}}, now)
	cands, _ := db.Candidates(ctx, "e1")
	if len(cands) != 1 {
		t.Fatalf("candidates not replaced: %+v", cands)
	}
}

func TestSessionsUpsert(t *testing.T) {
	db := open(t)
	db.InsertEmail(ctx, mail("e1", "h1"), nil, now)
	if _, err := db.GetSession(ctx, "e1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("no session yet")
	}
	db.SaveSession(ctx, SessionRow{EmailID: "e1", Status: "open", InitialDecision: "ask", State: []byte(`{"a":1}`)}, now)
	s1, _ := db.GetSession(ctx, "e1")
	db.SaveSession(ctx, SessionRow{ID: s1.ID, EmailID: "e1", Status: "resolved", InitialDecision: "ask", FinalCategory: "finance", ResolvedBy: "answers", QuestionsAsked: 2, State: []byte(`{"a":2}`)}, now)
	s2, _ := db.GetSession(ctx, "e1")
	if s2.ID != s1.ID || s2.Status != "resolved" || s2.QuestionsAsked != 2 || string(s2.State) != `{"a":2}` {
		t.Fatalf("upsert: %+v", s2)
	}
}

func TestLearnedRulesRepo(t *testing.T) {
	db := open(t)
	r := learning.Rule{CategoryID: "finance.bank", Key: "sender.domain=bank.example", Kind: learning.KindSenderDomain,
		Conditions: []learning.Cond{{Feature: "sender.domain", Value: "bank.example"}}, Weight: 0.2, Observations: 1, Confirmations: 1, CreatedAt: now, UpdatedAt: now}
	if err := db.SaveRule(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.Confirmations, r.Weight = 2, 0.35
	if err := db.SaveRule(ctx, r); err != nil { // upsert by (category,key,negative)
		t.Fatal(err)
	}
	neg := r
	neg.Negative = true
	if err := db.SaveRule(ctx, neg); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetRule(ctx, "finance.bank", r.Key, false)
	if got == nil || got.Confirmations != 2 || got.Weight != 0.35 || len(got.Conditions) != 1 {
		t.Fatalf("%+v", got)
	}
	byKey, _ := db.RulesByKeys(ctx, []string{r.Key, "nope"})
	if len(byKey) != 2 { // positive + negative
		t.Fatalf("by keys: %+v", byKey)
	}
	if missing, _ := db.GetRule(ctx, "finance", "x", false); missing != nil {
		t.Fatal("missing rule must be nil")
	}
	all, _ := db.ListRules(ctx, "", 10)
	if len(all) != 2 {
		t.Fatalf("list: %d", len(all))
	}
}

func TestQuestionsRoundTripAndState(t *testing.T) {
	db := open(t)
	q := questions.Question{ID: "q1", Text: "¿Es?", Feature: "ctx.q", Type: questions.Boolean, Validated: true,
		Affinity: map[string]map[string]float64{"finance.bank": {"yes": 0.9}}}
	q.Normalize()
	if err := db.SaveQuestion(ctx, q, false); err != nil {
		t.Fatal(err)
	}
	gen := q
	gen.ID, gen.Source, gen.Validated, gen.Disabled = "gen1", questions.SourceLLM, false, true
	db.SaveQuestion(ctx, gen, true)
	byID := func() map[string]questions.Question {
		qs, _ := db.Questions(ctx)
		m := map[string]questions.Question{}
		for _, q := range qs {
			m[q.ID] = q
		}
		return m
	}
	m := byID()
	if len(m) != 2 || m["q1"].Affinity["finance.bank"]["yes"] != 0.9 || len(m["q1"].Answers) != 2 {
		t.Fatalf("%+v", m)
	}
	if m["gen1"].Validated || !m["gen1"].Disabled {
		t.Fatalf("generated question must stay unvalidated: %+v", m["gen1"])
	}
	if err := db.SetQuestionState(ctx, "gen1", true, true); err != nil {
		t.Fatal(err)
	}
	if m = byID(); !m["gen1"].Validated || m["gen1"].Disabled {
		t.Fatal("approval not persisted")
	}
	if err := db.SetQuestionState(ctx, "nope", true, true); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown question")
	}
}

func TestQuestionStatsUseOnlyExplicitLabels(t *testing.T) {
	db := open(t)
	q := questions.Question{ID: "q1", Text: "¿Es?", Feature: "ctx.q", Type: questions.Boolean, Validated: true}
	q.Normalize()
	db.SaveQuestion(ctx, q, false)
	for _, id := range []string{"e1", "e2", "e3"} {
		db.InsertEmail(ctx, mail(id, "h"+id), nil, now)
	}
	db.AddInteraction(ctx, Interaction{EmailID: "e1", Kind: KindAnswer, QuestionID: "q1", Answer: "yes"}, now)
	db.AddInteraction(ctx, Interaction{EmailID: "e2", Kind: KindAnswer, QuestionID: "q1", Answer: "no"}, now)
	db.AddInteraction(ctx, Interaction{EmailID: "e3", Kind: KindSkip, QuestionID: "q1"}, now)
	db.AddInteraction(ctx, Interaction{EmailID: "e1", Kind: KindConfirm, ResultingCategory: "finance.bank"}, now)
	st, err := db.QuestionStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s := st["q1"]
	if s.Shown != 3 || s.Answered != 2 {
		t.Fatalf("shown/answered: %+v", s)
	}
	if s.Counts["finance.bank"]["yes"] != 1 || len(s.Counts) != 1 {
		t.Fatalf("only e1 has an explicit label: %+v", s.Counts)
	}
}

func TestHistoryAndThread(t *testing.T) {
	db := open(t)
	db.InsertEmail(ctx, mail("e1", "h1"), nil, now)
	db.SaveClassification(ctx, "e1", []Classification{{CategoryID: "finance.bank", Score: 1, Method: "user", Final: true}}, now)
	h, err := db.SenderHistory(ctx, "a@bank.example", "bank.example")
	if err != nil || h.EmailCount != 1 || h.DomainCount != 1 || len(h.EmailCategories) != 1 || h.EmailCategories[0] != "finance.bank" {
		t.Fatalf("%+v %v", h, err)
	}
	if h2, _ := db.SenderHistory(ctx, "new@x.example", "x.example"); h2.EmailCount != 0 {
		t.Fatal("unknown sender")
	}
	ti, _ := db.ThreadInfo(ctx, []string{"missing@x", "<e1@x>"})
	if !ti.Found || ti.Category != "finance.bank" || ti.Sender != "a@bank.example" {
		t.Fatalf("%+v", ti)
	}
	if ti2, _ := db.ThreadInfo(ctx, []string{"zzz"}); ti2.Found {
		t.Fatal("not found")
	}
}

func TestPurgeDeleteAndBackup(t *testing.T) {
	db := open(t)
	db.InsertEmail(ctx, mail("e1", "h1"), []features.Feature{
		{Name: "sender.domain", Value: "bank.example", Confidence: 1}, {Name: "content.keyword", Value: "secret", Confidence: 0.5}, {Name: "subject.tokens", Value: "hello", Confidence: 0.8},
	}, now)
	db.SaveClassification(ctx, "e1", []Classification{{CategoryID: "finance.bank", Score: 1, Method: "user", Final: true}}, now)
	if err := db.PurgeContent(ctx, "e1", now); err != nil {
		t.Fatal(err)
	}
	e, _ := db.GetEmail(ctx, "e1")
	set, _ := db.Features(ctx, "e1")
	if e.BodyText != "" || e.Subject != "" || set.Exists("content.keyword") || set.Exists("subject.tokens") || !set.Exists("sender.domain") {
		t.Fatalf("purge: %+v %v", e, set.All())
	}
	if _, err := db.FinalClassification(ctx, "e1"); err != nil {
		t.Fatal("classification must survive a content purge")
	}
	if err := db.PurgeContent(ctx, "nope", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("purge unknown")
	}

	dst := filepath.Join(t.TempDir(), "backup.db")
	if err := db.Backup(ctx, dst); err != nil {
		t.Fatal(err)
	}
	bk, err := Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer bk.Close()
	if _, err := bk.FinalClassification(ctx, "e1"); err != nil {
		t.Fatalf("backup is missing data: %v", err)
	}

	if err := db.DeleteEmail(ctx, "e1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinalClassification(ctx, "e1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("delete must cascade")
	}
	if err := db.DeleteEmail(ctx, "e1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("double delete")
	}
}

func TestSyncCategoriesDisablesRemoved(t *testing.T) {
	db := open(t)
	db.SyncCategories(ctx, []categories.Category{{ID: "finance", Name: "Finance", Enabled: true}})
	cats, _ := db.StoredCategories(ctx)
	enabled := map[string]bool{}
	for _, c := range cats {
		enabled[c.ID] = c.Enabled
	}
	if !enabled["finance"] || enabled["finance.bank"] {
		t.Fatalf("removed categories must be disabled, not deleted: %v", enabled)
	}
}

func TestCacheInvalidation(t *testing.T) {
	db := open(t)
	db.InsertEmail(ctx, mail("e1", "h1"), nil, now)
	v := db.LabelVersion()
	db.AddInteraction(ctx, Interaction{EmailID: "e1", Kind: KindAnswer, QuestionID: ""}, now)
	if db.LabelVersion() != v {
		t.Error("an answer is not a label")
	}
	db.AddInteraction(ctx, Interaction{EmailID: "e1", Kind: KindManual, ResultingCategory: "finance"}, now)
	if db.LabelVersion() == v {
		t.Error("a manual label must invalidate the similarity index")
	}
}
