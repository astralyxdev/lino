package history

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// Retention limits for Prune. Zero fields do not limit.
//
// Pruning deletes a prefix of change ids: every change up to a cutoff, never
// one in the middle. Reconstruction walks back from the current content, so
// the chain from any retained change to the present stays intact.
//
// VACUUM strategy: history.db keeps SQLite's default auto_vacuum=NONE. Pages
// freed by a prune go to the freelist and are reused by later changes, so in
// steady state the file stays near the size limit without rewriting it. The
// size limit counts used pages only. A full VACUUM (which rewrites the file,
// needs up to twice its size free and blocks writers) runs only from the live
// process's periodic prune, and only when free pages are both more than half
// the file and more than VacuumMinFree bytes, e.g. after the limits were
// lowered. After deleting anything, the WAL is checkpointed and truncated.
type Retention struct {
	MaxAge  time.Duration
	MaxSize int64 // bytes of used database pages
	Now     time.Time
}

// VacuumMinFree is the free space below which Vacuum never rewrites the file.
var VacuumMinFree int64 = 64 << 20

// PruneResult reports what Prune removed.
type PruneResult struct {
	Removed  int   // changes deleted
	UpTo     int64 // highest deleted id, 0 if none
	Used     int64 // used bytes after pruning
	Free     int64 // freelist bytes after pruning
	Vacuumed bool
}

// perChange approximates the row and index overhead of a change beyond its
// variable-length fields, for estimating what a cutoff frees.
const perChange = 96

// Prune deletes the oldest changes beyond the retention limits, strictly from
// the oldest end.
func (s *Store) Prune(ctx context.Context, r Retention) (PruneResult, error) {
	var res PruneResult
	if r.Now.IsZero() {
		r.Now = time.Now()
	}
	if r.MaxAge > 0 {
		var id sql.NullInt64
		err := s.SQL.QueryRowContext(ctx, `SELECT max(id) FROM changes WHERE time < ?`,
			r.Now.Add(-r.MaxAge).UnixNano()).Scan(&id)
		if err != nil {
			return res, err
		}
		if id.Valid {
			if err := s.deleteUpTo(ctx, id.Int64, &res); err != nil {
				return res, err
			}
		}
	}
	for range 8 {
		used, _, err := s.pages(ctx)
		if err != nil {
			return res, err
		}
		if r.MaxSize <= 0 || used <= r.MaxSize {
			break
		}
		cut, err := s.sizeCutoff(ctx, used-r.MaxSize, used)
		if err != nil {
			return res, err
		}
		if cut == 0 {
			break
		}
		if err := s.deleteUpTo(ctx, cut, &res); err != nil {
			return res, err
		}
	}
	if res.Removed > 0 {
		s.SQL.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	}
	var err error
	res.Used, res.Free, err = s.pages(ctx)
	return res, err
}

// Vacuum rewrites the database when free pages exceed half of it and
// VacuumMinFree. It reports whether it did.
func (s *Store) Vacuum(ctx context.Context) (bool, error) {
	used, free, err := s.pages(ctx)
	if err != nil || free < VacuumMinFree || free <= used {
		return false, err
	}
	if _, err := s.SQL.ExecContext(ctx, `VACUUM`); err != nil {
		return false, err
	}
	return true, nil
}

// MaybePrune runs Prune when the last prune recorded in the store is at least
// every old, so frequent callers (direct mode) pay for it rarely.
func (s *Store) MaybePrune(ctx context.Context, r Retention, every time.Duration) (PruneResult, bool, error) {
	if r.Now.IsZero() {
		r.Now = time.Now()
	}
	var last string
	err := s.SQL.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'last_prune'`).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return PruneResult{}, false, err
	}
	if n, err := strconv.ParseInt(last, 10, 64); err == nil && r.Now.Sub(time.Unix(0, n)) < every {
		return PruneResult{}, false, nil
	}
	res, err := s.Prune(ctx, r)
	if err != nil {
		return res, true, err
	}
	_, err = s.SQL.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES ('last_prune', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		strconv.FormatInt(r.Now.UnixNano(), 10))
	return res, true, err
}

func (s *Store) deleteUpTo(ctx context.Context, id int64, res *PruneResult) error {
	tx, err := s.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM fragments WHERE change_id <= ?`, id); err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, `DELETE FROM changes WHERE id <= ?`, id)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	res.Removed += int(n)
	res.UpTo = max(res.UpTo, id)
	return nil
}

// sizeCutoff returns the id up to which the oldest changes must go to free
// about excess of used bytes, scaling logical row sizes to used pages.
func (s *Store) sizeCutoff(ctx context.Context, excess, used int64) (int64, error) {
	rows, err := s.SQL.QueryContext(ctx, `
		SELECT c.id, `+strconv.Itoa(perChange)+` + length(c.path) + length(c.author) + length(c.extra)
			+ length(c.v_before) + length(c.v_after)
			+ coalesce((SELECT sum(24 + coalesce(length(f.old_lines), 0) + coalesce(length(f.new_lines), 0))
				FROM fragments f WHERE f.change_id = c.id), 0)
		FROM changes c ORDER BY c.id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type row struct{ id, size int64 }
	var all []row
	var total int64
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.size); err != nil {
			return 0, err
		}
		all = append(all, r)
		total += r.size
	}
	if err := rows.Err(); err != nil || len(all) == 0 {
		return 0, err
	}
	// Logical sizes undercount pages (indexes, fill): convert page bytes to
	// logical bytes.
	target := excess
	if total < used {
		target = int64(float64(excess) * float64(total) / float64(used))
	}
	var acc int64
	for _, r := range all {
		acc += r.size
		if acc >= target {
			return r.id, nil
		}
	}
	return all[len(all)-1].id, nil
}

// pages returns used and free bytes of the database.
func (s *Store) pages(ctx context.Context) (used, free int64, err error) {
	var count, freelist, size int64
	if err = s.SQL.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&count); err != nil {
		return
	}
	if err = s.SQL.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freelist); err != nil {
		return
	}
	if err = s.SQL.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&size); err != nil {
		return
	}
	return (count - freelist) * size, freelist * size, nil
}
