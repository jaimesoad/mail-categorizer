package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/classifier"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/explain"
	"github.com/jaimesoad/mail-categorizer/internal/features"
	"github.com/jaimesoad/mail-categorizer/internal/questions"
	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

// Process runs the full pipeline for one email (§26): normalize → features →
// signals → classification → decide (classify automatically, or open a
// session with a question). Processing the same message twice is idempotent.
func (e *Engine) Process(ctx context.Context, raw email.Email) (*Outcome, error) {
	if raw.From.Email == "" && raw.Subject == "" && raw.BodyText == "" {
		return nil, invalid("email has no sender, subject or body")
	}
	em := email.Normalize(raw, e.cfg.Privacy.MaxBodyChars)

	if id, ok, err := e.db.EmailIDByHash(ctx, em.RawHash); err != nil {
		return nil, err
	} else if ok {
		out, err := e.Get(ctx, id)
		if out != nil {
			out.AlreadyProcessed = true
		}
		return out, err
	}

	// Extract before inserting so history features describe the past only.
	fs, err := e.extractor.Extract(ctx, em)
	if err != nil {
		return nil, fmt.Errorf("extract features: %w", err)
	}
	set := features.NewSet(fs...)
	if e.enricher != nil {
		if extra, err := e.enricher.Enrich(ctx, em, set); err == nil {
			set.Add(extra...)
		}
	}

	stored := em
	if !e.cfg.Privacy.StoreBody {
		stored.BodyText = ""
	}
	id, existed, err := e.db.InsertEmail(ctx, stored, set.All(), e.now())
	if err != nil {
		if err == storage.ErrIDConflict {
			return nil, fmt.Errorf("%w: %v", ErrConflict, err)
		}
		return nil, err
	}
	if existed {
		out, err := e.Get(ctx, id)
		if out != nil {
			out.AlreadyProcessed = true
		}
		return out, err
	}
	em.ID = id
	defer e.locks.lock(id)()

	res, err := e.cls.Classify(ctx, em, set)
	if err != nil {
		return nil, err
	}
	st := &sessionState{Evidence: res.Evidence, Base: res.Candidates, Candidates: res.Candidates}
	if err := e.advance(ctx, st, em, set); err != nil {
		return nil, err
	}
	st.Initial = st.Decision
	if err := e.persist(ctx, id, st, "open"); err != nil {
		return nil, err
	}
	return e.outcome(ctx, id, st)
}

// advance decides what happens next for a session: finish with a category or
// pick the next question. It is the single place where thresholds, question
// limits and the unknown/ancestor fallbacks (§15, §31, §41) are applied.
func (e *Engine) advance(ctx context.Context, st *sessionState, em email.Email, set *features.Set) error {
	res := e.cls.Build(st.Candidates, st.Evidence)
	st.Pending = nil

	if res.Level == classifier.HighConfidence {
		by := ResolvedAuto
		if st.answered() > 0 {
			by = ResolvedAnswers
		}
		e.finalize(st, DecisionKnow, res.Best.Category, res.Best.Score, by)
		return nil
	}

	if len(st.Asked) < e.cfg.MaxQuestions {
		q, err := e.nextQuestion(ctx, st, em, set)
		if err != nil {
			return err
		}
		if q != nil {
			st.Pending = q
			st.Decision, st.Final, st.FinalScore, st.ResolvedBy = DecisionAsk, res.Best.Category, res.Best.Score, ""
			st.Method = string(res.Method)
			return nil
		}
	}

	// No (more) questions: be explicit about how sure we are.
	if res.Level == classifier.MediumConfidence {
		e.finalize(st, DecisionPredict, res.Best.Category, res.Best.Score, ResolvedPredicted)
		return nil
	}
	if cat, mass, ok := e.ancestorFallback(res); ok {
		d := DecisionPredict
		if mass >= e.cfg.Thresholds.Auto {
			d = DecisionKnow
		}
		e.finalize(st, d, cat, mass, ResolvedAncestor)
		return nil
	}
	// score of "unknown" = how little we believe in the best alternative
	e.finalize(st, DecisionUnknown, categories.UnknownID, 1-res.Best.Score, ResolvedUnknown)
	return nil
}

