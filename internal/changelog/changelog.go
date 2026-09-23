// Package changelog records every file change, from lino or from outside, with
// an increasing sequence number (its change id). The log lives in index.db;
// callers hold their own position and read what came after it.
package changelog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/index"
)

// Sources of a change.
const (
	SourceLino     = "lino"
	SourceExternal = "external"
)

// Kind is what a change did to its path.
type Kind string

const (
	Added    Kind = "added"
	Modified Kind = "modified"
	Removed  Kind = "removed"
	Moved    Kind = "moved"
)

// Range is a 1-based inclusive line range in the version after the change.
// An empty range (End = Start-1) marks lines removed before line Start.
type Range struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Entry is one change log row.
type Entry struct {
	Seq     int64     `json:"seq"`
	Time    time.Time `json:"time"`
	Source  string    `json:"source"`
	Author  string    `json:"author,omitempty"`
	Op      string    `json:"op,omitempty"` // lino operation (edit, write, ...) or "external"
	Path    string    `json:"path"`
	From    string    `json:"from,omitempty"` // previous path when Kind is Moved
	Kind    Kind      `json:"kind"`
	VBefore string    `json:"v_before,omitempty"`
	VAfter  string    `json:"v_after,omitempty"`
	Ranges  []Range   `json:"ranges,omitempty"`
}

var schema = []string{
	`CREATE TABLE IF NOT EXISTS changelog(
		seq      INTEGER PRIMARY KEY,
		time     INTEGER NOT NULL,
		source   TEXT NOT NULL,
		author   TEXT NOT NULL DEFAULT '',
		op       TEXT NOT NULL DEFAULT '',
		path     TEXT NOT NULL,
		from_path TEXT NOT NULL DEFAULT '',
		kind     TEXT NOT NULL,
		v_before TEXT NOT NULL DEFAULT '',
		v_after  TEXT NOT NULL DEFAULT '',
		ranges   TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE IF NOT EXISTS changelog_seq(k INTEGER PRIMARY KEY CHECK (k = 0), last INTEGER NOT NULL)`,
}

// Log is the change log of one index.
type Log struct {
	db *sql.DB
	n  *notifier
}

type notifier struct {
	mu sync.Mutex
	ch chan struct{}
}

var notifiers sync.Map // index path -> *notifier

// Open returns the change log stored in db, creating its tables on first use.
// Logs opened on the same index path in this process share one notifier.
//
// The sequence continues after the highest id in history.db when the log is
// new, so ids stay increasing even after index.db was rebuilt.
func Open(ctx context.Context, db *index.DB) (*Log, error) {
	for _, s := range schema {
		if _, err := db.SQL.ExecContext(ctx, s); err != nil {
			return nil, fmt.Errorf("changelog schema: %w", err)
		}
	}
	var n int
	if err := db.SQL.QueryRowContext(ctx, `SELECT count(*) FROM changelog_seq`).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		floor, err := historyLatest(ctx, filepath.Dir(filepath.Dir(db.Path)))
		if err != nil {
			return nil, err
		}
		if _, err := db.SQL.ExecContext(ctx,
			`INSERT OR IGNORE INTO changelog_seq(k, last) SELECT 0, max(?, ifnull(max(seq), 0)) FROM changelog`, floor); err != nil {
			return nil, err
		}
	}
	nt, _ := notifiers.LoadOrStore(db.Path, &notifier{ch: make(chan struct{})})
	return &Log{db: db.SQL, n: nt.(*notifier)}, nil
}

func historyLatest(ctx context.Context, root string) (int64, error) {
	if _, err := os.Stat(history.Path(root)); errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	h, err := history.Open(ctx, root)
	if err != nil {
		return 0, err
	}
	defer h.Close()
	return h.Latest(ctx)
}

// Append records e with the next sequence number and wakes waiters. A zero
// e.Time takes now. It returns the sequence number.
func (l *Log) Append(ctx context.Context, e Entry) (int64, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	seq, err := l.AppendTx(ctx, tx, e)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	l.Notify()
	return seq, nil
}

// AppendTx records e inside tx, which must belong to this log's index. The
// caller commits and then calls Notify.
func (l *Log) AppendTx(ctx context.Context, tx *sql.Tx, e Entry) (int64, error) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`UPDATE changelog_seq SET last = last + 1 WHERE k = 0 RETURNING last`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("changelog: next seq: %w", err)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO changelog(seq, time, source, author, op, path, from_path, kind, v_before, v_after, ranges)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		seq, e.Time.UnixNano(), e.Source, e.Author, e.Op, e.Path, e.From, string(e.Kind), e.VBefore, e.VAfter, encodeRanges(e.Ranges))
	if err != nil {
		return 0, fmt.Errorf("changelog: append: %w", err)
	}
	return seq, nil
}

// Notify wakes everyone waiting on Changed.
func (l *Log) Notify() {
	l.n.mu.Lock()
	close(l.n.ch)
	l.n.ch = make(chan struct{})
	l.n.mu.Unlock()
}

// Changed returns a channel closed at the next append in this process. Take
// it before reading with Since so no append is missed.
func (l *Log) Changed() <-chan struct{} {
	l.n.mu.Lock()
	defer l.n.mu.Unlock()
	return l.n.ch
}

// Since returns entries with seq > after, oldest first, at most limit
// (<= 0 = no limit).
func (l *Log) Since(ctx context.Context, after int64, limit int) ([]Entry, error) {
	q := `SELECT seq, time, source, author, op, path, from_path, kind, v_before, v_after, ranges
		FROM changelog WHERE seq > ? ORDER BY seq`
	if limit > 0 {
		q += " LIMIT " + strconv.Itoa(limit)
	}
	rows, err := l.db.QueryContext(ctx, q, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var t int64
		var kind, ranges string
		if err := rows.Scan(&e.Seq, &t, &e.Source, &e.Author, &e.Op, &e.Path, &e.From, &kind, &e.VBefore, &e.VAfter, &ranges); err != nil {
			return nil, err
		}
		e.Time = time.Unix(0, t)
		e.Kind = Kind(kind)
		if e.Ranges, err = decodeRanges(ranges); err != nil {
			return nil, fmt.Errorf("changelog %d: %w", e.Seq, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Latest returns the last sequence number handed out, 0 if none.
func (l *Log) Latest(ctx context.Context) (int64, error) {
	var seq int64
	err := l.db.QueryRowContext(ctx, `SELECT last FROM changelog_seq WHERE k = 0`).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return seq, err
}

func encodeRanges(rs []Range) string {
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = strconv.Itoa(r.Start) + "-" + strconv.Itoa(r.End)
	}
	return strings.Join(parts, ",")
}

func decodeRanges(s string) ([]Range, error) {
	if s == "" {
		return nil, nil
	}
	var out []Range
	for _, p := range strings.Split(s, ",") {
		a, b, ok := strings.Cut(p, "-")
		start, err1 := strconv.Atoi(a)
		end, err2 := strconv.Atoi(b)
		if !ok || err1 != nil || err2 != nil {
			return nil, fmt.Errorf("bad range %q", p)
		}
		out = append(out, Range{Start: start, End: end})
	}
	return out, nil
}
