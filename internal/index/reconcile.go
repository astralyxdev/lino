package index

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/astralyx/lino/internal/ignore"
	"github.com/astralyx/lino/internal/textfile"
)

// Summary is the result of a full reconcile.
type Summary struct {
	Files     int // files present after the reconcile
	Added     int
	Modified  int
	Removed   int
	Moved     int
	Unchanged int
	Changes   []Update // every non-Unchanged update, sorted by path
	Duration  time.Duration
}

const (
	reconcileBatch = 256 // file writes per transaction
	scanChunk      = 512 // files read into memory at once
)

type indexed struct {
	hash  string
	size  int64
	mtime int64
	mode  int64
}

type scanned struct {
	ent    ignore.Entry
	hash   string
	data   []byte
	binary bool
	stat   Stat
	err    error
}

// Reconcile brings the index in line with the files under rules.Root(). Files
// whose size, mtime and mode match the index are skipped; others are hashed in
// parallel. A new path whose hash equals that of a vanished path is recorded as
// a move without re-tokenising. Files over maxSize (> 0) are indexed by hash only.
func (d *DB) Reconcile(ctx context.Context, rules *ignore.Rules, maxSize int64) (Summary, error) {
	start := time.Now()
	var sum Summary

	known, err := d.loadIndexed(ctx)
	if err != nil {
		return sum, err
	}

	var todo []ignore.Entry
	seen := make(map[string]bool, len(known))
	err = ignore.Walk(rules.Root(), rules, func(e ignore.Entry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		seen[e.Rel] = true
		if k, ok := known[e.Rel]; ok && k.size == e.Size && k.mtime == e.ModTime.UnixNano() && k.mode == int64(e.Mode.Perm()) {
			sum.Unchanged++
			return nil
		}
		todo = append(todo, e)
		return nil
	})
	if err != nil {
		return sum, fmt.Errorf("walk: %w", err)
	}

	gone := map[string][]string{} // hash -> vanished paths
	var goneList []string
	for p, k := range known {
		if !seen[p] {
			gone[k.hash] = append(gone[k.hash], p)
			goneList = append(goneList, p)
		}
	}
	sort.Strings(goneList)
	for _, ps := range gone {
		sort.Strings(ps)
	}

	moved := map[string]bool{}
	failed := 0
	w := &batchWriter{ctx: ctx, db: d.SQL}
	for lo := 0; lo < len(todo); lo += scanChunk {
		results := scanAll(ctx, todo[lo:min(lo+scanChunk, len(todo))], maxSize)
		for _, r := range results {
			if r.err != nil {
				if errors.Is(r.err, fs.ErrNotExist) {
					failed++
					continue
				}
				w.abort()
				return sum, fmt.Errorf("read %s: %w", r.ent.Rel, r.err)
			}
			var u Update
			_, wasKnown := known[r.ent.Rel]
			if ps := gone[r.hash]; !wasKnown && len(ps) > 0 {
				from := ps[0]
				gone[r.hash] = ps[1:]
				moved[from] = true
				err = w.do(func(tx *sql.Tx) error {
					u, err = moveTx(ctx, tx, from, r.ent.Rel, r.stat)
					return err
				})
			} else {
				err = w.do(func(tx *sql.Tx) error {
					u, err = upsertTx(ctx, tx, r.ent.Rel, r.hash, r.stat, r.data, r.binary)
					return err
				})
			}
			if err != nil {
				w.abort()
				return sum, fmt.Errorf("index %s: %w", r.ent.Rel, err)
			}
			sum.add(u)
		}
	}
	for _, p := range goneList {
		if moved[p] {
			continue
		}
		var u Update
		err = w.do(func(tx *sql.Tx) error {
			u, err = removeTx(ctx, tx, p)
			return err
		})
		if err != nil {
			w.abort()
			return sum, fmt.Errorf("unindex %s: %w", p, err)
		}
		if u.Op == Removed {
			_, err := os.Lstat(filepath.Join(rules.Root(), filepath.FromSlash(p)))
			u.Ignored = err == nil
		}
		sum.add(u)
	}
	if err := w.commit(); err != nil {
		return sum, err
	}
	sort.Slice(sum.Changes, func(i, j int) bool { return sum.Changes[i].Path < sum.Changes[j].Path })
	sum.Files = len(seen) - failed
	sum.Duration = time.Since(start)
	if err := d.recordReconcile(ctx, sum, start); err != nil {
		return sum, err
	}
	return sum, nil
}

