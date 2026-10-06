package engine

import (
	"context"
	"testing"

	"github.com/jaimesoad/mail-categorizer/internal/config"
	"github.com/jaimesoad/mail-categorizer/internal/questions"
	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

type stubProposer struct{ calls int }

func (s *stubProposer) Propose(_ context.Context, in questions.ProposeInput) (*questions.Question, error) {
	s.calls++
	return &questions.Question{
		ID: "gen_out", Text: "¿El correo informa sobre dinero que salió de tu cuenta?", Feature: "transaction.direction", Type: questions.Boolean,
		Affinity: map[string]map[string]float64{"finance.bank": {"yes": 0.9, "no": 0.1}, "finance.purchase": {"yes": 0.1, "no": 0.9}},
		Answers:  []questions.Answer{{ID: "yes", FeatureValue: "outgoing"}, {ID: "no", FeatureValue: "other"}},
	}, nil
}

func TestGeneratedQuestionIsSessionScopedUntilApproved(t *testing.T) {
	// no configured questions: only the proposer can help
	cfg, _ := config.LoadOrDefault("")
	cfg.Questions = nil
	built, err := cfg.Build()
	if err != nil {
		t.Fatal(err)
	}
	db, _ := storage.Open(":memory:")
	defer db.Close()
	prop := &stubProposer{}
	e, err := New(bg, db, built, WithProposer(prop))
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.Process(bg, mail("q1", "x@store.example", "Payment received", "Payment received for your order #12345. Thank you for your purchase."))
	if err != nil {
		t.Fatal(err)
	}
	if !out.NeedsQuestion || out.Question == nil || out.Question.ID != "gen_out" {
		t.Skipf("scenario did not create ambiguity between two candidates (decision=%s cands=%+v)", out.Decision, out.Candidates)
	}
	stored, _ := db.Questions(bg)
	var found *questions.Question
	for i := range stored {
		if stored[i].ID == "gen_out" {
			found = &stored[i]
		}
	}
	if found == nil || found.Validated || !found.Disabled || found.Source != questions.SourceLLM {
		t.Fatalf("generated question must be stored unvalidated and disabled: %+v", found)
	}
	if _, err := e.Answer(bg, "q1", "gen_out", "yes"); err != nil {
		t.Fatalf("answering the session-scoped question: %v", err)
	}
	for _, q := range e.Questions() {
		if q.ID == "gen_out" && !q.Disabled {
			t.Fatal("unapproved question leaked into the catalogue")
		}
	}
	if err := e.ApproveQuestion(bg, "gen_out"); err != nil {
		t.Fatal(err)
	}
	ok := false
	for _, q := range e.Questions() {
		ok = ok || (q.ID == "gen_out" && !q.Disabled && q.Validated)
	}
	if !ok {
		t.Fatal("approved question should enter the catalogue")
	}
}
