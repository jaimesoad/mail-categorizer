package explain

import (
	"strings"
	"testing"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/classifier"
)

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

func result(tr *categories.Tree) classifier.Result {
	ev := []classifier.Evidence{
		{Category: "finance.bank", Weight: 0.7, Method: classifier.MethodRule, Reason: "Dominio bancario conocido."},
		{Category: "finance", Weight: 0.5, Method: classifier.MethodRule, Reason: "Describe una transacción."},
		{Category: "finance.bank", Weight: 0.4, Method: classifier.MethodSimilarity, Reason: "8 de 10 correos similares fueron Banco."},
		{Category: "finance.purchase", Weight: -0.6, Method: classifier.MethodLearned, Reason: "Aprendido: ese dominio NO suele ir en Compras."},
		{Category: "finance.purchase", Weight: 0.3, Method: classifier.MethodRule, Reason: "Menciona un pedido."},
	}
	c := &classifier.Classifier{Tree: tr, Thresholds: classifier.DefaultThresholds()}
	return c.FromEvidence(ev)
}

func TestExplanationAnswersWhy(t *testing.T) {
	tr := tree(t)
	x := Build(Input{Tree: tr, Result: result(tr), Decision: Know})
	if x.Category != "finance.bank" || x.Path != "Finance / Bank" {
		t.Fatalf("%+v", x)
	}
	joined := strings.Join(x.Reasons, "|")
	for _, want := range []string{"Dominio bancario", "transacción", "correos similares"} {
		if !strings.Contains(joined, want) {
			t.Errorf("reason %q missing from %v", want, x.Reasons)
		}
	}
	if strings.Contains(joined, "pedido") {
		t.Errorf("evidence for another category must not be a reason: %v", x.Reasons)
	}
	if len(x.Against) == 0 || !strings.Contains(x.Against[0], "NO suele") {
		t.Errorf("negative evidence should be reported: %v", x.Against)
	}
	if x.Method != string(classifier.MethodCombined) {
		t.Errorf("rule+similarity => combined, got %s", x.Method)
	}
	if !strings.HasPrefix(x.Headline, "Estoy seguro") {
		t.Errorf("headline: %s", x.Headline)
	}
	lines := strings.Join(x.Lines(), "\n")
	if !strings.Contains(lines, "Razones:") || !strings.Contains(lines, "Finance / Bank") {
		t.Errorf("lines: %s", lines)
	}
}

func TestFourStatesAreDistinguishable(t *testing.T) {
	tr := tree(t)
	res := result(tr)
	heads := map[string]bool{}
	for _, d := range []string{Know, Predict, Ask, Unknown, User} {
		heads[Build(Input{Tree: tr, Result: res, Decision: d}).Headline] = true
	}
	if len(heads) != 5 {
		t.Errorf("each decision state needs its own wording (§41): %v", heads)
	}
}

func TestUserDecisionAndAnswers(t *testing.T) {
	tr := tree(t)
	x := Build(Input{Tree: tr, Result: result(tr), Decision: User, Category: "finance.purchase",
		Answers: []QA{{Question: "¿Es una compra?", Answer: "Sí"}}})
	if x.Method != "user" || x.Score != 1 || !strings.Contains(x.Reasons[0], "usuario") {
		t.Errorf("%+v", x)
	}
	if !strings.Contains(strings.Join(x.Reasons, "|"), "Respondiste «Sí»") {
		t.Errorf("answers must be part of the explanation: %v", x.Reasons)
	}
}

func TestUnknownHasAnHonestExplanation(t *testing.T) {
	tr := tree(t)
	c := &classifier.Classifier{Tree: tr, Thresholds: classifier.DefaultThresholds()}
	x := Build(Input{Tree: tr, Result: c.FromEvidence(nil), Decision: Unknown})
	if x.Category != categories.UnknownID || !strings.Contains(x.Reasons[0], "No hay evidencia") {
		t.Errorf("%+v", x)
	}
}
