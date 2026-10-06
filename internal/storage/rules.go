package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jaimesoad/mail-categorizer/internal/learning"
)

const ruleCols = `id,category_id,key,kind,negative,conditions,weight,observations,confirmations,rejections,teacher_confirmations,created_at,updated_at`

func scanRule(sc interface{ Scan(...any) error }) (learning.Rule, error) {
	var r learning.Rule
	var neg int
	var conds string
	var c, u sql.NullString
	if err := sc.Scan(&r.ID, &r.CategoryID, &r.Key, &r.Kind, &neg, &conds, &r.Weight, &r.Observations, &r.Confirmations, &r.Rejections, &r.TeacherConfirmations, &c, &u); err != nil {
		return r, err
	}
	r.Negative = neg == 1
	if err := json.Unmarshal([]byte(conds), &r.Conditions); err != nil {
		return r, err
	}
	r.CreatedAt, r.UpdatedAt = parseTS(c), parseTS(u)
	return r, nil
}

// RulesByKeys implements learning.Repo.
func (d *DB) RulesByKeys(ctx context.Context, keys []string) ([]learning.Rule, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	rows, err := d.QueryContext(ctx, `SELECT `+ruleCols+` FROM learned_rules WHERE key IN (`+ph+`) ORDER BY category_id, key, negative`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []learning.Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRule implements learning.Repo.
func (d *DB) GetRule(ctx context.Context, category, key string, negative bool) (*learning.Rule, error) {
	row := d.QueryRowContext(ctx, `SELECT `+ruleCols+` FROM learned_rules WHERE category_id=? AND key=? AND negative=?`, category, key, b2i(negative))
	r, err := scanRule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// SaveRule implements learning.Repo (upsert by category,key,negative).
func (d *DB) SaveRule(ctx context.Context, r learning.Rule) error {
	if r.ID == "" {
		r.ID = NewID()
	}
	conds, err := json.Marshal(r.Conditions)
	if err != nil {
		return err
	}
	_, err = d.ExecContext(ctx, `INSERT INTO learned_rules(`+ruleCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(category_id,key,negative) DO UPDATE SET weight=excluded.weight, observations=excluded.observations,
  confirmations=excluded.confirmations, rejections=excluded.rejections, teacher_confirmations=excluded.teacher_confirmations, updated_at=excluded.updated_at, kind=excluded.kind`,
		r.ID, r.CategoryID, r.Key, r.Kind, b2i(r.Negative), string(conds), r.Weight, r.Observations, r.Confirmations, r.Rejections, r.TeacherConfirmations, ts(r.CreatedAt), ts(r.UpdatedAt))
	d.bump()
	return err
}

// ListRules lists learned rules, strongest first, optionally for one category.
func (d *DB) ListRules(ctx context.Context, category string, limit int) ([]learning.Rule, error) {
	q := `SELECT ` + ruleCols + ` FROM learned_rules`
	var args []any
	if category != "" {
		q += ` WHERE category_id=?`
		args = append(args, category)
	}
	q += ` ORDER BY weight DESC, key, category_id LIMIT ?`
	if limit <= 0 {
		limit = 100
	}
	args = append(args, limit)
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []learning.Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