func (s *Summary) add(u Update) {
	switch u.Op {
	case Unchanged:
		s.Unchanged++
		return
	case Added:
		s.Added++
	case Modified:
		s.Modified++
	case Removed:
		s.Removed++
	case Moved:
		s.Moved++
	}
	s.Changes = append(s.Changes, u)
}

func (d *DB) loadIndexed(ctx context.Context) (map[string]indexed, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT path, hash, size, mtime, mode FROM files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]indexed{}
	for rows.Next() {
		var p string
		var k indexed
		if err := rows.Scan(&p, &k.hash, &k.size, &k.mtime, &k.mode); err != nil {
			return nil, err
		}
		m[p] = k
	}
	return m, rows.Err()
}

// scanAll reads and hashes entries in parallel, returning results in input order.
func scanAll(ctx context.Context, ents []ignore.Entry, maxSize int64) []scanned {
	out := make([]scanned, len(ents))
	next := make(chan int)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for i := range next {
				out[i] = scan(ents[i], maxSize)
			}
		})
	}
	for i := range ents {
		if ctx.Err() != nil {
			break
		}
		next <- i
	}
	close(next)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		for i := range out {
			if out[i].hash == "" && out[i].err == nil {
				out[i] = scanned{ent: ents[i], err: err}
			}
		}
	}
	return out
}

func scan(e ignore.Entry, maxSize int64) scanned {
	r := scanned{ent: e}
	f, err := os.Open(e.Abs)
	if err != nil {
		r.err = err
		return r
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		r.err = err
		return r
	}
	if !st.Mode().IsRegular() {
		r.err = fs.ErrNotExist
		return r
	}
	r.stat = Stat{Size: st.Size(), ModTime: st.ModTime(), Mode: st.Mode().Perm()}
	if maxSize > 0 && st.Size() > maxSize {
		h := sha256.New()
		n, err := io.Copy(h, f)
		if err != nil {
			r.err = err
			return r
		}
		r.stat.Size, r.hash, r.binary = n, hex.EncodeToString(h.Sum(nil)), true
		return r
	}
	data, err := io.ReadAll(f)
	if err != nil {
		r.err = err
		return r
	}
	r.stat.Size = int64(len(data))
	r.hash = Hash(data)
	r.binary = textfile.Classify(data) == textfile.Binary
	if !r.binary {
		r.data = data
	}
	return r
}

// moveTx renames the row for from to to, keeping content and FTS rows.
func moveTx(ctx context.Context, tx *sql.Tx, from, to string, s Stat) (Update, error) {
	u := Update{Path: to, From: from, Op: Moved}
	var bin int
	err := tx.QueryRowContext(ctx,
		`UPDATE files SET path = ?, size = ?, mtime = ?, mode = ? WHERE path = ? RETURNING hash, binary`,
		to, s.Size, s.ModTime.UnixNano(), int64(s.Mode), from).Scan(&u.Hash, &bin)
	u.OldHash, u.Binary = u.Hash, bin != 0
	return u, err
}

// batchWriter groups writes into transactions of reconcileBatch statements.
type batchWriter struct {
	ctx context.Context
	db  *sql.DB
	tx  *sql.Tx
	n   int
}

func (w *batchWriter) do(fn func(*sql.Tx) error) error {
	if w.tx == nil {
		tx, err := w.db.BeginTx(w.ctx, nil)
		if err != nil {
			return err
		}
		w.tx = tx
	}
	if err := fn(w.tx); err != nil {
		return err
	}
	w.n++
	if w.n >= reconcileBatch {
		return w.commit()
	}
	return nil
}

func (w *batchWriter) commit() error {
	if w.tx == nil {
		return nil
	}
	err := w.tx.Commit()
	w.tx, w.n = nil, 0
	return err
}

func (w *batchWriter) abort() {
	if w.tx != nil {
		w.tx.Rollback()
		w.tx, w.n = nil, 0
	}
}
