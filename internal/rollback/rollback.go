// Package rollback implements `lino rollback`: undoing recorded changes by
// writing a new change of type rollback, never by rewriting history.
package rollback

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strconv"
	"strings"

	"github.com/astralyx/lino/internal/anchor"
	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/fileio"
	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/histrec"
	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/reindex"
	"github.com/astralyx/lino/internal/textfile"
	"github.com/astralyx/lino/internal/version"
)

func init() { cli.Register(Command) }

// Command is `lino rollback`.
var Command = &cli.Command{
	Name:    "rollback",
	Usage:   "[<id>] | --to <id> [--path P ...] [--dry-run]",
	Summary: "undo one change (default: the latest by --by, else overall) as a new change",
	Accept:  cli.Mutating,
	MinArgs: 0,
	MaxArgs: 1,
	Setup: func(fs *flag.FlagSet) cli.RunFunc {
		to := fs.String("to", "", "restore files to their state right after change `id`")
		var paths cli.StringList
		fs.Var(&paths, "path", "with --to: only paths matching this glob (repeatable)")
		dry := fs.Bool("dry-run", false, "show what would be undone; write nothing")
		return func(ctx context.Context, c *cli.Call) (output.Result, error) {
			if *to != "" || len(paths) > 0 {
				return runTo(ctx, c, *to, paths, *dry)
			}
			var id int64
			if len(c.Args) > 0 {
				var err error
				if id, err = ParseID(c.Arg(0)); err != nil {
					return output.Result{}, err
				}
			}
			return Undo(ctx, c.Cwd, Request{ID: id, By: c.By, DryRun: *dry})
		}
	},
}

// ParseID parses a change id argument.
func ParseID(s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id < 1 {
		return 0, outcome.New(outcome.Usage, "invalid change id %q", s).WithHint("lino history")
	}
	return id, nil
}

// Request is one rollback of a single change. ID 0 undoes the latest change
// still in effect (see Latest), by By when set.
type Request struct {
	ID     int64
	By     string
	DryRun bool // report what would be undone; write nothing
}

// Data is an applied rollback.
type Data struct {
	ID        int64  `json:"id"` // the new rollback change
	Op        string `json:"op"`
	Path      string `json:"path"`
	Undid     int64  `json:"undid"`
	OldV      string `json:"old_version"`
	NewV      string `json:"version"`
	By        string `json:"by,omitempty"`
	Unchanged bool   `json:"unchanged,omitempty"`
	Restored  bool   `json:"restored,omitempty"` // a removed file was written back
	Removed   bool   `json:"removed,omitempty"`  // a created file was removed
	From      string `json:"from,omitempty"`     // moved back from this path
}

// WriteText prints "1051  rollback  wallet/service.go  undid 1047  v=5f02aa→8c21e0".
func (d Data) WriteText(w io.Writer) error {
	if d.Unchanged {
		_, err := fmt.Fprintf(w, "unchanged %s v=%s: undoing %d changes nothing\n", d.Path, d.NewV, d.Undid)
		return err
	}
	if s, ok := d.fileText(); ok {
		_, err := io.WriteString(w, s)
		return err
	}
	_, err := fmt.Fprintf(w, "%d  %s  %s  undid %d  v=%s→%s\n", d.ID, d.Op, d.Path, d.Undid, d.OldV, d.NewV)
	return err
}

// Hunk is one region of the current file a rollback restores. Start is
// 1-based; Cur are the lines there now, Restore what replaces them.
type Hunk struct {
	Start   int
	Cur     []string
	Restore []string
}

// Plan is what undoing one change would do to the current file.
type Plan struct {
	Change history.Change // the change undone, with fragments
	Path   string         // its current path
	File   *fileio.File   // current content
	Hunks  []Hunk         // ascending, non-overlapping
	// File-level undo of rm, mv or a new file (see Action); no Hunks then.
	Action Action
	To     string        // ActMoveBack: the path the file returns to
	Doc    *textfile.Doc // ActRestore: the content written back
	Mode   fs.FileMode   // ActRestore
}

