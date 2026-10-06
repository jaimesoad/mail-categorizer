package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/questions"
)

// SyncCategories upserts the configured categories. Categories removed from the
// configuration are disabled, never deleted, so history stays intact.
func (d *DB) SyncCategories(ctx context.Context, cats []categories.Category) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		configured := map[string]bool{}
		// parents first so the FK is satisfied
		pending := append([]categories.Category(nil), cats...)
		done := map[string]bool{}
		for len(pending) > 0 {
			var next []categories.Category
			progressed := false
			for _, c := range pending {
				if c.ParentID != "" && !done[c.ParentID] {
					var n int
					if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM categories WHERE id=?`, c.ParentID).Scan(&n); err != nil {
						return err
					}
					if n == 0 {
						next = append(next, c)
						continue
					}
				}
				_, err := tx.ExecContext(ctx, `INSERT INTO categories(id,parent_id,name,description,enabled) VALUES (?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET parent_id=excluded.parent_id, name=excluded.name, description=excluded.description, enabled=excluded.enabled`,
					c.ID, nullStr(c.ParentID), c.Name, nullStr(c.Description), b2i(c.Enabled))
				if err != nil {
					return err
				}
				done[c.ID], configured[c.ID] = true, true
				progressed = true
			}
			if !progressed {
				return fmt.Errorf("sync categories: unresolved parents")
			}
			pending = next
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM categories WHERE enabled=1`)
		if err != nil {
			return err
		}
		var stale []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			if !configured[id] {
				stale = append(stale, id)
			}
		}
		rows.Close()
		for _, id := range stale {
			if _, err := tx.ExecContext(ctx, `UPDATE categories SET enabled=0 WHERE id=?`, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// StoredCategories returns every category row (including disabled ones).
func (d *DB) StoredCategories(ctx context.Context) ([]categories.Category, error) {
	rows, err := d.QueryContext(ctx, `SELECT id,parent_id,name,description,enabled FROM categories ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []categories.Category
	for rows.Next() {
		var c categories.Category
		var p, desc sql.NullString
		var en int
		if err := rows.Scan(&c.ID, &p, &c.Name, &desc, &en); err != nil {
			return nil, err
		}
		c.ParentID, c.Description, c.Enabled = p.String, desc.String, en == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

// SaveQuestion upserts a question and its answer mapping (§5.5/5.6). When
// keepState is true an existing row's enabled/validated flags are preserved
// (used when syncing config over questions an operator already approved).
func (d *DB) SaveQuestion(ctx context.Context, q questions.Question, keepState bool) error {
	def, err := json.Marshal(q)
	if err != nil {
		return err
	}
	return d.Tx(ctx, func(tx *sql.Tx) error {
		stmt := `INSERT INTO questions(id,text,feature,type,enabled,source,validated,definition) VALUES (?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET text=excluded.text, feature=excluded.feature, type=excluded.type, source=excluded.source, definition=excluded.definition`
		if !keepState {
			stmt += `, enabled=excluded.enabled, validated=excluded.validated`
		}
		if _, err := tx.ExecContext(ctx, stmt, q.ID, q.Text, q.Feature, string(q.Type), b2i(!q.Disabled), q.Source, b2i(q.Validated), string(def)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM question_answers WHERE question_id=?`, q.ID); err != nil {
			return err
		}
		for _, a := range q.Answers {
			if _, err := tx.ExecContext(ctx, `INSERT INTO question_answers(id,question_id,answer_value,feature,feature_value) VALUES (?,?,?,?,?)`,
				q.ID+":"+a.ID, q.ID, a.ID, q.Feature, a.FeatureValue); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetQuestionState approves/rejects (validated) and enables/disables a question.
func (d *DB) SetQuestionState(ctx context.Context, id string, validated, enabled bool) error {
	res, err := d.ExecContext(ctx, `UPDATE questions SET validated=?, enabled=? WHERE id=?`, b2i(validated), b2i(enabled), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	d.bump()
	return nil
}

// Questions loads every stored question with its current state.
func (d *DB) Questions(ctx context.Context) ([]questions.Question, error) {
	rows, err := d.QueryContext(ctx, `SELECT definition,enabled,validated FROM questions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []questions.Question
	for rows.Next() {
		var def string
		var en, val int
		if err := rows.Scan(&def, &en, &val); err != nil {
			return nil, err
		}
		var q questions.Question
		if err := json.Unmarshal([]byte(def), &q); err != nil {
			return nil, fmt.Errorf("question definition: %w", err)
		}
		q.Disabled, q.Validated = en == 0, val == 1
		out = append(out, q)
	}
	return out, rows.Err()
}
