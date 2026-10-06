package classifier

import (
	"math"
	"testing"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
)

func tree(t *testing.T) *categories.Tree {
	t.Helper()
	tr, err := categories.NewTree([]categories.Category{
		{ID: "work", Name: "Work", Enabled: true},
		{ID: "finance", Name: "Finance", Enabled: true},
		{ID: "finance.bank", ParentID: "finance", Name: "Bank", Enabled: true},
		{ID: "finance.purchase", ParentID: "finance", Name: "Purchase", Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func score(c []Candidate, id string) float64 {
	for _, x := range c {
		if x.Category == id {
			return x.Score
		}
	}
	return 0
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestAggregateSumsToOne(t *testing.T) {
	c := Aggregate(tree(t), []Evidence{{Category: "finance.bank", Weight: 0.7}, {Category: "work", Weight: 0.3}})
	sum := 0.0
	for _, x := range c {
		sum += x.Score
	}
	if !near(sum, 1) {
		t.Fatalf("sum = %v", sum)
	}
}

func TestSingleWeakEvidenceLeavesUnknownMass(t *testing.T) {
	c := Aggregate(tree(t), []Evidence{{Category: "work", Weight: 0.3}})
	if !near(score(c, "work"), 0.3) || !near(score(c, categories.UnknownID), 0.7) {
		t.Fatalf("got %+v", c)
	}
	if c[0].Category != categories.UnknownID {
		t.Fatalf("unknown should lead: %+v", c)
	}
}

func TestNoEvidenceIsUnknown(t *testing.T) {
	c := Aggregate(tree(t), nil)
	if len(c) != 1 || c[0].Category != categories.UnknownID || !near(c[0].Score, 1) {
		t.Fatalf("got %+v", c)
	}
}

func TestNoisyORCombination(t *testing.T) {
	c := Aggregate(tree(t), []Evidence{
		{Category: "finance.bank", Weight: 0.7}, {Category: "finance.bank", Weight: 0.2}, {Category: "finance.bank", Weight: 0.1},
	})
	want := 1 - 0.3*0.8*0.9
	if !near(score(c, "finance.bank"), want) {
		t.Fatalf("bank = %v want %v", score(c, "finance.bank"), want)
	}
}

func TestHierarchyLeafEvidenceKeepsItsStrength(t *testing.T) {
	c := Aggregate(tree(t), []Evidence{{Category: "finance.bank", Weight: 0.7}})
	if !near(score(c, "finance.bank"), 0.7) {
		t.Fatalf("a leaf-targeted 0.7 must stay 0.7, got %+v", c)
	}
	if score(c, "finance") > 1e-6 {
		t.Fatalf("no unrefined mass expected: %+v", c)
	}
}

func TestParentOnlyEvidenceStopsAtParent(t *testing.T) {
	c := Aggregate(tree(t), []Evidence{{Category: "finance", Weight: 0.9}})
	if c[0].Category != "finance" || !near(c[0].Score, 0.9) {
		t.Fatalf("got %+v", c)
	}
}

func TestParentPlusChildRefines(t *testing.T) {
	c := Aggregate(tree(t), []Evidence{{Category: "finance", Weight: 0.9}, {Category: "finance.bank", Weight: 0.7}})
	if c[0].Category != "finance.bank" {
		t.Fatalf("got %+v", c)
	}
	if score(c, "finance") <= 0 {
		t.Fatalf("remainder should stay at parent: %+v", c)
	}
}

func TestSiblingsConflictIsAmbiguous(t *testing.T) {
	c := Aggregate(tree(t), []Evidence{{Category: "finance.bank", Weight: 0.7}, {Category: "finance.purchase", Weight: 0.7}})
	if !near(score(c, "finance.bank"), score(c, "finance.purchase")) {
		t.Fatalf("got %+v", c)
	}
	if score(c, "finance.bank") > 0.5 {
		t.Fatalf("ambiguous evidence must not exceed 0.5: %+v", c)
	}
}

func TestNegativeEvidenceSuppresses(t *testing.T) {
	c := Aggregate(tree(t), []Evidence{
		{Category: "finance.bank", Weight: 0.8}, {Category: "finance.bank", Weight: -0.9},
		{Category: "finance.purchase", Weight: 0.6},
	})
	if c[0].Category != "finance.purchase" {
		t.Fatalf("got %+v", c)
	}
}

func TestNegativeOnParentSuppressesSubtreeShare(t *testing.T) {
	c := Aggregate(tree(t), []Evidence{{Category: "finance.bank", Weight: 0.8}, {Category: "finance", Weight: -0.5}})
	if score(c, "finance.bank") >= 0.8 {
		t.Fatalf("parent suppression ignored: %+v", c)
	}
}

func TestDisabledCategoryIgnored(t *testing.T) {
	tr, _ := categories.NewTree([]categories.Category{{ID: "a", Name: "A", Enabled: false}, {ID: "b", Name: "B", Enabled: true}})
	c := Aggregate(tr, []Evidence{{Category: "a", Weight: 0.9}})
	if c[0].Category != categories.UnknownID {
		t.Fatalf("got %+v", c)
	}
}

func TestDeterministicTieBreak(t *testing.T) {
	ev := []Evidence{{Category: "work", Weight: 0.6}, {Category: "finance", Weight: 0.6}}
	a, b := Aggregate(tree(t), ev), Aggregate(tree(t), ev)
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("non deterministic")
		}
	}
	if a[0].Category != "finance" { // id order breaks ties
		t.Fatalf("got %+v", a)
	}
}

func TestUpdateAndEntropy(t *testing.T) {
	c := []Candidate{{"a", 0.5}, {"b", 0.5}}
	if !near(Entropy(c), 1) {
		t.Fatalf("entropy = %v", Entropy(c))
	}
	u := Update(c, func(id string) float64 {
		if id == "a" {
			return 0.9
		}
		return 0.1
	})
	if !near(u[0].Score, 0.9) || u[0].Category != "a" {
		t.Fatalf("got %+v", u)
	}
}

func TestThresholdLevels(t *testing.T) {
	th := DefaultThresholds()
	mk := func(a, b float64) Result {
		return Result{Best: Candidate{"work", a}, Margin: a - b}
	}
	if th.LevelOf(mk(0.9, 0.05)) != HighConfidence {
		t.Error("expected high")
	}
	if th.LevelOf(mk(0.7, 0.2)) != MediumConfidence {
		t.Error("expected medium")
	}
	if th.LevelOf(mk(0.4, 0.3)) != LowConfidence {
		t.Error("expected low")
	}
	if th.LevelOf(Result{Best: Candidate{categories.UnknownID, 0.95}, Margin: 0.9}) != LowConfidence {
		t.Error("unknown is never confident")
	}
	// margin guard: high score but tiny margin must not auto-classify
	if (Thresholds{High: 0.4, Medium: 0.3, MinMargin: 0.2}).LevelOf(mk(0.45, 0.40)) == HighConfidence {
		t.Error("margin guard failed")
	}
}

func TestStrongEvidenceBeatsWeakCompetitor(t *testing.T) {
	// purchase is strongly supported (several signals), bank only by one weak rule
	c := Aggregate(tree(t), []Evidence{
		{Category: "finance.purchase", Weight: 0.9}, {Category: "finance.purchase", Weight: 0.7},
		{Category: "finance.bank", Weight: 0.43},
	})
	if c[0].Category != "finance.purchase" || c[0].Score < 0.9 {
		t.Fatalf("strong evidence must dominate: %+v", c)
	}
}

func TestLoneCategoryKeepsItsEvidenceAsScore(t *testing.T) {
	for _, w := range []float64{0.2, 0.5, 0.7, 0.91} {
		c := Aggregate(tree(t), []Evidence{{Category: "work", Weight: w}})
		if !near(score(c, "work"), w) {
			t.Errorf("w=%v got %+v", w, c)
		}
	}
}