// ancestorFallback (M6): when the best leaf is undecidable, the deepest node on
// its path (itself included, with its descendants) whose subtree mass is solid
// is still useful ("it is Finance, not sure which").
func (e *Engine) ancestorFallback(res classifier.Result) (string, float64, bool) {
	best := res.Best.Category
	if best == categories.UnknownID || !e.tree.Has(best) {
		return "", 0, false
	}
	for _, node := range append([]string{best}, e.tree.Ancestors(best)...) { // deepest first
		if m := classifier.SubtreeMass(e.tree, res.Candidates, node); m >= e.cfg.Thresholds.Ask {
			return node, m, true
		}
	}
	return "", 0, false
}

func (e *Engine) finalize(st *sessionState, decision, cat string, score float64, by string) {
	st.Decision, st.Final, st.FinalScore, st.ResolvedBy, st.Pending = decision, cat, score, by, nil
	st.Method = string(classifier.DominantMethod(e.tree, cat, st.Evidence))
	if st.answered() > 0 && by == ResolvedAnswers {
		st.Method = string(classifier.MethodCombined)
	}
}

// nextQuestion selects the most useful unasked question, falling back to an
// optional LLM proposal when nothing in the catalogue separates the candidates.
func (e *Engine) nextQuestion(ctx context.Context, st *sessionState, em email.Email, set *features.Set) (*pendingRec, error) {
	sess := questions.Session{Candidates: st.Candidates, Features: set, Asked: st.Asked}
	sc, err := e.sel.Select(ctx, sess)
	if err != nil {
		return nil, err
	}
	if sc != nil {
		return &pendingRec{QuestionID: sc.Question.ID, ExpectedGain: sc.Gain, Utility: sc.Utility}, nil
	}
	if e.proposer == nil || len(st.Proposed) > 0 || len(st.Candidates) < 2 {
		return nil, nil
	}
	q, err := e.proposer.Propose(ctx, questions.ProposeInput{Email: em, Candidates: st.Candidates, Features: set, Asked: st.Asked})
	if err != nil || q == nil {
		return nil, nil // the proposer is best-effort
	}
	q.Normalize()
	q.Source, q.Validated, q.Disabled = questions.SourceLLM, false, true
	if q.Validate() != nil {
		return nil, nil
	}
	// Persist for operator review; usable in this session only until approved.
	if err := e.db.SaveQuestion(ctx, *q, true); err != nil {
		return nil, err
	}
	m := questions.Model{Q: *q, Tree: e.tree}
	gain := questions.InformationGain(st.Candidates, m)
	if gain < e.sel.Params.MinGain {
		return nil, nil
	}
	st.Proposed = append(st.Proposed, *q)
	return &pendingRec{QuestionID: q.ID, ExpectedGain: gain, Utility: gain * q.Confidence}, nil
}

// questionByID finds a catalogue question or a session-scoped proposal.
func (e *Engine) questionByID(st *sessionState, id string) (questions.Question, bool) {
	for _, q := range st.Proposed {
		if q.ID == id {
			return q, true
		}
	}
	for _, q := range e.sel.Questions {
		if q.ID == id && !q.Disabled && q.Validated {
			return q, true
		}
	}
	return questions.Question{}, false
}

// persist writes classification rows and the session. status is "open" or "resolved".
func (e *Engine) persist(ctx context.Context, emailID string, st *sessionState, _ string) error {
	now := e.now()
	status := "resolved"
	if st.Decision == DecisionAsk {
		status = "open"
	}
	expl, _ := json.Marshal(e.explain(st))
	var rows []storage.Classification
	final := st.Final
	hasFinal := false
	for i, c := range st.Candidates {
		if i >= 5 || (c.Score < 0.01 && c.Category != final) {
			break
		}
		r := storage.Classification{
			EmailID: emailID, CategoryID: c.Category, Score: c.Score,
			Method: string(classifier.DominantMethod(e.tree, c.Category, st.Evidence)),
		}
		if status == "resolved" && c.Category == final {
			r.Final, r.Method, r.Explanation, r.Score, hasFinal = true, st.Method, string(expl), st.FinalScore, true
		}
		rows = append(rows, r)
	}
	if status == "resolved" && !hasFinal {
		rows = append(rows, storage.Classification{EmailID: emailID, CategoryID: final, Score: st.FinalScore, Method: st.Method, Explanation: string(expl), Final: true})
	}
	if status == "open" && len(rows) > 0 {
		rows[0].Explanation = string(expl)
	}
	if err := e.db.SaveClassification(ctx, emailID, rows, now); err != nil {
		return err
	}
	row := storage.SessionRow{
		EmailID: emailID, Status: status, InitialDecision: st.Initial, QuestionsAsked: len(st.Asked),
		FinalCategory: final, ResolvedBy: st.ResolvedBy, State: st.marshal(),
	}
	if existing, err := e.db.GetSession(ctx, emailID); err == nil {
		row.ID = existing.ID
	}
	if status == "open" {
		row.FinalCategory, row.ResolvedBy = "", ""
	}
	return e.db.SaveSession(ctx, row, now)
}

