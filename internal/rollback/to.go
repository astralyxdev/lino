package rollback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/fileio"
	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/histrec"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/reindex"
	"github.com/astralyx/lino/internal/search"
	"github.com/astralyx/lino/internal/textfile"
	"github.com/astralyx/lino/internal/version"
)

// ToRequest restores files to their state right after change To. Paths are
// --path globs relative to Cwd; none selects every file changed since.
type ToRequest struct {
	To     int64
	Paths  []string
	By     string
	DryRun bool // report what would be restored; write nothing
}

// FileRestore is one file a return-to-point rewrites, creates or removes.
type FileRestore struct {
	Path   string
	Real   string
	Before []byte // current content; nil when the file does not exist
	After  []byte // content right after the point; nil to remove the file
	Doc    *textfile.Doc
	Mode   fs.FileMode
}

// Exists reports whether the file exists now; Keeps whether it will after.
func (r FileRestore) Exists() bool { return r.Before != nil }
func (r FileRestore) Keeps() bool  { return r.After != nil }

// ToPlan is what returning to a point would do: removals first, then writes,
// each by path.
type ToPlan struct {
	To       int64
	Restores []FileRestore
}

// ToFile is one applied restore.
type ToFile struct {
	ID   int64  `json:"id"`
	Path string `json:"path"`
	OldV string `json:"old_version,omitempty"` // empty: the file was created
	NewV string `json:"version,omitempty"`     // empty: the file was removed
}

// ToData is an applied return to a point.
type ToData struct {
	To    int64    `json:"to"`
	By    string   `json:"by,omitempty"`
	Files []ToFile `json:"files"`
}

