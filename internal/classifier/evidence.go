// Package classifier combines weighted evidence from independent signals
// (rules, learned knowledge, similarity...) into a hierarchical distribution
// over categories, and decides how confident that distribution is.
package classifier

import (
	"context"

	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/features"
)

// Method says which kind of signal produced a classification (§5.4).
type Method string

const (
	MethodRule       Method = "rule"
	MethodLearned    Method = "learned"
	MethodSimilarity Method = "similarity"
	MethodModel      Method = "model"
	MethodUser       Method = "user"
	MethodCombined   Method = "combined"
)

// Evidence is one piece of support (Weight > 0) or contradiction (Weight < 0)
// for a category. |Weight| is in (0,1].
type Evidence struct {
	Category string  `json:"category"`
	Weight   float64 `json:"weight"`
	Method   Method  `json:"method"`
	Reason   string  `json:"reason"`
	Ref      string  `json:"ref,omitempty"` // rule id / learned rule key
}

// Signal produces evidence for an email. Implementations must be
// deterministic and must return evidence in a stable order.
type Signal interface {
	Evidence(ctx context.Context, e email.Email, f *features.Set) ([]Evidence, error)
}

// SignalFunc adapts a function to Signal.
type SignalFunc func(ctx context.Context, e email.Email, f *features.Set) ([]Evidence, error)

func (fn SignalFunc) Evidence(ctx context.Context, e email.Email, f *features.Set) ([]Evidence, error) {
	return fn(ctx, e, f)
}
