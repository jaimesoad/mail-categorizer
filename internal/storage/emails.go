package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/features"
)

// ErrIDConflict is returned when an email id exists with different content.
var ErrIDConflict = errors.New("email id already exists with different content")

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// InsertEmail stores an email and its features atomically. If an email with the
// same raw_hash already exists, nothing is written and (existingID, true) is
// returned: the pipeline is idempotent per message.
func (d *DB) InsertEmail(ctx context.Context, e email.Email, fs []features.Feature, now time.Time) (id string, existed bool, err error) {
	err = d.Tx(ctx, func(tx *sql.Tx) error {
		var existing string
		switch qerr := tx.QueryRowContext(ctx, `SELECT id FROM emails WHERE raw_hash=?`, e.RawHash).Scan(&existing); {
		case qerr == nil:
			id, existed = existing, true
			return nil
		case !errors.Is(qerr, sql.ErrNoRows):
			return qerr
		}
		var dup int
		if qerr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM emails WHERE id=?`, e.ID).Scan(&dup); qerr != nil {
			return qerr
		}
		if dup > 0 {
			return ErrIDConflict
		}
		_, ierr := tx.ExecContext(ctx, `INSERT INTO emails
(id,message_id,thread_id,sender_email,sender_name,sender_domain,subject,body_text,received_at,raw_hash,created_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			e.ID, nullStr(e.MessageID), nullStr(e.ThreadID), e.From.Email, e.From.Name, features.RegistrableDomain(e.From.Domain()),
			e.Subject, e.BodyText, nullTime(e.ReceivedAt), e.RawHash, ts(now))
		if ierr != nil {
			return ierr
		}
		id = e.ID
		return insertFeatures(ctx, tx, e.ID, fs)
	})
	return
}

func nullTime(t time.Time) sql.NullString {
	if t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: ts(t), Valid: true}
}

func insertFeatures(ctx context.Context, tx *sql.Tx, id string, fs []features.Feature) error {
	st, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO email_features(email_id,feature,value,confidence,source) VALUES (?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, f := range fs {
		if _, err := st.ExecContext(ctx, id, f.Name, f.Value, f.Confidence, string(f.Source)); err != nil {
			return err
		}
	}
	return nil
}

// SaveFeatures replaces the stored features of an email.
func (d *DB) SaveFeatures(ctx context.Context, id string, fs []features.Feature) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM email_features WHERE email_id=?`, id); err != nil {
			return err
		}
		return insertFeatures(ctx, tx, id, fs)
	})
}

// AddFeatures upserts features without removing existing ones (e.g. answers).
func (d *DB) AddFeatures(ctx context.Context, id string, fs ...features.Feature) error {
	return d.Tx(ctx, func(tx *sql.Tx) error { return insertFeatures(ctx, tx, id, fs) })
}

// Features loads the stored feature set of an email.
func (d *DB) Features(ctx context.Context, id string) (*features.Set, error) {
	rows, err := d.QueryContext(ctx, `SELECT feature,value,confidence,source FROM email_features WHERE email_id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := features.NewSet()
	for rows.Next() {
		var f features.Feature
		var src string
		if err := rows.Scan(&f.Name, &f.Value, &f.Confidence, &src); err != nil {
			return nil, err
		}
		f.Source = features.Source(src)
		set.Add(f)
	}
	return set, rows.Err()
}

// GetEmail loads an email by id.
func (d *DB) GetEmail(ctx context.Context, id string) (email.Email, error) {
	var e email.Email
	var msg, thr, recv sql.NullString
	var subj, body, sname sql.NullString
	err := d.QueryRowContext(ctx, `SELECT id,message_id,thread_id,sender_email,sender_name,subject,body_text,received_at,raw_hash FROM emails WHERE id=?`, id).
		Scan(&e.ID, &msg, &thr, &e.From.Email, &sname, &subj, &body, &recv, &e.RawHash)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	if err != nil {
		return e, err
	}
	e.MessageID, e.ThreadID, e.From.Name = msg.String, thr.String, sname.String
	e.Subject, e.BodyText = subj.String, body.String
	e.ReceivedAt = parseTS(recv)
	return e, nil
}

