// Package questions models the adaptive questions of the Akinator-style
// engine: their definition, how likely each answer is per category, the
// information gain of asking them and the selector that picks the best one.
package questions

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Type of a question (§5.5).
type Type string

const (
	Boolean      Type = "boolean"
	SingleChoice Type = "single_choice"
	MultiChoice  Type = "multi_choice"
	Text         Type = "text"
	CategoryQ    Type = "category"
)

// Source of a question definition.
const (
	SourceConfig = "config"
	SourceLLM    = "llm"
)

// Answer is one selectable answer and the feature value it asserts.
type Answer struct {
	ID           string `yaml:"id" json:"id"`
	Label        string `yaml:"label" json:"label"`
	FeatureValue string `yaml:"value,omitempty" json:"value,omitempty"`
}

// Question is a user-facing question that yields information about a feature.
type Question struct {
	ID      string   `yaml:"id" json:"id"`
	Text    string   `yaml:"text" json:"text"`
	Feature string   `yaml:"feature" json:"feature"`
	Type    Type     `yaml:"type" json:"type"`
	Answers []Answer `yaml:"answers,omitempty" json:"answers,omitempty"`

	// Affinity: category -> P(answer | category). For boolean questions the
	// config shorthand "category: 0.9" means P(yes | category) = 0.9 and is
	// expanded by Normalize. Categories without an entry inherit from their
	// closest ancestor; without any entry the answers are uninformative.
	Affinity map[string]map[string]float64 `yaml:"-" json:"affinity,omitempty"`

	Reliability float64 `yaml:"reliability,omitempty" json:"reliability,omitempty"` // P(the user answers correctly); default 0.95
	Confidence  float64 `yaml:"confidence,omitempty" json:"confidence,omitempty"`   // trust in the question itself; default 1
	Complexity  float64 `yaml:"complexity,omitempty" json:"complexity,omitempty"`   // extra penalty (needs context, ambiguous...)

	Disabled  bool   `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	Validated bool   `yaml:"-" json:"validated"`
	Source    string `yaml:"-" json:"source,omitempty"`
}

var featureName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// Boolean answer identifiers.
const (
	Yes = "yes"
	No  = "no"
)

// Normalize fills defaults: boolean answers, reliability, confidence and the
// validated flag for configuration-defined questions.
func (q *Question) Normalize() {
	if q.Type == "" {
		q.Type = Boolean
	}
	if q.Type == Boolean && len(q.Answers) == 0 {
		q.Answers = []Answer{{ID: Yes, Label: "Sí", FeatureValue: "true"}, {ID: No, Label: "No", FeatureValue: "false"}}
	}
	for i := range q.Answers {
		if q.Answers[i].Label == "" {
			q.Answers[i].Label = q.Answers[i].ID
		}
		if q.Answers[i].FeatureValue == "" {
			q.Answers[i].FeatureValue = q.Answers[i].ID
		}
	}
	if q.Reliability == 0 {
		q.Reliability = 0.95
	}
	if q.Confidence == 0 {
		q.Confidence = 1
	}
	if q.Source == "" {
		q.Source = SourceConfig
	}
}

// Validate checks structural soundness. LLM-generated questions go through it
// before being stored (§14).
func (q Question) Validate() error {
	if q.ID == "" {
		return fmt.Errorf("question id required")
	}
	if strings.TrimSpace(q.Text) == "" {
		return fmt.Errorf("question %q: text required", q.ID)
	}
	if n := utf8.RuneCountInString(q.Text); n > 200 {
		return fmt.Errorf("question %q: text too long (%d > 200)", q.ID, n)
	}
	if !featureName.MatchString(q.Feature) {
		return fmt.Errorf("question %q: feature %q must look like \"group.name\"", q.ID, q.Feature)
	}
	switch q.Type {
	case Boolean, SingleChoice, MultiChoice:
		min := 2
		if len(q.Answers) < min {
			return fmt.Errorf("question %q: at least %d answers required", q.ID, min)
		}
		seen := map[string]bool{}
		for _, a := range q.Answers {
			if a.ID == "" || seen[a.ID] {
				return fmt.Errorf("question %q: empty or duplicate answer id %q", q.ID, a.ID)
			}
			seen[a.ID] = true
		}
		if q.Type == Boolean && !(seen[Yes] && seen[No] && len(q.Answers) == 2) {
			return fmt.Errorf("question %q: boolean questions need exactly the answers yes/no", q.ID)
		}
	case Text, CategoryQ:
	default:
		return fmt.Errorf("question %q: unknown type %q", q.ID, q.Type)
	}
	for cat, row := range q.Affinity {
		sum := 0.0
		for a, p := range row {
			if p < 0 || p > 1 {
				return fmt.Errorf("question %q: affinity %s/%s out of [0,1]", q.ID, cat, a)
			}
			if !q.hasAnswer(a) {
				return fmt.Errorf("question %q: affinity for unknown answer %q", q.ID, a)
			}
			sum += p
		}
		if sum <= 0 {
			return fmt.Errorf("question %q: empty affinity row for %s", q.ID, cat)
		}
	}
	if q.Reliability < 0 || q.Reliability > 1 || q.Confidence < 0 || q.Confidence > 1 {
		return fmt.Errorf("question %q: reliability/confidence must be in [0,1]", q.ID)
	}
	return nil
}

func (q Question) hasAnswer(id string) bool {
	for _, a := range q.Answers {
		if a.ID == id {
			return true
		}
	}
	return false
}

// Selectable reports whether information gain can be computed for the type.
func (q Question) Selectable() bool { return q.Type == Boolean || q.Type == SingleChoice }

// AnswerIDs lists the answer ids in definition order.
func (q Question) AnswerIDs() []string {
	ids := make([]string, len(q.Answers))
	for i, a := range q.Answers {
		ids[i] = a.ID
	}
	return ids
}

var yesWords = map[string]string{"yes": Yes, "y": Yes, "true": Yes, "1": Yes, "si": Yes, "sí": Yes, "s": Yes, "no": No, "n": No, "false": No, "0": No}

// ResolveAnswer maps raw user input to the canonical answer id(s) of the
// question. Boolean accepts yes/no/sí/true/...; choices accept the answer id or
// label (case-insensitive); multi_choice accepts a comma-separated list.
func (q Question) ResolveAnswer(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty answer")
	}
	switch q.Type {
	case Boolean:
		if id, ok := yesWords[strings.ToLower(raw)]; ok {
			return []string{id}, nil
		}
		return nil, fmt.Errorf("answer %q is not yes/no", raw)
	case SingleChoice, MultiChoice:
		parts := []string{raw}
		if q.Type == MultiChoice {
			parts = strings.Split(raw, ",")
		}
		var out []string
		for _, p := range parts {
			p = strings.TrimSpace(p)
			found := ""
			for _, a := range q.Answers {
				if strings.EqualFold(a.ID, p) || strings.EqualFold(a.Label, p) {
					found = a.ID
					break
				}
			}
			if found == "" {
				return nil, fmt.Errorf("answer %q is not one of %v", p, q.AnswerIDs())
			}
			out = append(out, found)
		}
		return out, nil
	case Text:
		if utf8.RuneCountInString(raw) > 200 {
			return nil, fmt.Errorf("text answer too long")
		}
		return []string{raw}, nil
	case CategoryQ:
		return []string{raw}, nil
	}
	return nil, fmt.Errorf("unsupported question type %q", q.Type)
}

// FeatureValue returns the feature value asserted by an answer id.
func (q Question) FeatureValue(answerID string) string {
	for _, a := range q.Answers {
		if a.ID == answerID {
			return a.FeatureValue
		}
	}
	return answerID // text / category answers carry their own value
}
