package engine

import (
	"context"
	"encoding/json"

	"github.com/jaimesoad/mail-categorizer/internal/classifier"
	"github.com/jaimesoad/mail-categorizer/internal/questions"
	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

// Decision states (§41).
const (
	DecisionKnow    = "know"
	DecisionPredict = "predict"
	DecisionAsk     = "ask"
	DecisionUnknown = "unknown"
	DecisionUser    = "user"
)

// Resolution sources stored in sessions.resolved_by.
const (
	ResolvedAuto      = "auto"
	ResolvedAnswers   = "answers"
	ResolvedPredicted = "predicted"
	ResolvedAncestor  = "ancestor"
	ResolvedUnknown   = "unknown"
	ResolvedUser      = "user"
)

type answerRec struct {
	QuestionID   string   `json:"question_id"`
	Answer       string   `json:"answer"` // canonical answer id(s), comma separated; "" when skipped
	FeatureValue string   `json:"feature_value,omitempty"`
	Skipped      bool     `json:"skipped,omitempty"`
	Gain         float64  `json:"gain,omitempty"`
	Raw          []string `json:"-"`
}

type pendingRec struct {
	QuestionID   string  `json:"question_id"`
	ExpectedGain float64 `json:"expected_gain"`
	Utility      float64 `json:"utility"`
}

// sessionState is the persisted JSON of a session (§30).
type sessionState struct {
	Evidence   []classifier.Evidence  `json:"evidence"`
	Base       []classifier.Candidate `json:"base"`
	Candidates []classifier.Candidate `json:"candidates"`
	Asked      []string               `json:"asked"`
	Answers    []answerRec            `json:"answers"`
	Pending    *pendingRec            `json:"pending,omitempty"`
	Proposed   []questions.Question   `json:"proposed,omitempty"` // session-scoped, unvalidated, generated questions

	Decision   string  `json:"decision"`
	Final      string  `json:"final"`
	FinalScore float64 `json:"final_score"`
	Method     string  `json:"method"`
	ResolvedBy string  `json:"resolved_by"`
	Initial    string  `json:"initial_decision"`
}

func (s *sessionState) answered() int {
	n := 0
	for _, a := range s.Answers {
		if !a.Skipped {
			n++
		}
	}
	return n
}

func (s *sessionState) marshal() []byte {
	b, _ := json.Marshal(s)
	return b
}

func (e *Engine) loadSession(ctx context.Context, emailID string) (*sessionState, storage.SessionRow, error) {
	row, err := e.db.GetSession(ctx, emailID)
	if err != nil {
		return nil, row, err
	}
	var st sessionState
	if err := json.Unmarshal(row.State, &st); err != nil {
		return nil, row, err
	}
	return &st, row, nil
}
