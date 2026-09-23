package rollback

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/astralyx/lino/internal/anchor"
	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/fileio"
	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/textfile"
	"github.com/astralyx/lino/internal/version"
)

// Action is what a plan does to the file system.
type Action int

const (
	ActLines    Action = iota // restore line fragments (Hunks)
	ActRestore                // write back a removed file (Path, Data, Mode)
	ActRemove                 // remove a file the change created (Path, File)
	ActMoveBack               // move Path back to To
)

// fileLevel reports whether c is undone as a whole file rather than by lines.
func fileLevel(c history.Change) bool {
	return c.Extra.Removed || c.Extra.Created || c.Extra.From != ""
}

// prepareFile plans undoing a removal, a move or the creation of a file.
func prepareFile(ctx context.Context, ws *filecmd.Workspace, st *history.Store, c history.Change) (*Plan, error) {
	switch {
	case c.Extra.Removed:
		return prepareRestore(ctx, ws, st, c)
	case c.Extra.From != "":
		return prepareMoveBack(ctx, ws, st, c)
	default:
		return prepareRemove(ctx, ws, st, c)
	}
}

// prepareRestore plans writing back the file c removed; it conflicts if the
// path exists again.
func prepareRestore(ctx context.Context, ws *filecmd.Workspace, st *history.Store, c history.Change) (*Plan, error) {
	if err := refresh(ctx, ws, c.Path); err != nil {
		return nil, err
	}
	if err := vacant(ctx, ws, st, c, c.Path); err != nil {
		return nil, err
	}
	doc := &textfile.Doc{Format: textfile.Format{BOM: c.Extra.BOM, FinalNewline: !c.Extra.NoFinalNewline}}
	if c.Extra.CRLF {
		doc.Format.EOL = textfile.CRLF
	}
	for _, f := range c.Fragments {
		doc.Lines = append(doc.Lines, f.Old...)
	}
	mode := fs.FileMode(c.Extra.Mode).Perm()
	if mode == 0 {
		mode = fileio.DefaultMode
	}
	return &Plan{Change: c, Path: c.Path, Action: ActRestore, Doc: doc, Mode: mode}, nil
}

// prepareRemove plans removing the file c created, wherever later moves took
// it; it conflicts if the file changed since.
func prepareRemove(ctx context.Context, ws *filecmd.Workspace, st *history.Store, c history.Change) (*Plan, error) {
	if err := refresh(ctx, ws, c.Path); err != nil {
		return nil, err
	}
	m, err := st.MapAfter(ctx, c.Path, c.ID, 1, 0)
	if err != nil {
		return nil, outcome.Wrap(outcome.Internal, err, "")
	}
	if m.Path != c.Path {
		if err := refresh(ctx, ws, m.Path); err != nil {
			return nil, err
		}
		if m, err = st.MapAfter(ctx, c.Path, c.ID, 1, 0); err != nil {
			return nil, outcome.Wrap(outcome.Internal, err, "")
		}
	}
	if m.Removed {
		return nil, removedConflict(c, m)
	}
	f, err := ws.Load(ws.Root.Path(), m.Path)
	if err != nil {
		return nil, err
	}
	if version.Of(f.Data) != c.VAfter {
		return nil, changedConflict(ctx, st, c, m.Path, f, "cannot undo %d: %s was modified since it was created", c.ID, m.Path)
	}
	return &Plan{Change: c, Path: m.Path, Action: ActRemove, File: f}, nil
}

// prepareMoveBack plans moving the file c moved back to its source; it
// conflicts if the source is occupied or the file changed or moved since.
func prepareMoveBack(ctx context.Context, ws *filecmd.Workspace, st *history.Store, c history.Change) (*Plan, error) {
	for _, p := range []string{c.Path, c.Extra.From} {
		if err := refresh(ctx, ws, p); err != nil {
			return nil, err
		}
	}
	m, err := st.MapAfter(ctx, c.Path, c.ID, 1, 0)
	if err != nil {
		return nil, outcome.Wrap(outcome.Internal, err, "")
	}
	if m.Removed {
		return nil, removedConflict(c, m)
	}
	if m.Path != c.Path {
		return nil, laterConflict(ctx, st, c, "cannot undo %d: %s was moved again to %s", c.ID, c.Path, m.Path)
	}
	_, real, err := ws.Root.Jail(ws.Root.Path(), c.Path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(real)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, laterConflict(ctx, st, c, "cannot undo %d: %s no longer exists", c.ID, c.Path)
	}
	if err != nil {
		return nil, outcome.Wrap(outcome.Internal, err, c.Path+": "+err.Error())
	}
	if c.VAfter != "" && version.Of(data) != c.VAfter {
		return nil, laterConflict(ctx, st, c, "cannot undo %d: %s was modified since the move", c.ID, c.Path)
	}
	if err := vacant(ctx, ws, st, c, c.Extra.From); err != nil {
		return nil, err
	}
	return &Plan{Change: c, Path: c.Path, Action: ActMoveBack, To: c.Extra.From}, nil
}

