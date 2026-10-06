// Package explain answers "why was this email put here?" (§2.4) in plain language.
package explain

import (
	"fmt"
	"sort"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/classifier"
)

// Decision states of §41.
const (
	Know    = "know"    // "I know"
	Predict = "predict" // "I have a good prediction"
	Ask     = "ask"     // "I need to ask"
	Unknown = "unknown" // "I don't know"
	User    = "user"    // the user decided
	Teacher = "teacher" // the local LLM decided; awaiting human verification
)

// QA is a question the user answered during the session.
type QA struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// Alt is an alternative the system considered.
type Alt struct {
	Category string  `json:"category"`
	Path     string  `json:"path"`
	Score    float64 `json:"score"`
}

// Input is everything needed to explain a classification.
type Input struct {
	Tree     *categories.Tree
	Result   classifier.Result
	Decision string
	Category string // the final category; defaults to Result.Best
	Answers  []QA
}

// Explanation is the structured explanation.
type Explanation struct {
	Category     string   `json:"category"`
	Path         string   `json:"path"`
	Score        float64  `json:"score"`
	Method       string   `json:"method"`
	Decision     string   `json:"decision"`
	Headline     string   `json:"headline"`
	Reasons      []string `json:"reasons"`
	Against      []string `json:"against,omitempty"`
	Answers      []QA     `json:"answers,omitempty"`
	Alternatives []Alt    `json:"alternatives,omitempty"`
}

const maxReasons = 6

// Build produces the explanation.
func Build(in Input) Explanation {
	cat := in.Category
	if cat == "" {
		cat = in.Result.Best.Category
	}
	score := 0.0
	for _, c := range in.Result.Candidates {
		if c.Category == cat {
			score = c.Score
		}
	}
	if cat == "" {
		cat = categories.UnknownID
	}
	x := Explanation{
		Category: cat, Path: in.Tree.Path(cat), Score: round2(score), Decision: in.Decision,
		Method: string(classifier.DominantMethod(in.Tree, cat, in.Result.Evidence)), Answers: in.Answers,
	}
	if in.Decision == User {
		x.Method = string(classifier.MethodUser)
		x.Score = 1
	}
	if in.Decision == Teacher {
		x.Method = string(classifier.MethodModel)
	}
	x.Headline = headline(in.Decision, x.Path, x.Score)

	seen := map[string]bool{}
	if in.Decision == User {
		x.Reasons = append(x.Reasons, "La categoría fue establecida por el usuario.")
	}
	if in.Decision == Teacher {
		x.Reasons = append(x.Reasons, "La categoría fue propuesta por el LLM local durante el preentrenamiento.")
	}
	for _, e := range classifier.Relevant(in.Tree, in.Result.Evidence, cat) {
		if !seen[e.Reason] && len(x.Reasons) < maxReasons {
			seen[e.Reason] = true
			x.Reasons = append(x.Reasons, e.Reason)
		}
	}
	for _, qa := range in.Answers {
		x.Reasons = append(x.Reasons, fmt.Sprintf("Respondiste «%s» a «%s».", qa.Answer, qa.Question))
	}
	if len(x.Reasons) == 0 {
		x.Reasons = []string{"No hay evidencia suficiente a favor de ninguna categoría."}
	}

	// negative evidence that argued against a category
	for _, e := range in.Result.Evidence {
		if e.Weight < 0 {
			line := fmt.Sprintf("%s: %s", in.Tree.Path(e.Category), e.Reason)
			if !seen["!"+line] {
				seen["!"+line] = true
				x.Against = append(x.Against, line)
			}
		}
	}
	sort.Strings(x.Against)

	for _, c := range in.Result.Candidates {
		if c.Category == cat || c.Score < 0.03 || len(x.Alternatives) >= 3 {
			continue
		}
		x.Alternatives = append(x.Alternatives, Alt{c.Category, in.Tree.Path(c.Category), round2(c.Score)})
	}
	return x
}

func headline(decision, path string, score float64) string {
	switch decision {
	case Know:
		return fmt.Sprintf("Estoy seguro: %s (%.0f%%).", path, score*100)
	case Predict:
		return fmt.Sprintf("Mi mejor predicción es %s (%.0f%%), pero no estoy seguro.", path, score*100)
	case Ask:
		return "Necesito preguntarte algo para decidir."
	case User:
		return fmt.Sprintf("Clasificado por ti como %s.", path)
	case Teacher:
		return fmt.Sprintf("El asistente local (LLM) lo clasificó como %s; falta tu verificación.", path)
	}
	return "No tengo evidencia suficiente para clasificar este correo."
}

// Lines renders the explanation as text lines.
func (x Explanation) Lines() []string {
	lines := []string{x.Headline}
	if x.Category != categories.UnknownID {
		lines = append(lines, fmt.Sprintf("Categoría: %s", x.Path))
	}
	lines = append(lines, "Razones:")
	for _, r := range x.Reasons {
		lines = append(lines, "- "+r)
	}
	if len(x.Against) > 0 {
		lines = append(lines, "En contra de otras categorías:")
		for _, a := range x.Against {
			lines = append(lines, "- "+a)
		}
	}
	if len(x.Alternatives) > 0 {
		lines = append(lines, "Alternativas:")
		for _, a := range x.Alternatives {
			lines = append(lines, fmt.Sprintf("- %s (%.0f%%)", a.Path, a.Score*100))
		}
	}
	return lines
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
