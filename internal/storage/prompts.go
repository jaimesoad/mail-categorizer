package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Prompt is a message sent to the user (e.g. through Telegram) that awaits a reply.
type Prompt struct {
	ID         int64
	EmailID    string
	Kind       string // question | confirm | category | audit
	QuestionID string
	ChatID     int64
	MessageID  int64
	Payload    string
	CreatedAt  time.Time
	Resolved   bool
}

// CreatePrompt registers a prompt unless one with the same (email, kind,
// question) already exists. created=false means it was already there.
func (d *DB) CreatePrompt(ctx context.Context, p Prompt, now time.Time) (id int64, created bool, err error) {
	res, err := d.ExecContext(ctx, `INSERT OR IGNORE INTO prompts(email_id,kind,question_id,chat_id,payload,created_at) VALUES (?,?,?,?,?,?)`,
		p.EmailID, p.Kind, p.QuestionID, p.ChatID, p.Payload, ts(now))
	if err != nil {
		return 0, false, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		id, _ = res.LastInsertId()
		return id, true, nil
	}
	err = d.QueryRowContext(ctx, `SELECT id FROM prompts WHERE email_id=? AND kind=? AND question_id=?`, p.EmailID, p.Kind, p.QuestionID).Scan(&id)
	return id, false, err
}

// SetPromptMessage records the chat message that carries a prompt.
func (d *DB) SetPromptMessage(ctx context.Context, id, messageID int64) error {
	_, err := d.ExecContext(ctx, `UPDATE prompts SET message_id=? WHERE id=?`, messageID, id)
	return err
}

// SetPromptPayload replaces the payload (e.g. the category list shown).
func (d *DB) SetPromptPayload(ctx context.Context, id int64, payload string) error {
	_, err := d.ExecContext(ctx, `UPDATE prompts SET payload=? WHERE id=?`, payload, id)
	return err
}

// ResolvePrompt marks a prompt as answered.
func (d *DB) ResolvePrompt(ctx context.Context, id int64, now time.Time) error {
	_, err := d.ExecContext(ctx, `UPDATE prompts SET resolved_at=? WHERE id=?`, ts(now), id)
	return err
}

// DeletePrompt removes a prompt that could not be delivered.
func (d *DB) DeletePrompt(ctx context.Context, id int64) error {
	_, err := d.ExecContext(ctx, `DELETE FROM prompts WHERE id=?`, id)
	return err
}

// GetPrompt loads a prompt.
func (d *DB) GetPrompt(ctx context.Context, id int64) (Prompt, error) {
	var p Prompt
	var created, resolved sql.NullString
	err := d.QueryRowContext(ctx, `SELECT id,email_id,kind,question_id,chat_id,message_id,payload,created_at,resolved_at FROM prompts WHERE id=?`, id).
		Scan(&p.ID, &p.EmailID, &p.Kind, &p.QuestionID, &p.ChatID, &p.MessageID, &p.Payload, &created, &resolved)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	p.CreatedAt, p.Resolved = parseTS(created), resolved.Valid
	return p, err
}

// OpenPrompts counts unresolved prompts.
func (d *DB) OpenPrompts(ctx context.Context) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM prompts WHERE resolved_at IS NULL`).Scan(&n)
	return n, err
}

// EmailsAwaitingQuestion lists emails with an open session (a question is pending)
// that have no question prompt yet, oldest first.
func (d *DB) EmailsAwaitingQuestion(ctx context.Context, limit int) ([]string, error) {
	return d.ids(ctx, `SELECT s.email_id FROM sessions s
WHERE s.status='open' AND s.quiet=0 AND NOT EXISTS (SELECT 1 FROM prompts p WHERE p.email_id=s.email_id AND p.kind='question' AND p.resolved_at IS NULL)
ORDER BY s.updated_at, s.email_id LIMIT ?`, limit)
}

// EmailsAwaitingReview lists recently decided emails that no person has judged
// and that have no confirmation prompt yet (candidates for "was this right?").
func (d *DB) EmailsAwaitingReview(ctx context.Context, limit int) ([]string, error) {
	return d.ids(ctx, `SELECT s.email_id FROM sessions s
WHERE s.status='resolved' AND s.quiet=0 AND s.resolved_by IN ('auto','answers','predicted','unknown')
  AND NOT EXISTS (SELECT 1 FROM prompts p WHERE p.email_id=s.email_id AND p.kind IN ('confirm','category'))
ORDER BY s.updated_at DESC, s.email_id LIMIT ?`, limit)
}

func (d *DB) ids(ctx context.Context, q string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
