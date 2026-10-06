package config

import (
	"strings"
	"testing"
)

func TestDefaultConfigBuilds(t *testing.T) {
	c, err := LoadOrDefault("")
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Build()
	if err != nil {
		t.Fatal(err)
	}
	if !b.Tree.Has("finance.bank") || !b.Tree.Has("unknown") {
		t.Error("tree missing categories")
	}
	if len(b.Questions) < 5 || b.Rules.Len() < 10 {
		t.Errorf("questions=%d rules=%d", len(b.Questions), b.Rules.Len())
	}
	for _, q := range b.Questions {
		if !q.Validated || len(q.Answers) != 2 {
			t.Errorf("question %s not normalized: %+v", q.ID, q)
		}
	}
	if got := b.Questions[0].Affinity["finance.purchase"]["yes"]; got != 0.95 {
		t.Errorf("affinity shorthand: %v", got)
	}
}

func build(y string) error {
	c, err := Parse([]byte(y))
	if err != nil {
		return err
	}
	_, err = c.Build()
	return err
}

func TestValidationErrors(t *testing.T) {
	cats := "categories:\n  - {id: a, name: A}\n"
	cases := map[string]string{
		"unknown key":        "bogus: 1\n",
		"bad thresholds":     "thresholds: {auto: 0.5, ask: 0.9, min_margin: 0.1}\n",
		"missing parent":     "categories:\n  - {id: a, parent: zzz}\n",
		"cycle":              "categories:\n  - {id: a, parent: b}\n  - {id: b, parent: a}\n",
		"duplicate category": "categories:\n  - {id: a}\n  - {id: a}\n",
		"rule bad category":  cats + "rules:\n  - {id: r, category: nope, weight: 0.5, when: {feature: x.y, value: z}}\n",
		"rule bad regex":     cats + "rules:\n  - {id: r, category: a, weight: 0.5, when: {feature: x.y, op: regex, value: '('}}\n",
		"affinity bad cat":   cats + "questions:\n  - {id: q, text: '?', feature: ctx.q, affinity: {zzz: 0.5}}\n",
		"question dup":       cats + "questions:\n  - {id: q, text: '?', feature: ctx.q}\n  - {id: q, text: '?', feature: ctx.q}\n",
		"question bad feat":  cats + "questions:\n  - {id: q, text: '?', feature: Bad}\n",
	}
	for name, y := range cases {
		if err := build(y); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if err := build(cats + "questions:\n  - {id: q, text: '?', feature: ctx.q, affinity: {a: 0.7}}\n"); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
	if err := build(""); err != nil && !strings.Contains(err.Error(), "EOF") {
		t.Errorf("empty config: %v", err)
	}
}
