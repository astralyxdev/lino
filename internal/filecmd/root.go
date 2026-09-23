// Package filecmd implements the file commands (read, ...) on top of the
// cli framework. Commands register themselves with cli.Default on import.
package filecmd

import (
	"os"
	"path/filepath"

	"github.com/astralyx/lino/internal/config"
	"github.com/astralyx/lino/internal/fileio"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/paths"
)

// Workspace is an initialised root with its config.
type Workspace struct {
	Root   *paths.Root
	Config config.Config
}

// FindRoot returns the nearest ancestor of cwd (inclusive) that contains a
// .lino/ directory. None found is not_running with the init command as hint.
func FindRoot(cwd string) (*paths.Root, error) {
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, outcome.Wrap(outcome.Internal, err, "")
		}
		cwd = wd
	}
	start, err := paths.Canonical(cwd)
	if err != nil {
		return nil, outcome.Wrap(outcome.Internal, err, "")
	}
	for dir := start; ; {
		if paths.IsRoot(dir) {
			r, err := paths.NewRoot(dir)
			if err != nil {
				return nil, outcome.Wrap(outcome.Internal, err, "")
			}
			return r, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return nil, outcome.New(outcome.NotRunning, "no lino root at or above %s", start).WithHint("lino init " + start)
}

// Open finds the root for cwd and loads its config.
func Open(cwd string) (*Workspace, error) {
	r, err := FindRoot(cwd)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(r.Path())
	if err != nil {
		if _, ok := outcome.As(err); ok {
			return nil, err
		}
		return nil, outcome.Wrap(outcome.Internal, err, "")
	}
	return &Workspace{Root: r, Config: cfg}, nil
}

// Load loads a text file of the workspace, relative to cwd.
func (w *Workspace) Load(cwd, p string) (*fileio.File, error) {
	return fileio.LoadFile(w.Root, cwd, p, w.Config.MaxFileSize)
}
