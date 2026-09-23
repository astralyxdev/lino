package filecmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/astralyx/lino/internal/anchor"
	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/fileio"
	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/textfile"
	"github.com/astralyx/lino/internal/version"
)

func init() { cli.Register(RmCommand) }

// RmCommand is `lino rm`.
var RmCommand = &cli.Command{
	Name:    "rm",
	Usage:   "<file> [--v V | --force]",
	Summary: "remove a file",
	Accept:  cli.Mutating,
	MinArgs: 1,
	MaxArgs: 1,
	Setup: func(fs *flag.FlagSet) cli.RunFunc {
		v := fs.String("v", "", "version of the file you last read")
		force := fs.Bool("force", false, "remove without a version check (needed for binary files and symlinks)")
		return func(ctx context.Context, c *cli.Call) (output.Result, error) {
			return Rm(ctx, c.Cwd, RmRequest{Path: c.Arg(0), V: *v, Force: *force, By: c.By})
		}
	},
}

// RmRequest is one rm call.
type RmRequest struct {
	Path  string
	V     string
	Force bool
	By    string
}

// RmData is the result of rm. Data holds the removed bytes for history.
type RmData struct {
	Path    string `json:"path"`
	Op      string `json:"op"`
	By      string `json:"by,omitempty"`
	OldV    string `json:"old_version,omitempty"`
	Binary  bool   `json:"binary,omitempty"`
	Symlink bool   `json:"symlink,omitempty"`
	Data    []byte `json:"-"`
}

// WriteText prints "removed <path> v=old".
func (d RmData) WriteText(w io.Writer) error {
	s := "removed " + d.Path
	if d.OldV != "" {
		s += " v=" + d.OldV
	}
	switch {
	case d.Symlink:
		s += " (symlink)"
	case d.Binary:
		s += " (binary)"
	}
	_, err := fmt.Fprintln(w, s)
	return err
}

// Rm removes a file. Text files need --v at the exact current version or
// --force; binary files and symlinks need --force. A symlink is removed
// itself, never its target.
func Rm(ctx context.Context, cwd string, req RmRequest) (output.Result, error) {
	switch {
	case req.V != "" && req.Force:
		return output.Result{}, outcome.New(outcome.Usage, "rm: pass --v or --force, not both")
	case req.V == "" && !req.Force:
		return output.Result{}, outcome.New(outcome.Conflict, "%s: rm needs --v or --force", req.Path).
			WithHint("read the file first: lino read " + req.Path + ", then lino rm " + req.Path + " --v V")
	case req.V != "" && !version.Valid(req.V):
		return output.Result{}, outcome.New(outcome.Usage, "invalid --v %q: want %d hex characters", req.V, version.Len)
	}
	ws, err := Open(cwd)
	if err != nil {
		return output.Result{}, err
	}
	rel, real, err := ws.Root.Jail(cwd, req.Path)
	if err != nil {
		return output.Result{}, err
	}
	_, abs, err := ws.Root.Resolve(cwd, req.Path)
	if err != nil {
		return output.Result{}, err
	}
	if inMetaDir(rel) || inMetaDir(relOf(ws, real)) {
		return output.Result{}, outcome.New(outcome.Refused, "%s: inside %s/", rel, fileio.MetaDir)
	}
	if rel == "." {
		return output.Result{}, outcome.New(outcome.Refused, "%s: is the root", req.Path)
	}
	if err := ws.Prepare(ctx, cwd, req.Path); err != nil {
		return output.Result{}, err
	}
	var res output.Result
	p := ws.Pipeline()
	err = p.Exclusive(func() error {
		var err error
		res, err = rmLocked(ctx, p, rel, real, abs, req)
		return err
	})
	return res, err
}

func rmLocked(ctx context.Context, p *mutate.Pipeline, rel, real, abs string, req RmRequest) (output.Result, error) {
	d := RmData{Path: rel, Op: "rm", By: req.By}
	lst, err := os.Lstat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return output.Result{}, outcome.Wrap(outcome.NotFound, err, rel+": no such file")
		}
		return output.Result{}, outcome.Wrap(outcome.Internal, err, rel+": "+err.Error())
	}
	target := abs
	switch {
	case lst.Mode()&fs.ModeSymlink != 0:
		if !req.Force {
			return output.Result{}, outcome.New(outcome.Refused, "%s: is a symlink; rm needs --force", rel).
				WithHint("lino rm " + rel + " --force")
		}
		d.Symlink = true
	case lst.IsDir():
		return output.Result{}, outcome.New(outcome.Refused, "%s: is a directory", rel)
	case !lst.Mode().IsRegular():
		return output.Result{}, outcome.New(outcome.Refused, "%s: not a regular file", rel)
	default:
		target = real
		data, err := os.ReadFile(real)
		if err != nil {
			return output.Result{}, outcome.Wrap(outcome.Internal, err, rel+": "+err.Error())
		}
		d.Data = data
		d.OldV = version.Of(data)
		d.Binary = textfile.Classify(data) == textfile.Binary
		if d.Binary && !req.Force {
			return output.Result{}, outcome.New(outcome.Refused, "%s: binary file; rm needs --force", rel).
				WithHint("lino rm " + rel + " --force")
		}
		if !req.Force && d.OldV != req.V {
			lines := textfile.Parse(data).Lines
			return output.Result{}, outcome.New(outcome.Conflict, "%s changed since v=%s (now v=%s)", rel, req.V, d.OldV).
				WithHint("re-read the file and retry with --v "+d.OldV).
				WithLines(rel, anchor.Region(lines, 1, 1))
		}
	}
	if err := os.Remove(target); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return output.Result{}, outcome.Wrap(outcome.NotFound, err, rel+": no such file")
		}
		return output.Result{}, outcome.Wrap(outcome.Internal, err, rel+": "+err.Error())
	}
	if dir, err := os.Open(filepath.Dir(target)); err == nil {
		dir.Sync()
		dir.Close()
	}
	c := &mutate.Commit{Result: mutate.Result{Path: rel, Op: "rm", By: req.By, OldV: d.OldV}, Real: target, Before: d.Data, Mode: lst.Mode().Perm()}
	if err := p.RunHooks(ctx, c); err != nil {
		return output.Result{Outcome: outcome.OK, Data: d}, err
	}
	return output.Result{Outcome: outcome.OK, Data: d}, nil
}

func relOf(ws *Workspace, abs string) string {
	rel, _ := ws.Root.Rel(abs)
	return rel
}

func inMetaDir(rel string) bool {
	return rel == fileio.MetaDir || strings.HasPrefix(rel, fileio.MetaDir+"/")
}