// PurgeContent erases the textual content of an email (subject, body and the
// features derived from them) while keeping sender/header features, the final
// classification and everything learned from it (§24).
func (d *DB) PurgeContent(ctx context.Context, id string, now time.Time) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE emails SET subject='', body_text='', purged_at=? WHERE id=?`, ts(now), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM email_features WHERE email_id=? AND (feature LIKE 'content.%' OR feature LIKE 'subject.%')`, id)
		d.bump()
		d.bumpLabels()
		return err
	})
}

// PurgeAllContent applies PurgeContent to every email.
func (d *DB) PurgeAllContent(ctx context.Context, now time.Time) (int64, error) {
	var n int64
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE emails SET subject='', body_text='', purged_at=? WHERE purged_at IS NULL`, ts(now))
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		_, err = tx.ExecContext(ctx, `DELETE FROM email_features WHERE feature LIKE 'content.%' OR feature LIKE 'subject.%'`)
		return err
	})
	d.bump()
	d.bumpLabels()
	return n, err
}

// DeleteEmail removes an email and everything attached to it (features,
// classification, session, interactions). Learned rules are kept.
func (d *DB) DeleteEmail(ctx context.Context, id string) error {
	res, err := d.ExecContext(ctx, `DELETE FROM emails WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	d.bump()
	d.bumpLabels()
	return nil
}

// EmailIDByHash returns the id of an already stored email with this raw hash.
func (d *DB) EmailIDByHash(ctx context.Context, hash string) (string, bool, error) {
	var id string
	err := d.QueryRowContext(ctx, `SELECT id FROM emails WHERE raw_hash=?`, hash).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

// Classification is one stored candidate score.
type Classification struct {
	EmailID     string
	CategoryID  string
	Score       float64
	Method      string
	Explanation string
	Final       bool
	CreatedAt   time.Time
}

// SaveClassification replaces the stored candidates of an email. The row whose
// category equals final is marked final; the explanation is stored on it.
func (d *DB) SaveClassification(ctx context.Context, emailID string, rows []Classification, now time.Time) error {
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM classifications WHERE email_id=?`, emailID); err != nil {
			return err
		}
		for _, r := range rows {
			if _, err := tx.ExecContext(ctx, `INSERT INTO classifications(email_id,category_id,score,method,explanation,is_final,created_at) VALUES (?,?,?,?,?,?,?)`,
				emailID, r.CategoryID, r.Score, r.Method, nullStr(r.Explanation), b2i(r.Final), ts(now)); err != nil {
				return err
			}
		}
		return nil
	})
	d.bump()
	return err
}

// FinalClassification returns the final classification row of an email.
func (d *DB) FinalClassification(ctx context.Context, emailID string) (Classification, error) {
	var c Classification
	var expl, at sql.NullString
	err := d.QueryRowContext(ctx, `SELECT email_id,category_id,score,method,explanation,created_at FROM classifications WHERE email_id=? AND is_final=1`, emailID).
		Scan(&c.EmailID, &c.CategoryID, &c.Score, &c.Method, &expl, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.Explanation, c.CreatedAt, c.Final = expl.String, parseTS(at), true
	return c, err
}

// Candidates returns all stored candidate rows, highest score first.
func (d *DB) Candidates(ctx context.Context, emailID string) ([]Classification, error) {
	rows, err := d.QueryContext(ctx, `SELECT category_id,score,method,is_final FROM classifications WHERE email_id=? ORDER BY score DESC, category_id`, emailID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Classification
	for rows.Next() {
		c := Classification{EmailID: emailID}
		var fin int
		if err := rows.Scan(&c.CategoryID, &c.Score, &c.Method, &fin); err != nil {
			return nil, err
		}
		c.Final = fin == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListEmailIDs returns email ids, newest first, optionally filtered by final category.
func (d *DB) ListEmailIDs(ctx context.Context, category string, limit int) ([]string, error) {
	q := `SELECT e.id FROM emails e`
	var args []any
	if category != "" {
		q += ` JOIN classifications c ON c.email_id=e.id AND c.is_final=1 WHERE c.category_id=?`
		args = append(args, category)
	}
	q += ` ORDER BY e.created_at DESC, e.id LIMIT ?`
	if limit <= 0 {
		limit = 100
	}
	args = append(args, limit)
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

var _ = fmt.Sprintf

// PurgeAllContentForTest purges with the current time; convenience for tests.
func (d *DB) PurgeAllContentForTest(ctx context.Context) error {
	_, err := d.PurgeAllContent(ctx, time.Now().UTC())
	return err
}
