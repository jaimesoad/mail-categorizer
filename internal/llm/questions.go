package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/questions"
)

// favorStrength is P(favored answer | candidate) assumed for generated questions.
const favorStrength = 0.85

// QuestionGenerator proposes a question that separates the current candidates
// (§14). It implements engine.QuestionProposer. Output is validated here and
// again by the engine; it is stored unvalidated and only becomes a permanent
// part of the catalogue when an operator approves it.
type QuestionGenerator struct {
	P    Provider
	Tree *categories.Tree
}

// features that are safe to describe to a model: abstract booleans/kinds, never
// raw content, addresses or names.
var safePrefixes = []string{"content.contains_", "content.has_", "content.intent", "content.language", "headers.", "attachment.has_", "sender.is_", "sender.domain.is_"}

func safeFeature(name string) bool {
	for _, p := range safePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

type generated struct {
	Question string              `json:"question"`
	Answers  []string            `json:"answers"`
	Feature  string              `json:"feature"`
	Mapping  map[string]string   `json:"mapping"`
	Favors   map[string][]string `json:"favors"` // answer -> categories it points to
}

// Propose implements engine.QuestionProposer.
func (g QuestionGenerator) Propose(ctx context.Context, in questions.ProposeInput) (*questions.Question, error) {
	var cats []string
	valid := map[string]bool{}
	for i, c := range in.Candidates {
		if i >= 4 || c.Score < 0.05 || c.Category == categories.UnknownID {
			continue
		}
		cats = append(cats, fmt.Sprintf("%s (%s)", c.Category, g.Tree.Path(c.Category)))
		valid[c.Category] = true
	}
	if len(cats) < 2 {
		return nil, nil
	}
	known := map[string]string{}
	for _, f := range in.Features.All() {
		if safeFeature(f.Name) {
			known[f.Name] = f.Value
		}
	}
	kj, _ := json.Marshal(known)
	ans, err := g.P.Complete(ctx, Request{
		System: `You design one short yes/no question (in Spanish, under 20 words) that helps a user decide which category an email belongs to. ` +
			`Answer ONLY with JSON: {"question":"...","answers":["yes","no"],"feature":"group.name","mapping":{"yes":"<value>","no":"<value>"},"favors":{"yes":["<category id>"],"no":["<category id>"]}}. ` +
			`"favors" lists, for each answer, which of the candidate category ids it points to. Use only the given candidate ids.`,
		User: fmt.Sprintf("Candidate categories:\n- %s\nKnown features: %s\n", strings.Join(cats, "\n- "), kj),
	})
	if err != nil {
		return nil, err
	}
	return buildQuestion(ans, valid)
}

func buildQuestion(ans string, valid map[string]bool) (*questions.Question, error) {
	js, err := extractJSON(ans)
	if err != nil {
		return nil, err
	}
	var g generated
	if err := json.Unmarshal([]byte(js), &g); err != nil {
		return nil, fmt.Errorf("llm: bad question JSON: %w", err)
	}
	sum := sha256.Sum256([]byte(g.Question + "|" + g.Feature))
	q := &questions.Question{
		ID: "gen_" + hex.EncodeToString(sum[:5]), Text: strings.TrimSpace(g.Question), Feature: strings.TrimSpace(g.Feature),
		Type: questions.Boolean, Confidence: 0.7, Source: questions.SourceLLM,
		Affinity: map[string]map[string]float64{},
	}
	if len(g.Answers) != 2 || !(has(g.Answers, "yes") && has(g.Answers, "no")) {
		return nil, fmt.Errorf("llm: only yes/no questions are supported")
	}
	q.Normalize()
	for a, v := range g.Mapping {
		for i := range q.Answers {
			if q.Answers[i].ID == a && strings.TrimSpace(v) != "" {
				q.Answers[i].FeatureValue = strings.TrimSpace(v)
			}
		}
	}
	// favors -> affinity: P(answer | candidate)
	pointed := map[string]string{}
	for a, cs := range g.Favors {
		if a != questions.Yes && a != questions.No {
			continue
		}
		for _, c := range cs {
			if valid[c] {
				pointed[c] = a
			}
		}
	}
	if len(pointed) == 0 {
		return nil, fmt.Errorf("llm: question does not separate any candidate")
	}
	for c, a := range pointed {
		other := questions.Yes
		if a == questions.Yes {
			other = questions.No
		}
		q.Affinity[c] = map[string]float64{a: favorStrength, other: 1 - favorStrength}
	}
	if err := q.Validate(); err != nil {
		return nil, err
	}
	return q, nil
}

func has(xs []string, s string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
