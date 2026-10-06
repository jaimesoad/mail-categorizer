package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// SessionRow is the persisted envelope of a classification session. The
// engine owns the JSON State (frozen evidence, candidates, asked, answers).
type SessionRow struct {
	ID              string
	EmailID         string
	Status          string // open | resolved
	InitialDecision string
	QuestionsAsked  int
	FinalCategory   string
	ResolvedBy      string
	State           []byte
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SaveSession upserts a session by email id.
func (d *DB) SaveSession(ctx context.Context, s SessionRow, now time.Time) error {
	if s.ID == "" {
		s.ID = NewID()
	}
	_, err := d.ExecContext(ctx, `INSERT INTO sessions(id,email_id,status,initial_decision,questions_asked,final_category,resolved_by,state,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(email_id) DO UPDATE SET status=excluded.status, questions_asked=excluded.questions_asked,
  final_category=excluded.final_category, resolved_by=excluded.resolved_by, state=excluded.state, updated_at=excluded.updated_at`,
		s.ID, s.EmailID, s.Status, s.InitialDecision, s.QuestionsAsked, nullStr(s.FinalCategory), nullStr(s.ResolvedBy), string(s.State), ts(now), ts(now))
	d.bump()
	d.bumpLabels()
	return err
}

// GetSession loads the session of an email.
func (d *DB) GetSession(ctx context.Context, emailID string) (SessionRow, error) {
	var s SessionRow
	var fc, rb, c, u sql.NullString
	var state string
	err := d.QueryRowContext(ctx, `SELECT id,email_id,status,initial_decision,questions_asked,final_category,resolved_by,state,created_at,updated_at FROM sessions WHERE email_id=?`, emailID).
		Scan(&s.ID, &s.EmailID, &s.Status, &s.InitialDecision, &s.QuestionsAsked, &fc, &rb, &state, &c, &u)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	s.FinalCategory, s.ResolvedBy, s.State = fc.String, rb.String, []byte(state)
	s.CreatedAt, s.UpdatedAt = parseTS(c), parseTS(u)
	return s, err
}

// Interaction kinds.
const (
	KindAnswer  = "answer"
	KindSkip    = "skip"
	KindConfirm = "confirm"
	KindCorrect = "correct"
	KindManual  = "manual"
)

// Interaction is one user interaction (§5.7).
type Interaction struct {
	ID                 string
	EmailID            string
	SessionID          string
	Kind               string
	QuestionID         string
	Answer             string
	PreviousPrediction string
	ResultingCategory  string
	ExpectedGain       float64
	RealizedGain       float64
	CreatedAt          time.Time
}

// AddInteraction records an interaction.
func (d *DB) AddInteraction(ctx context.Context, in Interaction, now time.Time) error {
	if in.ID == "" {
		in.ID = NewID()
	}
	_, err := d.ExecContext(ctx, `INSERT INTO interactions(id,email_id,session_id,kind,question_id,answer,previous_prediction,resulting_category,expected_gain,realized_gain,created_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		in.ID, in.EmailID, nullStr(in.SessionID), in.Kind, nullStr(in.QuestionID), nullableAnswer(in), nullStr(in.PreviousPrediction),
		nullStr(in.ResultingCategory), in.ExpectedGain, in.RealizedGain, ts(now))
	d.bump()
	if in.Kind == KindConfirm || in.Kind == KindCorrect || in.Kind == KindManual {
		d.bumpLabels()
	}
	return err
}

func nullableAnswer(in Interaction) sql.NullString {
	if in.Kind == KindSkip {
		return sql.NullString{}
	}
	return nullStr(in.Answer)
}

// Interactions lists an email's interactions in chronological order.
func (d *DB) Interactions(ctx context.Context, emailID string) ([]Interaction, error) {
	rows, err := d.QueryContext(ctx, `SELECT id,COALESCE(session_id,''),kind,COALESCE(question_id,''),COALESCE(answer,''),COALESCE(previous_prediction,''),COALESCE(resulting_category,''),COALESCE(expected_gain,0),COALESCE(realized_gain,0),created_at
FROM interactions WHERE email_id=? ORDER BY created_at, rowid`, emailID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Interaction
	for rows.Next() {
		in := Interaction{EmailID: emailID}
		var c sql.NullString
		if err := rows.Scan(&in.ID, &in.SessionID, &in.Kind, &in.QuestionID, &in.Answer, &in.PreviousPrediction, &in.ResultingCategory, &in.ExpectedGain, &in.RealizedGain, &c); err != nil {
			return nil, err
		}
		in.CreatedAt = parseTS(c)
		out = append(out, in)
	}
	return out, rows.Err()
}
