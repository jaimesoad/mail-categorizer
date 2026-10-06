package learning

import (
	"context"
	"fmt"
	"time"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/features"
)

// Kind of feedback.
type Kind string

const (
	// KindAnswer: the session was resolved thanks to the user's answers.
	KindAnswer Kind = "answer"
	// KindConfirm: the user confirmed an automatic classification.
	KindConfirm Kind = "confirm"
	// KindCorrect: the user said the prediction was wrong and chose another category.
	KindCorrect Kind = "correct"
	// KindManual: the user filed the email by hand ("Move to...", weight 1.0, §33).
	KindManual Kind = "manual"
)

// Feedback is one learning event.
type Feedback struct {
	EmailID  string
	Features *features.Set
	Category string // the category the user established (never "unknown")
	Previous string // what the system had predicted ("" when none)
	Kind     Kind
}

// Learner updates learned rules from feedback.
type Learner struct {
	Repo Repo
	Tree *categories.Tree
	Now  func() time.Time
}

// Learn records the evidence of one feedback event. It only ever learns from
// user-provided signals; automatic classifications never reach this method,
// which prevents the system from reinforcing its own mistakes.
func (l *Learner) Learn(ctx context.Context, fb Feedback) error {
	if fb.Category == "" || fb.Category == categories.UnknownID || fb.Features == nil {
		return nil
	}
	if !l.Tree.Has(fb.Category) {
		return fmt.Errorf("learn: unknown category %q", fb.Category)
	}
	now := l.now()
	gens := Generalizations(fb.Features)
	if len(gens) == 0 {
		return nil
	}
	keys := make([]string, len(gens))
	for i, g := range gens {
		keys[i] = g.Key
	}
	existing, err := l.Repo.RulesByKeys(ctx, keys)
	if err != nil {
		return err
	}

	// 1. rules that pointed somewhere unrelated to the true category were wrong
	for _, r := range existing {
		if r.CategoryID == fb.Category || l.related(r.CategoryID, fb.Category) {
			continue
		}
		if !r.Negative {
			r.Rejections++ // a positive rule claimed a different category
		} else {
			continue // a negative rule against an unrelated category stays valid
		}
		l.refresh(&r, now)
		if err := l.Repo.SaveRule(ctx, r); err != nil {
			return err
		}
	}

	wrongPrev := fb.Previous != "" && fb.Previous != categories.UnknownID &&
		fb.Previous != fb.Category && !l.related(fb.Previous, fb.Category) &&
		(fb.Kind == KindCorrect || fb.Kind == KindManual)

	for _, g := range gens {
		// 2. positive evidence for the established category
		if err := l.bump(ctx, fb.Category, g, false, now); err != nil {
			return err
		}
		// 3. a negative rule that said "not this category" was contradicted
		if neg, err := l.Repo.GetRule(ctx, fb.Category, g.Key, true); err != nil {
			return err
		} else if neg != nil {
			neg.Rejections++
			l.refresh(neg, now)
			if err := l.Repo.SaveRule(ctx, *neg); err != nil {
				return err
			}
		}
		// 4. the wrong prediction becomes negative knowledge (§21), except for
		//    the most general level, which would over-penalize.
		if wrongPrev && g.Kind != KindIntent {
			if err := l.bump(ctx, fb.Previous, g, true, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *Learner) bump(ctx context.Context, category string, g Generalization, negative bool, now time.Time) error {
	r, err := l.Repo.GetRule(ctx, category, g.Key, negative)
	if err != nil {
		return err
	}
	if r == nil {
		r = &Rule{CategoryID: category, Key: g.Key, Kind: g.Kind, Conditions: g.Conds, Negative: negative, CreatedAt: now}
	}
	r.Observations++
	r.Confirmations++
	l.refresh(r, now)
	return l.Repo.SaveRule(ctx, *r)
}

func (l *Learner) refresh(r *Rule, now time.Time) {
	r.Weight = Weight(weightCap[r.Kind], r.Confirmations, r.Rejections)
	r.UpdatedAt = now
	if r.Kind == "" {
		r.Kind = KindSenderDomain
	}
}

// related: one category is the ancestor of the other, so confirming one does
// not contradict the other (a rule for "finance" is not wrong when the user
// refines the email to "finance.bank").
func (l *Learner) related(a, b string) bool {
	return l.Tree.IsAncestor(a, b) || l.Tree.IsAncestor(b, a)
}

func (l *Learner) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now().UTC()
}
