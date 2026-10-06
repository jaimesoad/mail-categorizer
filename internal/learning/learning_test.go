package learning

import (
	"context"
	"math"
	"testing"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/features"
)

type memRepo struct{ rules map[string]Rule }

func newRepo() *memRepo { return &memRepo{rules: map[string]Rule{}} }
func rk(c, k string, n bool) string {
	s := c + "#" + k
	if n {
		s += "#neg"
	}
	return s
}
func (m *memRepo) RulesByKeys(_ context.Context, keys []string) ([]Rule, error) {
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	var out []Rule
	for _, r := range m.rules {
		if set[r.Key] {
			out = append(out, r)
		}
	}
	return out, nil
}
func (m *memRepo) GetRule(_ context.Context, c, k string, n bool) (*Rule, error) {
	if r, ok := m.rules[rk(c, k, n)]; ok {
		return &r, nil
	}
	return nil, nil
}
func (m *memRepo) SaveRule(_ context.Context, r Rule) error {
	m.rules[rk(r.CategoryID, r.Key, r.Negative)] = r
	return nil
}

func tree(t *testing.T) *categories.Tree {
	tr, err := categories.NewTree([]categories.Category{
		{ID: "finance", Name: "Finance", Enabled: true},
		{ID: "finance.bank", ParentID: "finance", Name: "Bank", Enabled: true},
		{ID: "finance.purchase", ParentID: "finance", Name: "Purchase", Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func fs(domain string, free bool) *features.Set {
	b := "false"
	if free {
		b = "true"
	}
	return features.NewSet(
		features.Feature{Name: features.SenderEmail, Value: "a@" + domain, Confidence: 1, Source: features.SourceHeader},
		features.Feature{Name: features.SenderDomain, Value: domain, Confidence: 1, Source: features.SourceHeader},
		features.Feature{Name: features.SenderIsFreemail, Value: b, Confidence: 1, Source: features.SourceParser},
		features.Feature{Name: features.ContentIntent, Value: "transaction", Confidence: 0.9, Source: features.SourcePattern},
		features.Feature{Name: "context.is_work", Value: "true", Confidence: 1, Source: features.SourceUser},
	)
}

func TestWeightCurveMatchesSpec(t *testing.T) {
	for conf, want := range map[int]float64{1: 0.20, 3: 0.45, 10: 0.80} {
		if got := Weight(0.9, float64(conf), 0); math.Abs(got-want) > 0.04 {
			t.Errorf("conf=%d: got %v want ≈%v", conf, got, want)
		}
	}
	if Weight(0.9, 0, 5) != 0 {
		t.Error("no confirmations => 0")
	}
	// one error must not destroy a well confirmed rule
	if a, b := Weight(0.9, 10, 0), Weight(0.9, 10, 1); b < 0.85*a || b >= a {
		t.Errorf("a single rejection should attenuate mildly: %v -> %v", a, b)
	}
}

func TestGeneralizationsSkipFreemailDomainsAndUserFeatures(t *testing.T) {
	kinds := func(set *features.Set) map[string]int {
		m := map[string]int{}
		for _, g := range Generalizations(set) {
			m[g.Kind]++
			for _, c := range g.Conds {
				if c.Feature == "context.is_work" {
					t.Fatal("user-only feature leaked into a rule")
				}
			}
		}
		return m
	}
	corp := kinds(fs("bank.example", false))
	if corp[KindSenderEmail] != 1 || corp[KindSenderDomain] != 1 || corp[KindDomainIntent] != 1 || corp[KindIntent] != 1 {
		t.Errorf("corporate: %v", corp)
	}
	free := kinds(fs("gmail.com", true))
	if free[KindSenderDomain] != 0 || free[KindDomainIntent] != 0 || free[KindSenderEmail] != 1 {
		t.Errorf("freemail: %v", free)
	}
}

func TestLearnPositiveGrowsAndSignalReadsIt(t *testing.T) {
	repo, tr := newRepo(), tree(t)
	l := &Learner{Repo: repo, Tree: tr}
	set := fs("store.example", false)
	for i := 0; i < 5; i++ {
		if err := l.Learn(context.Background(), Feedback{Features: set, Category: "finance.purchase", Kind: KindManual}); err != nil {
			t.Fatal(err)
		}
	}
	r, _ := repo.GetRule(context.Background(), "finance.purchase", "sender.domain=store.example", false)
	if r == nil || r.Confirmations != 5 || r.Weight < 0.5 {
		t.Fatalf("rule = %+v", r)
	}
	ev, err := Signal{Repo: repo}.Evidence(context.Background(), email.Email{}, set)
	if err != nil || len(ev) == 0 {
		t.Fatalf("ev=%v err=%v", ev, err)
	}
	for _, e := range ev {
		if e.Category != "finance.purchase" || e.Weight <= 0 || e.Method != "learned" {
			t.Fatalf("bad evidence %+v", e)
		}
	}
	if ev[0].Weight <= ev[1].Weight { // strongest first at full weight, rest discounted
		t.Errorf("expected discounted dependents: %+v", ev)
	}
}

func TestCorrectionCreatesNegativeRuleAndRejects(t *testing.T) {
	repo, tr := newRepo(), tree(t)
	l := &Learner{Repo: repo, Tree: tr}
	set := fs("shop.example", false)
	ctx := context.Background()
	// the system had learned "shop → bank" (wrongly) twice
	for i := 0; i < 2; i++ {
		_ = l.Learn(ctx, Feedback{Features: set, Category: "finance.bank", Kind: KindManual})
	}
	before, _ := repo.GetRule(ctx, "finance.bank", "sender.domain=shop.example", false)
	// now the user corrects: it is a purchase
	if err := l.Learn(ctx, Feedback{Features: set, Category: "finance.purchase", Previous: "finance.bank", Kind: KindCorrect}); err != nil {
		t.Fatal(err)
	}
	after, _ := repo.GetRule(ctx, "finance.bank", "sender.domain=shop.example", false)
	if after.Rejections != 1 || after.Weight >= before.Weight || after.Weight == 0 {
		t.Errorf("positive bank rule should be attenuated, not destroyed: before=%v after=%v", before.Weight, after.Weight)
	}
	neg, _ := repo.GetRule(ctx, "finance.bank", "sender.domain=shop.example", true)
	if neg == nil || !neg.Negative || neg.Confirmations != 1 {
		t.Fatalf("negative rule missing: %+v", neg)
	}
	ev, _ := Signal{Repo: repo}.Evidence(ctx, email.Email{}, set)
	sawNeg := false
	for _, e := range ev {
		if e.Category == "finance.bank" && e.Weight < 0 {
			sawNeg = true
		}
	}
	if !sawNeg {
		t.Errorf("signal should emit negative evidence: %+v", ev)
	}
}

func TestRefinementIsNotAnError(t *testing.T) {
	repo, tr := newRepo(), tree(t)
	l := &Learner{Repo: repo, Tree: tr}
	set := fs("mybank.example", false)
	ctx := context.Background()
	_ = l.Learn(ctx, Feedback{Features: set, Category: "finance", Kind: KindManual})
	// user refines Finance -> Finance/Bank: previous prediction was an ancestor
	_ = l.Learn(ctx, Feedback{Features: set, Category: "finance.bank", Previous: "finance", Kind: KindCorrect})
	if neg, _ := repo.GetRule(ctx, "finance", "sender.domain=mybank.example", true); neg != nil {
		t.Error("refining must not create a negative rule for the ancestor")
	}
	if r, _ := repo.GetRule(ctx, "finance", "sender.domain=mybank.example", false); r.Rejections != 0 {
		t.Error("refining must not reject the ancestor rule")
	}
}

func TestUnknownAndNilAreIgnored(t *testing.T) {
	l := &Learner{Repo: newRepo(), Tree: tree(t)}
	if err := l.Learn(context.Background(), Feedback{Features: fs("a.com", false), Category: categories.UnknownID}); err != nil {
		t.Fatal(err)
	}
	if err := l.Learn(context.Background(), Feedback{Category: "finance"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Learn(context.Background(), Feedback{Features: fs("a.com", false), Category: "nope"}); err == nil {
		t.Fatal("unknown category should error")
	}
}

func TestAmbiguousConditionSharesItsConfirmations(t *testing.T) {
	repo, tr := newRepo(), tree(t)
	l := &Learner{Repo: repo, Tree: tr}
	ctx := context.Background()
	set := fs("acme.example", false)
	// the same domain was filed 9 times as purchase and 1 time as bank
	for i := 0; i < 9; i++ {
		_ = l.Learn(ctx, Feedback{Features: set, Category: "finance.purchase", Kind: KindManual})
	}
	_ = l.Learn(ctx, Feedback{Features: set, Category: "finance.bank", Kind: KindManual})
	ev, _ := Signal{Repo: repo}.Evidence(ctx, email.Email{}, set)
	var purchase, bank float64
	for _, e := range ev {
		switch e.Category {
		case "finance.purchase":
			if e.Weight > purchase {
				purchase = e.Weight
			}
		case "finance.bank":
			if e.Weight > bank {
				bank = e.Weight
			}
		}
	}
	if purchase < 4*bank {
		t.Errorf("a domain confirmed 9:1 must favour purchase clearly: purchase=%.3f bank=%.3f", purchase, bank)
	}
	// a key confirmed for a single category keeps its full weight
	solo := fs("solo.example", false)
	for i := 0; i < 4; i++ {
		_ = l.Learn(ctx, Feedback{Features: solo, Category: "finance.bank", Kind: KindManual})
	}
	ev, _ = Signal{Repo: repo}.Evidence(ctx, email.Email{}, solo)
	r, _ := repo.GetRule(ctx, "finance.bank", "sender.email=a@solo.example", false)
	if len(ev) == 0 || ev[0].Weight < r.Weight-1e-9 {
		t.Errorf("single-category rule must not be diluted: %+v vs %v", ev, r.Weight)
	}
}
