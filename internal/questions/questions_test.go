package questions

import (
	"context"
	"math"
	"testing"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/classifier"
	"github.com/jaimesoad/mail-categorizer/internal/features"
)

func tree(t *testing.T) *categories.Tree {
	t.Helper()
	tr, err := categories.NewTree([]categories.Category{
		{ID: "finance", Name: "Finance", Enabled: true},
		{ID: "finance.bank", ParentID: "finance", Name: "Bank", Enabled: true},
		{ID: "finance.purchase", ParentID: "finance", Name: "Purchase", Enabled: true},
		{ID: "work", Name: "Work", Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func boolQ(id, feature string, aff map[string]float64) Question {
	q := Question{ID: id, Text: "¿" + id + "?", Feature: feature, Type: Boolean, Validated: true, Affinity: map[string]map[string]float64{}}
	for c, p := range aff {
		q.Affinity[c] = map[string]float64{Yes: p}
	}
	q.Normalize()
	return q
}

func near(a, b, eps float64) bool { return math.Abs(a-b) < eps }

func TestInformationGainPerfectSplitIsOneBit(t *testing.T) {
	q := boolQ("is_purchase", "context.is_purchase", map[string]float64{"finance.purchase": 1, "finance.bank": 0})
	m := Model{Q: q, Tree: tree(t)}
	cands := []classifier.Candidate{{Category: "finance.bank", Score: 0.5}, {Category: "finance.purchase", Score: 0.5}}
	// priorStrength smoothing keeps it slightly below 1
	if g := InformationGain(cands, m); g < 0.5 || g > 1.0001 {
		t.Fatalf("gain = %v", g)
	}
}

func TestUninformativeQuestionHasZeroGain(t *testing.T) {
	q := boolQ("meh", "context.meh", nil)
	cands := []classifier.Candidate{{Category: "finance.bank", Score: 0.5}, {Category: "finance.purchase", Score: 0.5}}
	if g := InformationGain(cands, Model{Q: q, Tree: tree(t)}); !near(g, 0, 1e-9) {
		t.Fatalf("gain = %v", g)
	}
}

func TestSelectorPrefersMostInformative(t *testing.T) {
	good := boolQ("is_purchase", "context.is_purchase", map[string]float64{"finance.purchase": 0.95, "finance.bank": 0.05})
	weak := boolQ("is_important", "context.is_important", map[string]float64{"finance.purchase": 0.6, "finance.bank": 0.5})
	sel := &Selector{Tree: tree(t), Questions: []Question{weak, good}, Params: DefaultParams()}
	cands := []classifier.Candidate{{Category: "finance.bank", Score: 0.45}, {Category: "finance.purchase", Score: 0.45}, {Category: "unknown", Score: 0.1}}
	got, err := sel.Select(context.Background(), Session{Candidates: cands, Features: features.NewSet()})
	if err != nil || got == nil || got.Question.ID != "is_purchase" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestSelectorRespectsExclusions(t *testing.T) {
	q := boolQ("is_purchase", "content.contains_receipt", map[string]float64{"finance.purchase": 0.95, "finance.bank": 0.05})
	cands := []classifier.Candidate{{Category: "finance.bank", Score: 0.5}, {Category: "finance.purchase", Score: 0.5}}
	ctx := context.Background()

	sel := &Selector{Tree: tree(t), Questions: []Question{q}, Params: DefaultParams()}
	if g, _ := sel.Select(ctx, Session{Candidates: cands, Features: features.NewSet()}); g == nil {
		t.Fatal("baseline should select")
	}
	// already asked in this session
	if g, _ := sel.Select(ctx, Session{Candidates: cands, Features: features.NewSet(), Asked: []string{"is_purchase"}}); g != nil {
		t.Error("repeated question")
	}
	// the system already knows the feature
	known := features.NewSet(features.Feature{Name: "content.contains_receipt", Value: "true", Confidence: 0.97})
	if g, _ := sel.Select(ctx, Session{Candidates: cands, Features: known}); g != nil {
		t.Error("asked something already known")
	}
	// disabled / unvalidated
	d, u := q, q
	d.Disabled = true
	u.Validated = false
	sel.Questions = []Question{d, u}
	if g, _ := sel.Select(ctx, Session{Candidates: cands, Features: features.NewSet()}); g != nil {
		t.Error("disabled/unvalidated question selected")
	}
	// a single confident candidate: nothing to separate
	sel.Questions = []Question{q}
	if g, _ := sel.Select(ctx, Session{Candidates: []classifier.Candidate{{Category: "work", Score: 1}}, Features: features.NewSet()}); g != nil {
		t.Error("asked with one candidate")
	}
}

func TestComplexityPenaltyDemotesLongQuestions(t *testing.T) {
	aff := map[string]float64{"finance.purchase": 0.95, "finance.bank": 0.05}
	short := boolQ("short", "context.a", aff)
	long := boolQ("long", "context.b", aff)
	long.Text = "¿Este correo electrónico que acabas de recibir en tu bandeja de entrada principal se relaciona con alguna compra que hiciste recientemente en línea o en una tienda física cercana?"
	sel := &Selector{Tree: tree(t), Questions: []Question{long, short}, Params: DefaultParams()}
	r, _ := sel.Rank(context.Background(), Session{Candidates: []classifier.Candidate{{Category: "finance.bank", Score: 0.5}, {Category: "finance.purchase", Score: 0.5}}, Features: features.NewSet()})
	if len(r) != 2 || r[0].Question.ID != "short" || r[0].Utility <= r[1].Utility {
		t.Fatalf("got %+v", r)
	}
}

type fakeStats map[string]Stats

func (f fakeStats) QuestionStats(context.Context) (map[string]Stats, error) { return f, nil }

func TestLowResponseRateDemotesQuestion(t *testing.T) {
	aff := map[string]float64{"finance.purchase": 0.95, "finance.bank": 0.05}
	a, b := boolQ("a", "context.a", aff), boolQ("b", "context.b", aff)
	sel := &Selector{Tree: tree(t), Questions: []Question{a, b}, Params: DefaultParams(),
		Stats: fakeStats{"a": {Shown: 50, Answered: 2}, "b": {Shown: 50, Answered: 48}}}
	r, _ := sel.Rank(context.Background(), Session{Candidates: []classifier.Candidate{{Category: "finance.bank", Score: 0.5}, {Category: "finance.purchase", Score: 0.5}}, Features: features.NewSet()})
	if r[0].Question.ID != "b" {
		t.Fatalf("got %+v", r)
	}
}

func TestLearnedCountsOverridePrior(t *testing.T) {
	q := boolQ("q", "context.q", map[string]float64{"work": 0.1})
	m := Model{Q: q, Tree: tree(t)}
	before := m.Dist("work")[Yes]
	m.Stats = Stats{Counts: map[string]map[string]float64{"work": {Yes: 40}}}
	after := m.Dist("work")[Yes]
	if !(after > 0.8 && before < 0.2) {
		t.Fatalf("before=%v after=%v", before, after)
	}
}

func TestAffinityInheritedFromAncestor(t *testing.T) {
	q := boolQ("q", "context.q", map[string]float64{"finance": 0.9})
	m := Model{Q: q, Tree: tree(t)}
	if got := m.Dist("finance.bank")[Yes]; !near(got, 0.9, 1e-9) {
		t.Fatalf("got %v", got)
	}
	if got := m.Dist("work")[Yes]; !near(got, 0.5, 1e-9) {
		t.Fatalf("uninformative category should be 0.5, got %v", got)
	}
}

func TestPosteriorAfterAnswer(t *testing.T) {
	q := boolQ("is_purchase", "context.is_purchase", map[string]float64{"finance.purchase": 0.95, "finance.bank": 0.05})
	m := Model{Q: q, Tree: tree(t)}
	cands := []classifier.Candidate{{Category: "finance.bank", Score: 0.43}, {Category: "finance.purchase", Score: 0.41}, {Category: "work", Score: 0.08}, {Category: "unknown", Score: 0.08}}
	post := Posterior(cands, m, Yes)
	if post[0].Category != "finance.purchase" || post[0].Score < 0.75 {
		t.Fatalf("got %+v", post)
	}
}

func TestResolveAnswer(t *testing.T) {
	b := boolQ("b", "context.b", nil)
	for in, want := range map[string]string{"yes": Yes, "Sí": Yes, "NO": No, "true": Yes, "0": No} {
		if got, err := b.ResolveAnswer(in); err != nil || got[0] != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := b.ResolveAnswer("maybe"); err == nil {
		t.Error("maybe should fail")
	}
	mc := Question{ID: "m", Text: "?", Feature: "x.y", Type: MultiChoice, Answers: []Answer{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
	mc.Normalize()
	if got, err := mc.ResolveAnswer("a, C"); err != nil || len(got) != 2 || got[1] != "c" {
		t.Errorf("multi: %v %v", got, err)
	}
}

func TestValidate(t *testing.T) {
	ok := boolQ("ok", "context.ok", nil)
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*Question){
		"no id":       func(q *Question) { q.ID = "" },
		"no text":     func(q *Question) { q.Text = " " },
		"bad feature": func(q *Question) { q.Feature = "Bad Feature" },
		"bad type":    func(q *Question) { q.Type = "weird" },
		"bad affinity": func(q *Question) {
			q.Affinity = map[string]map[string]float64{"x": {Yes: 1.5}}
		},
		"affinity unknown answer": func(q *Question) {
			q.Affinity = map[string]map[string]float64{"x": {"maybe": 0.5}}
		},
		"long text": func(q *Question) {
			s := ""
			for i := 0; i < 250; i++ {
				s += "a"
			}
			q.Text = s
		},
	}
	for name, mut := range bad {
		q := boolQ("ok", "context.ok", nil)
		mut(&q)
		if err := q.Validate(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
