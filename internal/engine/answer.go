package engine

import (
	"context"
	"strings"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/classifier"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/features"
	"github.com/jaimesoad/mail-categorizer/internal/learning"
	"github.com/jaimesoad/mail-categorizer/internal/questions"
	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

// Answer records the user's answer to a question and advances the session
// (§12–§13, §17): the answer becomes a user-sourced feature, updates the
// candidates by Bayes' rule and, if it settles the matter, is learned.
func (e *Engine) Answer(ctx context.Context, emailID, questionID, raw string) (*Outcome, error) {
	defer e.locks.lock(emailID)()
	st, _, err := e.loadSession(ctx, emailID)
	if err != nil {
		return nil, err
	}
	if st.Decision == DecisionKnow || st.Decision == DecisionUser || st.Decision == DecisionTeacher {
		return nil, ErrConflict2("session already resolved")
	}
	q, ok := e.questionByID(st, questionID)
	if !ok {
		return nil, invalid("unknown or disabled question %q", questionID)
	}
	for _, a := range st.Answers {
		if a.QuestionID == questionID {
			return nil, ErrConflict2("question %q was already answered or skipped", questionID)
		}
	}
	ids, err := q.ResolveAnswer(raw)
	if err != nil {
		return nil, invalid("%v", err)
	}

	// "category" questions are manual labelling.
	if q.Type == questions.CategoryQ {
		if err := e.checkLabelable(ids[0]); err != nil {
			return nil, err
		}
		return e.label(ctx, emailID, st, ids[0], true)
	}

	em, err := e.db.GetEmail(ctx, emailID)
	if err != nil {
		return nil, err
	}
	rec := answerRec{QuestionID: questionID, Answer: strings.Join(ids, ",")}
	var fvals []string
	for _, id := range ids {
		fvals = append(fvals, q.FeatureValue(id))
	}
	rec.FeatureValue = strings.Join(fvals, ",")

	before := classifier.Entropy(st.Candidates)
	prevBest := st.Candidates[0].Category
	expected := 0.0
	if st.Pending != nil && st.Pending.QuestionID == questionID {
		expected = st.Pending.ExpectedGain
	}

	// the answer is knowledge about the email
	var fs []features.Feature
	for _, v := range fvals {
		fs = append(fs, features.Feature{Name: q.Feature, Value: v, Confidence: 1, Source: features.SourceUser})
	}
	if err := e.db.AddFeatures(ctx, emailID, fs...); err != nil {
		return nil, err
	}
	set, err := e.db.Features(ctx, emailID)
	if err != nil {
		return nil, err
	}
	// rules that reference the answered feature may fire now: freeze new evidence only
	st.Evidence = e.mergeRuleEvidence(ctx, st.Evidence, em, set)

	st.Asked = append(st.Asked, questionID)
	st.Answers = append(st.Answers, rec)
	if err := e.replay(ctx, st); err != nil {
		return nil, err
	}
	rec.Gain = before - classifier.Entropy(st.Candidates)
	st.Answers[len(st.Answers)-1] = rec

	if err := e.advance(ctx, st, em, set); err != nil {
		return nil, err
	}
	teacher := actorOf(ctx) == ActorLLM
	if teacher && st.ResolvedBy == ResolvedAnswers {
		st.ResolvedBy = ResolvedTeacher // keep the trust level of who answered
	}
	if err := e.persist(ctx, emailID, st, ""); err != nil {
		return nil, err
	}
	now := e.now()
	if err := e.db.AddInteraction(ctx, storage.Interaction{
		EmailID: emailID, SessionID: sessionID(ctx, e.db, emailID), Kind: storage.KindAnswer, QuestionID: questionID, Answer: rec.Answer,
		PreviousPrediction: prevBest, ResultingCategory: st.Candidates[0].Category, ExpectedGain: expected, RealizedGain: rec.Gain,
		Actor: string(actorOf(ctx)),
	}, now); err != nil {
		return nil, err
	}

	learned := false
	if st.Decision == DecisionKnow && (st.ResolvedBy == ResolvedAnswers || st.ResolvedBy == ResolvedTeacher) {
		learned, err = e.learn(ctx, emailID, set, st.Final, "", learning.KindAnswer, teacher)
		if err != nil {
			return nil, err
		}
	}
	out, err := e.outcome(ctx, emailID, st)
	if out != nil {
		out.Learned = learned
	}
	return out, err
}

// Skip records that the user declined to answer a question (it will not be
// asked again for this email, and lowers its response rate, §2.2).
func (e *Engine) Skip(ctx context.Context, emailID, questionID string) (*Outcome, error) {
	defer e.locks.lock(emailID)()
	st, _, err := e.loadSession(ctx, emailID)
	if err != nil {
		return nil, err
	}
	if st.Decision != DecisionAsk {
		return nil, ErrConflict2("no open question to skip")
	}
	if _, ok := e.questionByID(st, questionID); !ok {
		return nil, invalid("unknown question %q", questionID)
	}
	for _, a := range st.Answers {
		if a.QuestionID == questionID {
			return nil, ErrConflict2("question %q was already answered or skipped", questionID)
		}
	}
	em, err := e.db.GetEmail(ctx, emailID)
	if err != nil {
		return nil, err
	}
	set, err := e.db.Features(ctx, emailID)
	if err != nil {
		return nil, err
	}
	st.Asked = append(st.Asked, questionID)
	st.Answers = append(st.Answers, answerRec{QuestionID: questionID, Skipped: true})
	if err := e.advance(ctx, st, em, set); err != nil {
		return nil, err
	}
	if err := e.persist(ctx, emailID, st, ""); err != nil {
		return nil, err
	}
	if err := e.db.AddInteraction(ctx, storage.Interaction{
		EmailID: emailID, SessionID: sessionID(ctx, e.db, emailID), Kind: storage.KindSkip, QuestionID: questionID,
		Actor: string(actorOf(ctx)),
	}, e.now()); err != nil {
		return nil, err
	}
	return e.outcome(ctx, emailID, st)
}

// replay rebuilds the candidates from the frozen evidence and re-applies every
// answer in order. Using one code path for it keeps sessions reproducible.
func (e *Engine) replay(ctx context.Context, st *sessionState) error {
	stats, err := e.db.QuestionStats(ctx)
	if err != nil {
		return err
	}
	cands := classifier.Aggregate(e.tree, st.Evidence)
	st.Base = cands
	for _, a := range st.Answers {
		if a.Skipped {
			continue
		}
		q, ok := e.questionByID(st, a.QuestionID)
		if !ok || !q.Selectable() {
			continue
		}
		m := questions.Model{Q: q, Tree: e.tree, Stats: stats[q.ID]}
		cands = questions.Posterior(cands, m, a.Answer)
	}
	st.Candidates = cands
	return nil
}

// mergeRuleEvidence appends evidence of rules that fire only now (because an
// answer introduced a feature they reference) without touching the frozen rest.
func (e *Engine) mergeRuleEvidence(ctx context.Context, ev []classifier.Evidence, em email.Email, set *features.Set) []classifier.Evidence {
	have := map[string]bool{}
	for _, x := range ev {
		if x.Method == classifier.MethodRule {
			have[x.Ref] = true
		}
	}
	fresh, err := e.built.Rules.Evidence(ctx, em, set)
	if err != nil {
		return ev
	}
	for _, x := range fresh {
		if !have[x.Ref] {
			ev = append(ev, x)
		}
	}
	return ev
}

func sessionID(ctx context.Context, db *storage.DB, emailID string) string {
	if row, err := db.GetSession(ctx, emailID); err == nil {
		return row.ID
	}
	return ""
}

// ErrConflict2 formats a conflict error.
func ErrConflict2(format string, a ...any) error {
	return wrap(ErrConflict, format, a...)
}

func (e *Engine) learn(ctx context.Context, emailID string, set *features.Set, cat, prev string, kind learning.Kind, teacher bool) (bool, error) {
	if e.learner == nil || cat == "" || cat == categories.UnknownID {
		return false, nil
	}
	err := e.learner.Learn(ctx, learning.Feedback{EmailID: emailID, Features: set, Category: cat, Previous: prev, Kind: kind, Teacher: teacher})
	return err == nil, err
}
