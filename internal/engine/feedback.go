package engine

import (
	"context"
	"fmt"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/classifier"
	"github.com/jaimesoad/mail-categorizer/internal/learning"
	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

func wrap(base error, format string, a ...any) error {
	return fmt.Errorf("%w: %s", base, fmt.Sprintf(format, a...))
}

func (e *Engine) checkLabelable(cat string) error {
	if cat == categories.UnknownID {
		return invalid("%q is not a valid target category", cat)
	}
	if !e.tree.Has(cat) {
		return invalid("unknown category %q", cat)
	}
	if !e.tree.Enabled(cat) {
		return invalid("category %q is disabled", cat)
	}
	return nil
}

// Feedback applies the user's verdict on the current classification (§16,
// §20, §33). correct=true confirms it; correct=false requires the right
// category. A user label has maximum weight: the email is final, with score 1.
func (e *Engine) Feedback(ctx context.Context, emailID string, correct bool, category string) (*Outcome, error) {
	defer e.locks.lock(emailID)()
	st, _, err := e.loadSession(ctx, emailID)
	if err != nil {
		return nil, err
	}
	if correct {
		cat := st.Final
		if st.Decision == DecisionAsk || cat == categories.UnknownID || cat == "" {
			return nil, invalid("there is no classification to confirm yet")
		}
		return e.label(ctx, emailID, st, cat, false)
	}
	if category == "" {
		return nil, invalid("category is required when correct=false")
	}
	if err := e.checkLabelable(category); err != nil {
		return nil, err
	}
	return e.label(ctx, emailID, st, category, false)
}

// Move files an email into a category by hand ("Move to...", §33).
func (e *Engine) Move(ctx context.Context, emailID, category string) (*Outcome, error) {
	defer e.locks.lock(emailID)()
	st, _, err := e.loadSession(ctx, emailID)
	if err != nil {
		return nil, err
	}
	if err := e.checkLabelable(category); err != nil {
		return nil, err
	}
	return e.label(ctx, emailID, st, category, false)
}

// label stores a user-established category. The caller holds the email lock.
func (e *Engine) label(ctx context.Context, emailID string, st *sessionState, cat string, viaQuestion bool) (*Outcome, error) {
	set, err := e.db.Features(ctx, emailID)
	if err != nil {
		return nil, err
	}
	prev := st.Final
	if st.Decision == DecisionAsk && len(st.Candidates) > 0 {
		prev = st.Candidates[0].Category
	}
	kind, lkind := storage.KindManual, learning.KindManual
	switch {
	case prev == cat && st.Decision != DecisionAsk:
		kind, lkind = storage.KindConfirm, learning.KindConfirm
	case prev != "" && prev != categories.UnknownID && prev != cat:
		kind, lkind = storage.KindCorrect, learning.KindCorrect
		if st.Decision == DecisionAsk {
			kind, lkind = storage.KindManual, learning.KindManual // nothing was committed yet
			prev = ""
		}
	}

	st.Decision, st.Final, st.FinalScore, st.ResolvedBy = DecisionUser, cat, 1.0, ResolvedUser
	st.Method = string(classifier.MethodUser)
	st.Pending = nil
	if err := e.persist(ctx, emailID, st, ""); err != nil {
		return nil, err
	}
	if err := e.db.AddInteraction(ctx, storage.Interaction{
		EmailID: emailID, SessionID: sessionID(ctx, e.db, emailID), Kind: kind,
		PreviousPrediction: prev, ResultingCategory: cat,
	}, e.now()); err != nil {
		return nil, err
	}
	learned, err := e.learn(ctx, emailID, set, cat, prev, lkind)
	if err != nil {
		return nil, err
	}
	out, err := e.outcome(ctx, emailID, st)
	if out != nil {
		out.Learned = learned
	}
	return out, err
}