func (e *Engine) explain(st *sessionState) explain.Explanation {
	res := e.cls.Build(st.Candidates, st.Evidence)
	var qa []explain.QA
	for _, a := range st.Answers {
		if a.Skipped {
			continue
		}
		if q, ok := e.questionByID(st, a.QuestionID); ok {
			qa = append(qa, explain.QA{Question: q.Text, Answer: answerLabel(q, a.Answer)})
		}
	}
	x := explain.Build(explain.Input{Tree: e.tree, Result: res, Decision: st.Decision, Category: st.Final, Answers: qa})
	if st.Decision != DecisionUser && st.FinalScore > 0 {
		x.Score = float64(int(st.FinalScore*100+0.5)) / 100
	}
	return x
}

func answerLabel(q questions.Question, ans string) string {
	var labels []string
	for _, id := range strings.Split(ans, ",") {
		l := id
		for _, a := range q.Answers {
			if a.ID == id {
				l = a.Label
			}
		}
		labels = append(labels, l)
	}
	return strings.Join(labels, ", ")
}

// outcome builds the public view of a session.
func (e *Engine) outcome(ctx context.Context, emailID string, st *sessionState) (*Outcome, error) {
	row, err := e.db.GetSession(ctx, emailID)
	if err != nil {
		return nil, err
	}
	o := &Outcome{
		EmailID: emailID, SessionID: row.ID, Decision: st.Decision, Category: st.Final,
		Path: e.tree.Path(st.Final), Score: st.FinalScore, Method: classifier.Method(st.Method),
		Resolved: st.Decision != DecisionAsk, Candidates: st.Candidates, QuestionsAsked: len(st.Asked),
		Explanation: e.explain(st),
	}
	switch st.Decision {
	case DecisionAsk:
		o.NeedsQuestion = true
		if q, ok := e.questionByID(st, st.Pending.QuestionID); ok {
			o.Question = &PendingQuestion{
				ID: q.ID, Text: q.Text, Type: q.Type, Feature: q.Feature, Answers: q.Answers,
				InformationGain: round3(st.Pending.ExpectedGain), Utility: round3(st.Pending.Utility),
			}
		}
	case DecisionUnknown:
		o.NeedsManual = true
		o.RequestConfirmation = false
	case DecisionPredict:
		o.RequestConfirmation = true
	case DecisionKnow:
		o.RequestConfirmation = st.answered() == 0 && e.shouldConfirm(emailID, st.FinalScore)
	}
	return o, nil
}

func round3(f float64) float64 { return float64(int(f*1000+0.5)) / 1000 }

// Get returns the current outcome of an already processed email.
func (e *Engine) Get(ctx context.Context, emailID string) (*Outcome, error) {
	st, _, err := e.loadSession(ctx, emailID)
	if err != nil {
		return nil, err
	}
	return e.outcome(ctx, emailID, st)
}

// Explain returns the structured explanation of the current classification.
func (e *Engine) Explain(ctx context.Context, emailID string) (explain.Explanation, error) {
	st, _, err := e.loadSession(ctx, emailID)
	if err != nil {
		return explain.Explanation{}, err
	}
	return e.explain(st), nil
}

// NextQuestion returns the pending question of an email, selecting one if the
// session is open but none is pending (e.g. after a restart).
func (e *Engine) NextQuestion(ctx context.Context, emailID string) (*Outcome, error) {
	defer e.locks.lock(emailID)()
	st, _, err := e.loadSession(ctx, emailID)
	if err != nil {
		return nil, err
	}
	if st.Decision == DecisionAsk && st.Pending != nil {
		return e.outcome(ctx, emailID, st)
	}
	return e.outcome(ctx, emailID, st) // resolved: NeedsQuestion=false tells the caller
}