// Undo reverts change req.ID in the workspace found from cwd and records the
// result as a new rollback change.
func Undo(ctx context.Context, cwd string, req Request) (output.Result, error) {
	ws, err := filecmd.Open(cwd)
	if err != nil {
		return output.Result{}, err
	}
	var res output.Result
	p := ws.Pipeline()
	err = p.Exclusive(func() error {
		if req.ID == 0 {
			st, err := histrec.Store(ctx, ws.Root.Path())
			if err != nil {
				return outcome.Wrap(outcome.Internal, err, "")
			}
			if req.ID, err = Latest(ctx, st, req.By); err != nil || req.ID == 0 {
				res = nothing(req.By)
				return err
			}
		}
		pl, err := Prepare(ctx, ws, req.ID)
		if req.DryRun {
			res, err = dryUndo(ctx, ws, req.ID, pl, err)
			return err
		}
		if err != nil {
			return err
		}
		res, err = pl.Apply(ctx, p, req.By)
		return err
	})
	return res, err
}

// Prepare loads change id and maps it onto the current file. It fails with
// not_found for an unknown or pruned id, refused for a change that cannot be
// rolled back, and conflict when later changes touched the same lines. The
// caller holds the pipeline lock.
func Prepare(ctx context.Context, ws *filecmd.Workspace, id int64) (*Plan, error) {
	root := ws.Root.Path()
	st, err := histrec.Store(ctx, root)
	if err != nil {
		return nil, outcome.Wrap(outcome.Internal, err, "")
	}
	c, err := st.Get(ctx, id)
	if errors.Is(err, history.ErrNotFound) {
		return nil, outcome.New(outcome.NotFound, "no change %d in history (unknown or pruned)", id).WithHint("lino history")
	}
	if err != nil {
		return nil, outcome.Wrap(outcome.Internal, err, "")
	}
	if err := supported(c); err != nil {
		return nil, err
	}
	if fileLevel(c) {
		return prepareFile(ctx, ws, st, c)
	}
	// Record unseen edits of the file first, so they count as later changes.
	if err := refresh(ctx, ws, c.Path); err != nil {
		return nil, err
	}
	hunks, m, err := mapChange(ctx, st, c)
	if err != nil {
		return nil, err
	}
	if m.Path != c.Path {
		if err := refresh(ctx, ws, m.Path); err != nil {
			return nil, err
		}
		if hunks, m, err = mapChange(ctx, st, c); err != nil {
			return nil, err
		}
	}
	if m.Removed {
		return nil, outcome.New(outcome.Conflict, "cannot undo %d: %s was removed by change %s", id, m.Path, ids(m.Overlaps)).
			WithHint("undo the removal first: lino rollback " + ids(m.Overlaps[len(m.Overlaps)-1:]))
	}
	f, err := ws.Load(root, m.Path)
	if err != nil {
		return nil, err
	}
	lines := f.Lines()
	if len(m.Overlaps) > 0 {
		return nil, conflict(f, hunks, "cannot undo %d: later changes touched the same lines: %s", id, ids(m.Overlaps)).
			WithHint("lino show " + strconv.FormatInt(m.Overlaps[0].ID, 10))
	}
	for _, h := range hunks {
		end := h.Start - 1 + len(h.Cur)
		if end > len(lines) || !slices.Equal(lines[h.Start-1:end], h.Cur) {
			return nil, conflict(f, hunks, "cannot undo %d: %s changed on disk in a way history has not recorded", id, m.Path).
				WithHint("retry in a moment, or check lino changes")
		}
	}
	return &Plan{Change: c, Path: m.Path, File: f, Hunks: hunks}, nil
}

// Apply writes the plan as a rollback change through p's hooks (re-index,
// change log, history). The caller holds the pipeline lock.
func (pl *Plan) Apply(ctx context.Context, p *mutate.Pipeline, by string) (output.Result, error) {
	if pl.Action != ActLines {
		return pl.applyFile(ctx, p, by)
	}
	f := pl.File
	doc := &textfile.Doc{Lines: slices.Clone(f.Lines()), Format: f.Format()}
	for i := len(pl.Hunks) - 1; i >= 0; i-- {
		h := pl.Hunks[i]
		doc.Replace(h.Start-1, len(h.Cur), h.Restore)
	}
	after := doc.Bytes()
	target, changed := spans(pl.Hunks)
	res := mutate.Result{
		Path: f.Path, Op: history.OpRollback, By: by,
		OldV: version.Of(f.Data), NewV: version.Of(after),
		Target: target, Changed: changed, Total: len(doc.Lines),
	}
	d := Data{Op: res.Op, Path: res.Path, Undid: pl.Change.ID, OldV: res.OldV, NewV: res.NewV, By: by}
	if string(after) == string(f.Data) {
		d.Unchanged = true
		return output.Result{Outcome: outcome.OK, Data: d}, nil
	}
	if err := fileio.WriteAtomic(f.Real, after, f.Mode); err != nil {
		return output.Result{}, err
	}
	cm := &mutate.Commit{Result: res, Real: f.Real, Before: f.Data, After: after, Doc: doc, Undid: pl.Change.ID}
	err := p.RunHooks(ctx, cm)
	d.ID = cm.Seq
	return output.Result{Outcome: outcome.Updated, Data: d}, err
}