// applyFile carries out a file-level plan. The caller holds the pipeline lock.
func (pl *Plan) applyFile(ctx context.Context, p *mutate.Pipeline, by string) (output.Result, error) {
	d := Data{Op: history.OpRollback, Path: pl.Path, Undid: pl.Change.ID, By: by}
	switch pl.Action {
	case ActRestore:
		_, real, err := p.Root.Jail(p.Root.Path(), pl.Path)
		if err != nil {
			return output.Result{}, err
		}
		after := pl.Doc.Bytes()
		if err := fileio.WriteAtomic(real, after, pl.Mode); err != nil {
			return output.Result{}, err
		}
		n := len(pl.Doc.Lines)
		d.NewV, d.Restored = version.Of(after), true
		cm := &mutate.Commit{
			Result: mutate.Result{Path: pl.Path, Op: d.Op, By: by, NewV: d.NewV, Changed: mutate.Range{Start: 1, End: n}, Total: n},
			Real:   real, After: after, Doc: pl.Doc, Undid: pl.Change.ID,
		}
		err = p.RunHooks(ctx, cm)
		d.ID = cm.Seq
		return output.Result{Outcome: outcome.Created, Data: d}, err
	case ActRemove:
		f := pl.File
		if err := os.Remove(f.Real); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return output.Result{}, outcome.Wrap(outcome.Internal, err, f.Path+": "+err.Error())
		}
		syncDir(filepath.Dir(f.Real))
		d.OldV, d.Removed = version.Of(f.Data), true
		cm := &mutate.Commit{
			Result: mutate.Result{Path: f.Path, Op: d.Op, By: by, OldV: d.OldV, Target: mutate.Range{Start: 1, End: len(f.Lines())}},
			Real:   f.Real, Before: f.Data, Mode: f.Mode, Removed: true, Undid: pl.Change.ID,
		}
		err := p.RunHooks(ctx, cm)
		d.ID = cm.Seq
		return output.Result{Outcome: outcome.Updated, Data: d}, err
	case ActMoveBack:
		root := p.Root.Path()
		cm, err := p.MoveLocked(ctx, mutate.MoveRequest{Cwd: root, From: pl.Path, To: pl.To, By: by, Op: d.Op, Undid: pl.Change.ID})
		if cm == nil {
			return output.Result{}, err
		}
		d.Path, d.From, d.OldV, d.NewV, d.ID = cm.Path, cm.From, cm.OldV, cm.NewV, cm.Seq
		return output.Result{Outcome: outcome.Updated, Data: d}, err
	}
	return output.Result{}, outcome.New(outcome.Internal, "rollback: unknown plan action %d", pl.Action)
}

// fileText is the text line of a file-level rollback, e.g.
// "1052  rollback  a.go  undid 1047  moved back from b.go".
func (d Data) fileText() (string, bool) {
	head := fmt.Sprintf("%d  %s  %s  undid %d  ", d.ID, d.Op, d.Path, d.Undid)
	switch {
	case d.Restored:
		return head + "restored v=" + d.NewV + "\n", true
	case d.Removed:
		return head + "removed v=" + d.OldV + "\n", true
	case d.From != "":
		s := head + "moved back from " + d.From
		if d.NewV != "" {
			s += " v=" + d.NewV
		}
		return s + "\n", true
	}
	return "", false
}

// vacant fails with conflict when rel exists (restoring or moving back onto
// it would overwrite a file).
func vacant(ctx context.Context, ws *filecmd.Workspace, st *history.Store, c history.Change, rel string) error {
	_, abs, err := ws.Root.Resolve(ws.Root.Path(), rel)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(abs); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	e := laterConflictOn(ctx, st, c.ID, rel, "cannot undo %d: %s exists again", c.ID, rel)
	e.Hint = "remove or move it first (lino rm " + rel + "), then retry"
	if f, err := ws.Load(ws.Root.Path(), rel); err == nil && len(f.Lines()) > 0 {
		e = e.WithLines(f.Path, anchor.Region(f.Lines(), 1, min(len(f.Lines()), 5)))
	}
	return e
}

func removedConflict(c history.Change, m history.Mapped) error {
	last := m.Overlaps[len(m.Overlaps)-1:]
	return outcome.New(outcome.Conflict, "cannot undo %d: %s was removed by change %s", c.ID, m.Path, ids(last)).
		WithHint("undo the removal first: lino rollback " + ids(last))
}

func changedConflict(ctx context.Context, st *history.Store, c history.Change, path string, f *fileio.File, format string, args ...any) error {
	e := laterConflictOn(ctx, st, c.ID, path, format, args...)
	if len(f.Lines()) > 0 {
		e = e.WithLines(f.Path, anchor.Region(f.Lines(), 1, min(len(f.Lines()), 5)))
	}
	return e
}

func laterConflict(ctx context.Context, st *history.Store, c history.Change, format string, args ...any) error {
	return laterConflictOn(ctx, st, c.ID, c.Path, format, args...)
}

// laterConflictOn is a conflict naming the changes to path after id.
func laterConflictOn(ctx context.Context, st *history.Store, id int64, path, format string, args ...any) *outcome.Error {
	msg := fmt.Sprintf(format, args...)
	later, _ := st.List(ctx, history.Filter{Path: path, Since: id, Asc: true, Limit: 10})
	if len(later) == 0 {
		return outcome.New(outcome.Conflict, "%s", msg).WithHint("lino history " + path)
	}
	return outcome.New(outcome.Conflict, "%s (later changes: %s)", msg, ids(later)).
		WithHint("lino show " + strconv.FormatInt(later[0].ID, 10))
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
