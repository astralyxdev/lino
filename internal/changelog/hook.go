package changelog

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/linediff"
	"github.com/astralyx/lino/internal/live"
	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/paths"
	"github.com/astralyx/lino/internal/reindex"
	"github.com/astralyx/lino/internal/textfile"
	"github.com/astralyx/lino/internal/version"
)

func init() {
	filecmd.Hooks = append(filecmd.Hooks, Hook)
	prev := live.OnExternal
	live.OnExternal = func(ctx context.Context, p *live.Process, ups []index.Update) {
		if prev != nil {
			prev(ctx, p, ups)
		}
		RecordExternal(ctx, p.DB, ups)
	}
	prevIdx := index.External
	index.External = func(ctx context.Context, db *index.DB, ups []index.Update) {
		if prevIdx != nil {
			prevIdx(ctx, db, ups)
		}
		RecordExternal(ctx, db, ups)
	}
}

// Hook appends one entry for a lino mutation, using the index reindex.Open
// provides (the live process's own, or one opened for this call).
func Hook(ctx context.Context, c *mutate.Commit) error {
	root, ok := findRoot(c.Real)
	if !ok {
		return nil
	}
	t, release, err := reindex.Open(ctx, root)
	if err != nil || t == nil {
		return err
	}
	defer release()
	l, err := Open(ctx, t.DB)
	if err != nil {
		return err
	}
	e := FromCommit(root, c)
	e.Time = time.Now()
	tx, err := t.DB.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seq, err := l.AppendTx(ctx, tx, e)
	if err != nil {
		return err
	}
	var undo []func()
	for _, rec := range Recorders {
		u, err := rec(ctx, root, seq, e, c)
		if u != nil {
			undo = append(undo, u)
		}
		if err != nil {
			runUndo(undo)
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		runUndo(undo)
		return err
	}
	c.Seq = seq
	l.Notify()
	return nil
}

// Recorder stores more about a lino mutation under the change id seq, such as
// history. It runs inside the change log transaction: an error aborts the
// entry. undo, when non-nil, reverts what the recorder stored and is called if
// the entry is not committed.
type Recorder func(ctx context.Context, root string, seq int64, e Entry, c *mutate.Commit) (undo func(), err error)

// Recorders run for every lino mutation, in order, before its change log entry
// commits.
var Recorders []Recorder

func runUndo(fs []func()) {
	for i := len(fs) - 1; i >= 0; i-- {
		fs[i]()
	}
}

// FromCommit builds the entry for a lino mutation in root.
func FromCommit(root string, c *mutate.Commit) Entry {
	e := Entry{
		Source: SourceLino, Author: c.By, Op: c.Op, Path: c.Path,
		Kind: Modified, VBefore: c.OldV, VAfter: c.NewV,
	}
	if rel, err := filepath.Rel(root, c.Real); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		e.Path = filepath.ToSlash(rel)
	}
	switch {
	case c.Op == "mv" || c.From != "":
		e.Kind, e.From = Moved, c.From
	case c.Op == "rm" || c.Removed:
		e.Kind, e.VAfter = Removed, ""
	case (c.Op == "write" || c.Op == "rollback") && c.OldV == "" && c.Before == nil:
		e.Kind = Added
		if n := c.Changed.Len(); n > 0 {
			e.Ranges = []Range{{1, n}}
		}
	case c.Op == "write", c.Op == "rollback":
		e.Ranges = DiffRanges(textfile.Parse(c.Before).Lines, textfile.Parse(c.After).Lines)
	case !c.Changed.Empty():
		e.Ranges = []Range{{c.Changed.Start, c.Changed.End}}
	default:
		e.Ranges = []Range{{c.Target.Start, c.Target.Start - 1}}
	}
	return e
}

// DiffRanges returns the ranges of b (1-based) that differ from a.
func DiffRanges(a, b []string) []Range {
	var out []Range
	shift := 0
	for _, f := range linediff.Diff(a, b) {
		start := f.Pos + shift + 1
		out = append(out, Range{start, start + len(f.New) - 1})
		shift += len(f.New) - len(f.Old)
	}
	return out
}

// RecordExternal appends one external entry per index update that changed
// something. Errors are dropped: the index is already updated and the watcher
// has no caller to report to.
func RecordExternal(ctx context.Context, db *index.DB, ups []index.Update) {
	var es []Entry
	var us []index.Update
	for _, u := range ups {
		if e, ok := FromUpdate(u); ok {
			e.Ranges = ExternalRanges(ctx, db, u)
			if e.Time.IsZero() {
				e.Time = time.Now()
			}
			es = append(es, e)
			us = append(us, u)
		}
	}
	if len(es) == 0 {
		return
	}
	l, err := Open(ctx, db)
	if err != nil {
		return
	}
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	var undo []func()
	for i, e := range es {
		seq, err := l.AppendTx(ctx, tx, e)
		if err != nil {
			runUndo(undo)
			return
		}
		for _, rec := range ExternalRecorders {
			if u, err := rec(ctx, db, seq, e, us[i]); err == nil && u != nil {
				undo = append(undo, u)
			}
		}
	}
	if tx.Commit() != nil {
		runUndo(undo)
		return
	}
	l.Notify()
}

// ExternalRecorder stores more about an external change under the change id
// seq, such as history. It runs inside the change log transaction; its error
// only drops what it stores, never the change log entry. undo, when non-nil,
// is called if the entry is not committed.
type ExternalRecorder func(ctx context.Context, db *index.DB, seq int64, e Entry, u index.Update) (undo func(), err error)

// ExternalRecorders run for every external change, in order.
var ExternalRecorders []ExternalRecorder

// ExternalRanges returns the changed line ranges of an external update: the
// diff of u.Old against the current indexed content for a text modification,
// the whole file for an added text file. It returns nil when unknown (binary,
// removed, moved, or the row changed again since u).
func ExternalRanges(ctx context.Context, db *index.DB, u index.Update) []Range {
	if u.Binary || (u.Op != index.Modified && u.Op != index.Added) || (u.Op == index.Modified && u.OldBinary) {
		return nil
	}
	fi, ok, err := db.File(ctx, u.Path)
	if err != nil || !ok || fi.Hash != u.Hash {
		return nil
	}
	cur := textfile.Parse([]byte(fi.Content)).Lines
	if u.Op == index.Added {
		if len(cur) == 0 {
			return nil
		}
		return []Range{{1, len(cur)}}
	}
	return DiffRanges(textfile.Parse([]byte(u.Old)).Lines, cur)
}

// FromUpdate builds the external entry for an index update, without line
// ranges (see ExternalRanges); ok is false for Unchanged.
func FromUpdate(u index.Update) (Entry, bool) {
	e := Entry{Source: SourceExternal, Op: "external", Path: u.Path, VBefore: short(u.OldHash), VAfter: short(u.Hash)}
	switch u.Op {
	case index.Added:
		e.Kind = Added
	case index.Modified:
		e.Kind = Modified
	case index.Removed:
		e.Kind = Removed
	case index.Moved:
		e.Kind, e.From = Moved, u.From
	default:
		return Entry{}, false
	}
	return e, true
}

func short(hash string) string {
	if len(hash) > version.Len {
		return hash[:version.Len]
	}
	return hash
}

func findRoot(p string) (string, bool) {
	for dir := filepath.Dir(p); ; {
		if paths.IsRoot(dir) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