// supported refuses changes this command cannot undo by fragments.
func supported(c history.Change) error {
	switch {
	case c.Extra.Binary:
		return outcome.New(outcome.Refused, "change %d on %s is binary: recorded by version only, cannot be rolled back", c.ID, c.Path)
	case c.Extra.Symlink:
		return outcome.New(outcome.Refused, "change %d removed a symlink: recorded by path only, cannot be rolled back", c.ID)
	}
	return nil
}

// mapChange carries each fragment's new lines from right after c to the
// current file. Overlaps of all fragments are merged, oldest first.
func mapChange(ctx context.Context, st *history.Store, c history.Change) ([]Hunk, history.Mapped, error) {
	var out history.Mapped
	out.Path = c.Path
	seen := map[int64]bool{}
	var hunks []Hunk
	shift := 0
	for _, f := range c.Fragments {
		start := f.Pos + shift + 1
		shift += len(f.New) - len(f.Old)
		m, err := st.MapAfter(ctx, c.Path, c.ID, start, start+len(f.New)-1)
		if err != nil {
			return nil, out, outcome.Wrap(outcome.Internal, err, "")
		}
		out.Path, out.Removed = m.Path, out.Removed || m.Removed
		for _, o := range m.Overlaps {
			if !seen[o.ID] {
				seen[o.ID] = true
				out.Overlaps = append(out.Overlaps, o)
			}
		}
		hunks = append(hunks, Hunk{Start: m.Start, Cur: f.New, Restore: f.Old})
	}
	slices.SortFunc(out.Overlaps, func(a, b history.Change) int { return int(a.ID - b.ID) })
	return hunks, out, nil
}

func refresh(ctx context.Context, ws *filecmd.Workspace, rel string) error {
	root := ws.Root.Path()
	t, release, err := reindex.Open(ctx, root)
	if err != nil {
		return outcome.Wrap(outcome.Internal, err, "")
	}
	if t == nil {
		return nil
	}
	defer release()
	if _, err := t.DB.Refresh(ctx, root, []string{rel}, ws.Config.MaxFileSize); err != nil {
		return outcome.Wrap(outcome.Internal, err, "")
	}
	return nil
}

// conflict builds a conflict error showing the current lines around hunks.
func conflict(f *fileio.File, hunks []Hunk, format string, args ...any) *outcome.Error {
	e := outcome.New(outcome.Conflict, format, args...)
	if len(hunks) == 0 || len(f.Lines()) == 0 {
		return e
	}
	lo, hi := hunks[0].Start, hunks[len(hunks)-1].Start+len(hunks[len(hunks)-1].Cur)-1
	lo = min(max(lo, 1), len(f.Lines()))
	hi = min(max(hi, lo), len(f.Lines()))
	return e.WithLines(f.Path, anchor.Region(f.Lines(), lo, hi))
}

// spans returns the region the hunks cover in the current file and in the
// file after restoring.
func spans(hunks []Hunk) (target, changed mutate.Range) {
	if len(hunks) == 0 {
		return
	}
	first, last := hunks[0], hunks[len(hunks)-1]
	delta := 0
	for _, h := range hunks[:len(hunks)-1] {
		delta += len(h.Restore) - len(h.Cur)
	}
	target = mutate.Range{Start: first.Start, End: last.Start + len(last.Cur) - 1}
	changed = mutate.Range{Start: first.Start, End: last.Start + delta + len(last.Restore) - 1}
	return
}

func ids(cs []history.Change) string {
	s := make([]string, len(cs))
	for i, c := range cs {
		s[i] = strconv.FormatInt(c.ID, 10)
	}
	return strings.Join(s, ", ")
}
