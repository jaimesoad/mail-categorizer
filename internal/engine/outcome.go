package engine

import (
	"github.com/jaimesoad/mail-categorizer/internal/classifier"
	"github.com/jaimesoad/mail-categorizer/internal/explain"
	"github.com/jaimesoad/mail-categorizer/internal/questions"
)

// PendingQuestion is a question presented to the user.
type PendingQuestion struct {
	ID              string             `json:"id"`
	Text            string             `json:"text"`
	Type            questions.Type     `json:"type"`
	Feature         string             `json:"feature"`
	Answers         []questions.Answer `json:"answers,omitempty"`
	InformationGain float64            `json:"information_gain"`
	Utility         float64            `json:"utility"`
}

// Outcome is the externally visible state of one email's classification.
type Outcome struct {
	EmailID   string `json:"email_id"`
	SessionID string `json:"session_id"`

	Decision   string                 `json:"decision"` // know | predict | ask | unknown | user
	Category   string                 `json:"category"` // current best / final category id
	Path       string                 `json:"path"`     // human readable "Finance / Bank"
	Score      float64                `json:"score"`
	Method     classifier.Method      `json:"method"`
	Resolved   bool                   `json:"resolved"`
	Candidates []classifier.Candidate `json:"candidates"`

	NeedsQuestion       bool             `json:"needs_question"`
	Question            *PendingQuestion `json:"question,omitempty"`
	NeedsManual         bool             `json:"needs_manual"` // "I don't know": please file it manually
	RequestConfirmation bool             `json:"request_confirmation"`
	Learned             bool             `json:"learned"`
	AlreadyProcessed    bool             `json:"already_processed,omitempty"`
	QuestionsAsked      int              `json:"questions_asked"`

	Explanation explain.Explanation `json:"explanation"`
}
