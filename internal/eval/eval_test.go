package eval

import (
	"context"
	"testing"

	"github.com/jaimesoad/mail-categorizer/internal/config"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/samples"
)

func dataset(t *testing.T, per int) []Item {
	t.Helper()
	var items []Item
	for _, s := range samples.Generate(42, per) {
		e, err := email.ParseEML(s.EML)
		if err != nil {
			t.Fatal(err)
		}
		e.ID = ""
		items = append(items, Item{ID: s.Category + "/" + s.ID, Email: e, Expected: s.Category})
	}
	return items
}

func built(t *testing.T) *config.Built {
	t.Helper()
	cfg, err := config.LoadOrDefault("")
	if err != nil {
		t.Fatal(err)
	}
	b, err := cfg.Build()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSplitIsDeterministicAndDisjoint(t *testing.T) {
	items := dataset(t, 20)
	a1, b1, c1 := Split(items, 60, 20, "s")
	a2, b2, c2 := Split(items, 60, 20, "s")
	if len(a1)+len(b1)+len(c1) != len(items) {
		t.Fatal("split lost items")
	}
	for i := range a1 {
		if a1[i].ID != a2[i].ID {
			t.Fatal("train not deterministic")
		}
	}
	if len(b1) != len(b2) || len(c1) != len(c2) {
		t.Fatal("not deterministic")
	}
	seen := map[string]bool{}
	for _, s := range [][]Item{a1, b1, c1} {
		for _, it := range s {
			if seen[it.ID] {
				t.Fatalf("%s in two splits", it.ID)
			}
			seen[it.ID] = true
		}
	}
	if len(a1) < len(items)/2 || len(c1) == 0 {
		t.Errorf("unexpected proportions %d/%d/%d", len(a1), len(b1), len(c1))
	}
}

// The success criterion of §40: ask a lot at first, then less and less, while
// staying accurate and never confidently wrong.
func TestSystemLearnsToAskLess(t *testing.T) {
	res, err := Run(context.Background(), built(t), dataset(t, 40), Options{Window: 40})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("\n%s", res.Text())

	if len(res.Curve) < 3 {
		t.Fatalf("curve too short: %+v", res.Curve)
	}
	first, last := res.Curve[0], res.Curve[len(res.Curve)-1]
	if !(last.QuestionRate < first.QuestionRate) {
		t.Errorf("question rate did not decrease: %.2f -> %.2f", first.QuestionRate, last.QuestionRate)
	}
	if last.QuestionRate > 0.5*first.QuestionRate+0.05 {
		t.Errorf("question rate should at least halve: %.2f -> %.2f", first.QuestionRate, last.QuestionRate)
	}
	if res.Test.Accuracy < 0.85 {
		t.Errorf("test accuracy %.2f < 0.85", res.Test.Accuracy)
	}
	if res.Test.Sure > 0 && res.Test.AccuracyWhenSure < 0.95 {
		t.Errorf("accuracy when the system says it knows must be >= 95%%, got %.2f", res.Test.AccuracyWhenSure)
	}
	if res.Test.AutoRate < 0.4 {
		t.Errorf("auto rate on test too low: %.2f", res.Test.AutoRate)
	}
}
