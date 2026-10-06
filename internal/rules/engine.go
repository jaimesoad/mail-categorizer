package rules

import (
	"context"
	"fmt"

	"github.com/jaimesoad/mail-categorizer/internal/classifier"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/features"
)

// Rule is a user-defined deterministic rule: when its condition holds it adds
// Weight (negative = suppress) to Category.
type Rule struct {
	ID       string    `yaml:"id" json:"id"`
	Category string    `yaml:"category" json:"category"`
	Weight   float64   `yaml:"weight" json:"weight"`
	Reason   string    `yaml:"reason,omitempty" json:"reason,omitempty"`
	When     Condition `yaml:"when" json:"when"`
	Disabled bool      `yaml:"disabled,omitempty" json:"disabled,omitempty"`
}

type compiled struct {
	Rule
	m Matcher
}

// Engine evaluates a fixed rule list. It implements classifier.Signal.
type Engine struct{ rules []compiled }

// NewEngine compiles rules, rejecting duplicates, invalid weights and bad conditions.
func NewEngine(rs []Rule) (*Engine, error) {
	e := &Engine{}
	seen := map[string]bool{}
	for _, r := range rs {
		if r.ID == "" {
			return nil, fmt.Errorf("rule with empty id")
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("duplicate rule id %q", r.ID)
		}
		seen[r.ID] = true
		if r.Weight == 0 || r.Weight < -1 || r.Weight > 1 {
			return nil, fmt.Errorf("rule %q: weight must be in [-1,1] and non-zero", r.ID)
		}
		if r.Category == "" {
			return nil, fmt.Errorf("rule %q: category required", r.ID)
		}
		m, err := Compile(r.When)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", r.ID, err)
		}
		if !r.Disabled {
			e.rules = append(e.rules, compiled{r, m})
		}
	}
	return e, nil
}

// Evidence implements classifier.Signal.
func (e *Engine) Evidence(_ context.Context, _ email.Email, f *features.Set) ([]classifier.Evidence, error) {
	var out []classifier.Evidence
	for _, r := range e.rules {
		if !r.m.Match(f) {
			continue
		}
		reason := r.Reason
		if reason == "" {
			reason = "Regla " + r.ID
		}
		out = append(out, classifier.Evidence{
			Category: r.Category, Weight: r.Weight, Method: classifier.MethodRule, Reason: reason, Ref: r.ID,
		})
	}
	return out, nil
}

// Rules returns the IDs of the active rules (for diagnostics).
func (e *Engine) Len() int { return len(e.rules) }
