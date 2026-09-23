package mutate

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/astralyx/lino/internal/fileio"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/version"
)

// MoveRequest renames one file inside the root.
type MoveRequest struct {
	Cwd      string
	From, To string
	By       string
	Op       string // change name; empty means "mv"
	Undid    int64  // rollback: the change the move undoes
}

// Move renames a file (text or binary) to a path that must not exist yet,
// creating parent directories. It runs the hooks with a Commit whose Op is
// "mv", From/Path are the old/new paths, Real is the new real path and
// Before/After/Doc are nil. OldV == NewV is the content version, empty for
// files over MaxSize.
func (p *Pipeline) Move(ctx context.Context, req MoveRequest) (*Result, error) {
	fromRel, fromReal, err := p.movePath(req.Cwd, req.From)
	if err != nil {
		return nil, err
	}
	toRel, toReal, err := p.movePath(req.Cwd, req.To)
	if err != nil {
		return nil, err
	}

	l := p.locker()
	l.Lock()
	defer l.Unlock()
	c, err := p.moveLocked(ctx, req, fromRel, fromReal, toRel, toReal)
	if c == nil {
		return nil, err
	}
	return &c.Result, err
}

// MoveLocked is Move for a caller already holding the pipeline lock (see
// Exclusive). It returns the commit the hooks saw, nil if nothing moved.
func (p *Pipeline) MoveLocked(ctx context.Context, req MoveRequest) (*Commit, error) {
	fromRel, fromReal, err := p.movePath(req.Cwd, req.From)
	if err != nil {
		return nil, err
	}
	toRel, toReal, err := p.movePath(req.Cwd, req.To)
	if err != nil {
		return nil, err
	}
	return p.moveLocked(ctx, req, fromRel, fromReal, toRel, toReal)
}

func (p *Pipeline) moveLocked(ctx context.Context, req MoveRequest, fromRel, fromReal, toRel, toReal string) (*Commit, error) {
	_, fromAbs, _ := p.Root.Resolve(req.Cwd, req.From)
	st, err := os.Lstat(fromAbs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, outcome.Wrap(outcome.NotFound, err, fromRel+": no such file")
	case err != nil:
		return nil, outcome.Wrap(outcome.Internal, err, fromRel+": "+err.Error())
	case st.Mode()&fs.ModeSymlink != 0:
		return nil, outcome.New(outcome.Refused, "%s: is a symlink", fromRel)
	case st.IsDir():
		return nil, outcome.New(outcome.Refused, "%s: is a directory; mv moves files", fromRel)
	case !st.Mode().IsRegular():
		return nil, outcome.New(outcome.Refused, "%s: not a regular file", fromRel)
	}
	_, toAbs, _ := p.Root.Resolve(req.Cwd, req.To)
	if exists(toAbs) || exists(toReal) {
		return nil, outcome.New(outcome.Conflict, "%s: already exists", toRel).
			WithHint("remove it first with lino rm " + toRel + ", or pick another name")
	}

	var v string
	if p.MaxSize <= 0 || st.Size() <= p.MaxSize {
		data, err := os.ReadFile(fromReal)
		if err != nil {
			return nil, outcome.Wrap(outcome.Internal, err, fromRel+": "+err.Error())
		}
		v = version.Of(data)
	}

	if err := os.MkdirAll(filepath.Dir(toReal), 0o755); err != nil {
		return nil, outcome.Wrap(outcome.Internal, err, toRel+": "+err.Error())
	}
	if err := renameNoReplace(fromReal, toReal); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, outcome.New(outcome.Conflict, "%s: already exists", toRel)
		}
		return nil, outcome.Wrap(outcome.Internal, err, "mv "+fromRel+" "+toRel+": "+err.Error())
	}
	syncDir(filepath.Dir(fromReal))
	syncDir(filepath.Dir(toReal))

	op := req.Op
	if op == "" {
		op = "mv"
	}
	c := &Commit{Result: Result{Path: toRel, From: fromRel, Op: op, By: req.By, OldV: v, NewV: v}, Real: toReal, Undid: req.Undid}
	return c, p.RunHooks(ctx, c)
}

func (p *Pipeline) movePath(cwd, path string) (rel, real string, err error) {
	rel, real, err = p.Root.Jail(cwd, path)
	if err != nil {
		return "", "", err
	}
	realRel, _ := p.Root.Rel(real)
	if rel == "." || realRel == "." {
		return "", "", outcome.New(outcome.Refused, "%s: is the root", path)
	}
	if inMeta(rel) || inMeta(realRel) {
		return "", "", outcome.New(outcome.Refused, "%s: inside %s/", rel, fileio.MetaDir)
	}
	return rel, real, nil
}

func inMeta(rel string) bool {
	return rel == fileio.MetaDir || strings.HasPrefix(rel, fileio.MetaDir+"/")
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// renameNoReplace moves from to to, failing with fs.ErrExist if to exists.
// A hard link claims the target atomically; filesystems without hard links
// fall back to a plain rename.
func renameNoReplace(from, to string) error {
	err := os.Link(from, to)
	if err == nil {
		return os.Remove(from)
	}
	if errors.Is(err, fs.ErrExist) {
		return err
	}
	if exists(to) {
		return fs.ErrExist
	}
	return os.Rename(from, to)
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		fileio.Fsync(d)
		d.Close()
	}
}
