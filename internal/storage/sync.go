package storage

import (
	"context"
)

// SyncItem is an email whose final category differs from what was last pushed
// to the external mailbox (e.g. the Gmail label).
type SyncItem struct {
	EmailID  string
	Category string // final category now
	Previous string // category last pushed ("" if never)
	Decision string // resolved_by of the session: auto|answers|predicted|ancestor|user|teacher
}

// SyncPending lists resolved, non-quiet emails whose category was not yet
// pushed or has changed since (a correction, a Telegram answer, a retry).
func (d *DB) SyncPending(ctx context.Context, limit int) ([]SyncItem, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.QueryContext(ctx, `SELECT email_id, final_category, COALESCE(synced_category,''), COALESCE(resolved_by,'')
FROM sessions
WHERE status='resolved' AND quiet=0 AND final_category IS NOT NULL AND final_category!='unknown'
  AND COALESCE(synced_category,'') != final_category
ORDER BY updated_at, email_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SyncItem
	for rows.Next() {
		var it SyncItem
		if err := rows.Scan(&it.EmailID, &it.Category, &it.Previous, &it.Decision); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// AckSync records that category was pushed to the external mailbox. If the
// category changed meanwhile, the email simply stays pending.
func (d *DB) AckSync(ctx context.Context, emailID, category string) error {
	res, err := d.ExecContext(ctx, `UPDATE sessions SET synced_category=? WHERE email_id=?`, category, emailID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UnlabeledEmailIDs lists emails with text but no trusted label, oldest first:
// the work queue of `pretrain -from-db`.
func (d *DB) UnlabeledEmailIDs(ctx context.Context, limit int) ([]string, error) {
	labels, err := d.Labels(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := d.QueryContext(ctx, `SELECT id FROM emails WHERE body_text IS NOT NULL AND body_text!='' ORDER BY created_at, id`)
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
		if _, ok := labels[id]; !ok {
			out = append(out, id)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, rows.Err()
}
