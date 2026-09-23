package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Meta returns the value stored under key in the meta table.
func (d *DB) Meta(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := d.SQL.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

// SetMeta stores value under key in the meta table.
func (d *DB) SetMeta(ctx context.Context, key, value string) error {
	_, err := d.SQL.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

const lastReconcileKey = "last_reconcile"

// ReconcileInfo is the record of the last full reconcile.
type ReconcileInfo struct {
	At         time.Time `json:"at"`
	DurationMS int64     `json:"duration_ms"`
	Files      int       `json:"files"`
	Added      int       `json:"added"`
	Modified   int       `json:"modified"`
	Removed    int       `json:"removed"`
	Moved      int       `json:"moved"`
}

// LastReconcile returns the record written by the last Reconcile.
func (d *DB) LastReconcile(ctx context.Context) (ReconcileInfo, bool, error) {
	s, ok, err := d.Meta(ctx, lastReconcileKey)
	if err != nil || !ok {
		return ReconcileInfo{}, false, err
	}
	var ri ReconcileInfo
	if err := json.Unmarshal([]byte(s), &ri); err != nil {
		return ReconcileInfo{}, false, nil
	}
	return ri, true, nil
}

func (d *DB) recordReconcile(ctx context.Context, s Summary, at time.Time) error {
	b, err := json.Marshal(ReconcileInfo{
		At: at, DurationMS: s.Duration.Milliseconds(), Files: s.Files,
		Added: s.Added, Modified: s.Modified, Removed: s.Removed, Moved: s.Moved,
	})
	if err != nil {
		return err
	}
	return d.SetMeta(ctx, lastReconcileKey, string(b))
}

// Totals are counts over the whole index.
type Totals struct {
	Files  int `json:"files"`
	Lines  int `json:"lines"`
	Binary int `json:"binary"`
}

// Totals counts indexed files, text lines and binary files.
func (d *DB) Totals(ctx context.Context) (Totals, error) {
	var t Totals
	err := d.SQL.QueryRowContext(ctx,
		`SELECT count(*), coalesce(sum(lines), 0), coalesce(sum(binary), 0) FROM files`).Scan(&t.Files, &t.Lines, &t.Binary)
	return t, err
}