// WriteText prints one line per file: "1051  rollback  a.go  to 1047  v=5f02aa→8c21e0",
// with "-" for a side where the file does not exist.
func (d ToData) WriteText(w io.Writer) error {
	if len(d.Files) == 0 {
		_, err := fmt.Fprintf(w, "nothing to restore: files are as they were right after %d\n", d.To)
		return err
	}
	for _, f := range d.Files {
		if _, err := fmt.Fprintf(w, "%d  rollback  %s  to %d  v=%s→%s\n", f.ID, f.Path, d.To, dash(f.OldV), dash(f.NewV)); err != nil {
			return err
		}
	}
	return nil
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// RestoreTo returns the files selected by req to their state right after
// change req.To, recording one rollback change per file.
func RestoreTo(ctx context.Context, cwd string, req ToRequest) (output.Result, error) {
	ws, err := filecmd.Open(cwd)
	if err != nil {
		return output.Result{}, err
	}
	dir := "."
	if cwd != "" {
		if abs, err := filepath.Abs(cwd); err == nil {
			if rel, ok := ws.Root.Rel(abs); ok {
				dir = rel
			}
		}
	}
	globs := search.NormalizeGlobs(req.Paths, dir)
	var res output.Result
	p := ws.Pipeline()
	err = p.Exclusive(func() error {
		pl, err := PrepareTo(ctx, ws, req.To, globs)
		if err != nil {
			return err
		}
		if req.DryRun {
			res, err = dryTo(ws, pl)
			return err
		}
		res, err = pl.Apply(ctx, p, req.By)
		return err
	})
	return res, err
}

// PrepareTo computes, for every file changed after change id and selected by
// root-relative globs (none: all), its content right after id. A move pulls
// in both its paths so a file is never duplicated or lost. It fails with
// not_found for an unknown or pruned id and refused when a file cannot be
// reconstructed (binary content, broken chain). The caller holds the
// pipeline lock.
func PrepareTo(ctx context.Context, ws *filecmd.Workspace, id int64, globs []string) (*ToPlan, error) {
	root := ws.Root.Path()
	st, err := histrec.Store(ctx, root)
	if err != nil {
		return nil, outcome.Wrap(outcome.Internal, err, "")
	}
	if _, err := st.Get(ctx, id); errors.Is(err, history.ErrNotFound) {
		return nil, outcome.New(outcome.NotFound, "no change %d in history (unknown or pruned)", id).WithHint("lino history")
	} else if err != nil {
		return nil, outcome.Wrap(outcome.Internal, err, "")
	}
	t, release, err := reindex.Open(ctx, root)
	if err != nil {
		return nil, outcome.Wrap(outcome.Internal, err, "")
	}
	if t == nil {
		return nil, outcome.New(outcome.NotRunning, "no index in %s", root).WithHint("lino init " + root)
	}
	defer release()

	// Record unseen edits first, so they are later changes too.
	if !index.Watched(ctx) {
		if _, err := t.DB.RefreshAll(ctx, root, ws.Config.MaxFileSize); err != nil {
			return nil, outcome.Wrap(outcome.Internal, err, "")
		}
	}
	later, err := st.List(ctx, history.Filter{Since: id, Asc: true})
	if err != nil {
		return nil, outcome.Wrap(outcome.Internal, err, "")
	}
	sel := selectPaths(later, globs)
	if _, err := t.DB.Refresh(ctx, root, sel, ws.Config.MaxFileSize); err != nil {
		return nil, outcome.Wrap(outcome.Internal, err, "")
	}
	if later, err = st.List(ctx, history.Filter{Since: id, Asc: true}); err != nil {
		return nil, outcome.Wrap(outcome.Internal, err, "")
	}
	sel = selectPaths(later, globs)

	cur := histrec.Current(t.DB)
	pl := &ToPlan{To: id}
	for _, rel := range sel {
		r, changed, err := restoreOf(ctx, ws, st, cur, later, rel, id)
		if err != nil {
			return nil, err
		}
		if changed {
			pl.Restores = append(pl.Restores, r)
		}
	}
	slices.SortStableFunc(pl.Restores, func(a, b FileRestore) int {
		if a.Keeps() != b.Keeps() {
			if a.Keeps() {
				return 1
			}
			return -1
		}
		return 0
	})
	return pl, nil
}

// selectPaths returns, sorted, the paths touched by changes that globs
// select, closed over moves: selecting either side of a move selects both.
func selectPaths(changes []history.Change, globs []string) []string {
	sel := map[string]bool{}
	for _, c := range changes {
		for _, p := range []string{c.Path, c.Extra.From} {
			if p != "" && search.MatchPath(globs, p) {
				sel[p] = true
			}
		}
	}
	for grew := true; grew; {
		grew = false
		for _, c := range changes {
			if c.Extra.From == "" || sel[c.Path] == sel[c.Extra.From] {
				continue
			}
			sel[c.Path], sel[c.Extra.From] = true, true
			grew = true
		}
	}
	out := make([]string, 0, len(sel))
	for p := range sel {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// restoreOf compares rel now with rel right after id; changed is false when
// they already match.
func restoreOf(ctx context.Context, ws *filecmd.Workspace, st *history.Store, cur history.CurrentFunc, later []history.Change, rel string, id int64) (FileRestore, bool, error) {
	want, ok, err := st.At(ctx, cur, rel, id)
	if errors.Is(err, history.ErrNotFound) {
		return FileRestore{}, false, outcome.New(outcome.Refused, "cannot restore %s to %d: %v", rel, id, err).
			WithHint("limit the restore with --path, or pick a later point")
	}
	if err != nil {
		return FileRestore{}, false, outcome.Wrap(outcome.Internal, err, "")
	}
	r := FileRestore{Path: rel}
	_, r.Real, err = ws.Root.Jail("", rel)
	if err != nil {
		return FileRestore{}, false, err
	}
	abs := ws.Root.Abs(rel)
	lst, err := os.Lstat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return FileRestore{}, false, outcome.Wrap(outcome.Internal, err, rel+": "+err.Error())
	case !lst.Mode().IsRegular():
		return FileRestore{}, false, outcome.New(outcome.Refused, "cannot restore %s: not a regular file", rel)
	default:
		f, err := ws.Load("", rel)
		if err != nil {
			return FileRestore{}, false, err
		}
		r.Before, r.Mode, r.Real = f.Data, f.Mode, f.Real
		if r.Before == nil {
			r.Before = []byte{}
		}
	}
	if ok {
		r.Doc = want
		r.After = want.Bytes()
		if r.After == nil {
			r.After = []byte{}
		}
		if !r.Exists() {
			r.Mode = modeOf(ws, later, rel)
		}
	}
	switch {
	case !r.Exists() && !r.Keeps():
		return r, false, nil
	case r.Exists() && r.Keeps() && string(r.Before) == string(r.After):
		return r, false, nil
	}
	return r, true, nil
}

// modeOf picks the mode for recreating rel: the mode its removal recorded,
// else the mode of the file it was moved to, else the default.
func modeOf(ws *filecmd.Workspace, later []history.Change, rel string) fs.FileMode {
	for _, c := range later {
		switch {
		case c.Path == rel && c.Extra.Removed && c.Extra.Mode != 0:
			return fs.FileMode(c.Extra.Mode)
		case c.Extra.From == rel:
			if st, err := os.Stat(ws.Root.Abs(c.Path)); err == nil {
				return st.Mode().Perm()
			}
		}
	}
	return fileio.DefaultMode
}

// Apply writes and removes the plan's files, each as a rollback change
// through p's hooks. The caller holds the pipeline lock.
func (pl *ToPlan) Apply(ctx context.Context, p *mutate.Pipeline, by string) (output.Result, error) {
	d := ToData{To: pl.To, By: by, Files: []ToFile{}}
	if len(pl.Restores) == 0 {
		return output.Result{Outcome: outcome.Empty, Data: d}, nil
	}
	for _, r := range pl.Restores {
		res := mutate.Result{Path: r.Path, Op: history.OpRollback, By: by}
		cm := &mutate.Commit{Real: r.Real, Mode: r.Mode, Removed: !r.Keeps()}
		if r.Exists() {
			res.OldV = version.Of(r.Before)
			cm.Before = r.Before
			n := len(textfile.Parse(r.Before).Lines)
			res.Target = mutate.Range{Start: 1, End: n}
		}
		if r.Keeps() {
			res.NewV = version.Of(r.After)
			res.Total = len(r.Doc.Lines)
			res.Changed = mutate.Range{Start: 1, End: res.Total}
			cm.After, cm.Doc = r.After, r.Doc
			if err := fileio.WriteAtomic(r.Real, r.After, r.Mode); err != nil {
				return output.Result{Outcome: outcome.Updated, Data: d}, outcome.Wrap(outcome.Internal, err, r.Path+": "+err.Error())
			}
		} else {
			if err := os.Remove(r.Real); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return output.Result{Outcome: outcome.Updated, Data: d}, outcome.Wrap(outcome.Internal, err, r.Path+": "+err.Error())
			}
			syncParent(filepath.Dir(r.Real))
		}
		cm.Result = res
		err := p.RunHooks(ctx, cm)
		d.Files = append(d.Files, ToFile{ID: cm.Seq, Path: r.Path, OldV: res.OldV, NewV: res.NewV})
		if err != nil {
			return output.Result{Outcome: outcome.Updated, Data: d}, err
		}
	}
	return output.Result{Outcome: outcome.Updated, Data: d}, nil
}

func syncParent(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

func runTo(ctx context.Context, c *cli.Call, to string, paths []string, dry bool) (output.Result, error) {
	switch {
	case to == "":
		return output.Result{}, outcome.New(outcome.Usage, "--path needs --to").WithHint("lino rollback --to <id> --path P")
	case len(c.Args) > 0:
		return output.Result{}, outcome.New(outcome.Usage, "pass a change id or --to, not both")
	}
	id, err := ParseID(to)
	if err != nil {
		return output.Result{}, err
	}
	return RestoreTo(ctx, c.Cwd, ToRequest{To: id, Paths: paths, By: c.By, DryRun: dry})
}
