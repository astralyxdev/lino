package registry

import (
	"path/filepath"

	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/paths"
)

// MarkerDir is the per-root directory whose presence makes a root initialised.
const MarkerDir = ".lino"

// Source says which resolution step picked a target.
type Source string

const (
	FromFlag Source = "flag"
	FromEnv  Source = "env"
	FromCwd  Source = "cwd"
)

// Target is the process a command talks to.
type Target struct {
	Root   string // canonical root
	ID     string
	Entry  *Entry // registry entry, if any; may be stale (see Check)
	Source Source
}

// Resolve picks the target process: flagID (-i/--id, id or name), then envID
// (LINO_ID), then the initialised root containing cwd. An unknown id or name
// fails with not_found; a cwd outside any initialised root fails with
// not_running and a hint with the init command.
func (r *Registry) Resolve(flagID, envID, cwd string) (Target, error) {
	for _, c := range []struct {
		v   string
		src Source
	}{{flagID, FromFlag}, {envID, FromEnv}} {
		if c.v == "" {
			continue
		}
		e, err := r.Lookup(c.v)
		if err != nil {
			return Target{}, err
		}
		return Target{Root: e.Root, ID: e.ID, Entry: &e, Source: c.src}, nil
	}

	root, ok, err := r.FindRoot(cwd)
	if err != nil {
		return Target{}, err
	}
	if !ok {
		dir := cwd
		if c, err := paths.Canonical(cwd); err == nil {
			dir = c
		}
		return Target{}, outcome.New(outcome.NotRunning, "no lino process and no initialised root at %s", dir).
			WithHint("lino init " + dir)
	}
	t := Target{Root: root, ID: IDFor(root), Source: FromCwd}
	if e, err := r.Read(t.ID); err == nil {
		t.Entry = &e
	} else if !outcome.Is(err, outcome.NotFound) {
		return Target{}, err
	}
	return t, nil
}

// FindRoot walks up from dir (canonicalised) to the nearest directory holding
// a .lino/ directory. The user's registry home (the parent of r.Dir, normally
// ~/.lino) is not a root marker.
func (r *Registry) FindRoot(dir string) (string, bool, error) {
	d, err := paths.Canonical(dir)
	if err != nil {
		return "", false, err
	}
	home := filepath.Dir(r.Dir)
	if c, err := paths.Canonical(home); err == nil {
		home = c
	}
	for {
		m := filepath.Join(d, MarkerDir)
		if m != home && paths.IsRoot(d) {
			return d, true, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false, nil
		}
		d = parent
	}
}
