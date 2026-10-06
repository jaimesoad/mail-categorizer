package engine

import (
	"fmt"
	"testing"

	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

func teach(e *Engine, id, from, subj, body, cat string, t *testing.T) *Outcome {
	t.Helper()
	if _, err := e.Process(bg, mail(id, from, subj, body)); err != nil {
		t.Fatal(err)
	}
	out, err := e.Move(WithActor(bg, ActorLLM), id, cat)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTeacherLabelsAreWeakerThanHumanOnes(t *testing.T) {
	e := testEngine(t)
	out := teach(e, "t1", "x@acme.example", "Hello", "something about acme", "work", t)
	if out.Decision != DecisionTeacher || out.Method != "model" || out.Score >= 1 {
		t.Fatalf("teacher label must be marked as such: %+v", out)
	}
	rs, _ := e.DB().ListRules(bg, "work", 20)
	if len(rs) == 0 {
		t.Fatal("teacher labels should seed rules")
	}
	for _, r := range rs {
		if r.Confirmations != 0 || r.TeacherConfirmations != 1 {
			t.Fatalf("teacher confirmation must be tracked separately: %+v", r)
		}
		if r.Weight >= 0.2 {
			t.Errorf("one teacher confirmation must weigh less than one human one (0.20): %v", r.Weight)
		}
	}
	// ten teacher labels cannot make a rule as strong as ten human ones
	for i := 2; i <= 10; i++ {
		teach(e, fmt.Sprintf("t%d", i), "x@acme.example", "Hello "+fmt.Sprint(i), "something about acme "+fmt.Sprint(i), "work", t)
	}
	teacherOnly, _ := e.DB().GetRule(bg, "work", "sender.domain=acme.example", false)
	if teacherOnly.Weight > 0.70 {
		t.Errorf("rules nobody human confirmed are capped at 0.70: %v", teacherOnly.Weight)
	}
	human := testEngine(t)
	for i := 1; i <= 10; i++ {
		id := fmt.Sprintf("h%d", i)
		human.Process(bg, mail(id, "x@acme.example", "Hello "+id, "something about acme "+id))
		human.Move(bg, id, "work")
	}
	hr, _ := human.DB().GetRule(bg, "work", "sender.domain=acme.example", false)
	if hr.Weight <= teacherOnly.Weight {
		t.Errorf("human-confirmed rule (%v) must outweigh teacher-only (%v)", hr.Weight, teacherOnly.Weight)
	}
}

func TestHumanVerdictOverridesTeacherAndRejectsItsRules(t *testing.T) {
	e := testEngine(t)
	for i := 0; i < 4; i++ {
		teach(e, fmt.Sprintf("t%d", i), "x@shop2.example", "Hi", fmt.Sprintf("body %d", i), "finance.bank", t)
	}
	labels, _ := e.DB().Labels(bg)
	if labels["t0"].Source != "llm" || labels["t0"].Category != "finance.bank" {
		t.Fatalf("labels: %+v", labels["t0"])
	}
	// the person disagrees with the teacher
	out, err := e.Feedback(bg, "t0", false, "finance.purchase")
	if err != nil || out.Decision != DecisionUser || out.Category != "finance.purchase" {
		t.Fatalf("%+v %v", out, err)
	}
	labels, _ = e.DB().Labels(bg)
	if labels["t0"].Source != "user" || labels["t0"].Category != "finance.purchase" {
		t.Fatalf("human verdict must win: %+v", labels["t0"])
	}
	// and the wrong teacher rule was penalised / negative knowledge created
	neg, _ := e.DB().GetRule(bg, "finance.bank", "sender.domain=shop2.example", true)
	pos, _ := e.DB().GetRule(bg, "finance.bank", "sender.domain=shop2.example", false)
	if neg == nil || pos == nil || pos.Rejections != 1 {
		t.Fatalf("correction of the teacher should reject its rule and create a negative one: pos=%+v neg=%+v", pos, neg)
	}
	// a teacher re-labelling never overrides the person
	if _, err := e.Move(WithActor(bg, ActorLLM), "t0", "finance.bank"); err != nil {
		t.Fatal(err)
	}
	labels, _ = e.DB().Labels(bg)
	if labels["t0"].Source != "user" {
		t.Fatalf("teacher overrode a human label: %+v", labels["t0"])
	}
}

func TestTeacherInteractionsDoNotPolluteUserEffortOrQuestionStats(t *testing.T) {
	e := testEngine(t)
	out, _ := e.Process(bg, mail("q1", "store@store.example", "Payment received", "Payment received for your order #1."))
	if !out.NeedsQuestion {
		t.Fatal("setup")
	}
	if _, err := e.Answer(WithActor(bg, ActorLLM), "q1", out.Question.ID, "yes"); err != nil {
		t.Fatal(err)
	}
	st, _ := e.DB().QuestionStats(bg)
	if st[out.Question.ID].Shown != 0 {
		t.Errorf("LLM answers must not count as user response rate: %+v", st[out.Question.ID])
	}
	ints, _ := e.DB().Interactions(bg, "q1")
	if len(ints) != 1 || ints[0].Actor != storage.ActorLLM {
		t.Fatalf("interaction actor: %+v", ints)
	}
}
