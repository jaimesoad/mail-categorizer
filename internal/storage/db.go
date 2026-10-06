// Package storage is the SQLite persistence layer (§5): emails, features,
// categories, classifications, questions, sessions, interactions and learned
// rules. It uses a pure-Go driver (no CGO) and versioned migrations.
package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps *sql.DB. All access goes through a single connection: SQLite
// serializes writers anyway, and it keeps ":memory:" databases coherent.
// Never hold a *sql.Rows open while issuing another query.
type DB struct {
	*sql.DB
	path string

	mu       sync.Mutex
	version  uint64 // bumped on every knowledge-changing write; invalidates caches
	labelVer uint64 // bumped only when the set of trusted labels (or email text) changes
	cache    map[string]any
}

// Open opens (creating if needed) the database at path and migrates it.
// Use ":memory:" for an ephemeral database.
func Open(path string) (*DB, error) {
	dsn := path
	if path == ":memory:" {
		dsn = "file::memory:"
	} else if !strings.HasPrefix(path, "file:") {
		dsn = "file:" + path
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	dsn += sep + "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	if path != ":memory:" {
		dsn += "&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	}
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sdb.SetMaxOpenConns(1)
	d := &DB{DB: sdb, path: path, cache: map[string]any{}}
	if err := d.migrate(context.Background()); err != nil {
		sdb.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) migrate(ctx context.Context) error {
	if _, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return err
	}
	cur := 0
	var v string
	err := d.QueryRowContext(ctx, `SELECT value FROM schema_meta WHERE key='version'`).Scan(&v)
	switch {
	case err == sql.ErrNoRows:
	case err != nil:
		return err
	default:
		cur, _ = strconv.Atoi(v)
	}
	if cur > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this binary (%d)", cur, len(migrations))
	}
	for i := cur; i < len(migrations); i++ {
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_meta(key,value) VALUES('version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.Itoa(i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Tx runs fn in a transaction.
func (d *DB) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Version is a counter that changes whenever knowledge-relevant data changes.
func (d *DB) Version() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.version
}

// LabelVersion changes only when trusted labels change: it keys the similarity index cache.
func (d *DB) LabelVersion() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.labelVer
}

func (d *DB) bumpLabels() {
	d.mu.Lock()
	d.labelVer++
	d.mu.Unlock()
}

func (d *DB) bump() {
	d.mu.Lock()
	d.version++
	d.cache = map[string]any{}
	d.mu.Unlock()
}

func (d *DB) cached(key string) (any, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, ok := d.cache[key]
	return v, ok
}

func (d *DB) setCached(key string, v any, atVersion uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.version == atVersion {
		d.cache[key] = v
	}
}

// Backup writes a consistent copy of the database to dst (VACUUM INTO).
func (d *DB) Backup(ctx context.Context, dst string) error {
	if strings.ContainsAny(dst, "\x00") {
		return fmt.Errorf("invalid path")
	}
	_, err := d.ExecContext(ctx, `VACUUM INTO ?`, dst)
	return err
}

// NewID returns a random 128-bit hex id.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

const tsLayout = time.RFC3339Nano

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

func parseTS(s sql.NullString) time.Time {
	if !s.Valid || s.String == "" {
		return time.Time{}
	}
	t, err := time.Parse(tsLayout, s.String)
	if err != nil {
		return time.Time{}
	}
	return t
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
