package paths

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrOutsideRoot is returned when a path resolves outside the root.
var ErrOutsideRoot = errors.New("path is outside the root")

// Canonical returns dir as an absolute, cleaned path with symlinks resolved.
func Canonical(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// Root is a canonical project root.
type Root struct {
	path string
}

// NewRoot canonicalises dir and checks that it is a directory.
func NewRoot(dir string) (*Root, error) {
	p, err := Canonical(dir)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, &os.PathError{Op: "root", Path: p, Err: errors.New("not a directory")}
	}
	return &Root{path: p}, nil
}

// Path returns the canonical absolute root path.
func (r *Root) Path() string { return r.path }

// Resolve turns a user-supplied path into a root-relative display path
// (slash-separated, "." for the root itself) and an absolute path.
// Relative paths are taken relative to cwd when cwd lies inside the root,
// otherwise relative to the root. An empty cwd means the root.
// Resolution is lexical; symlinks inside the root are not followed here.
func (r *Root) Resolve(cwd, p string) (rel, abs string, err error) {
	if p == "" {
		p = "."
	}
	if filepath.IsAbs(p) {
		abs = filepath.Clean(p)
	} else {
		abs = filepath.Join(r.base(cwd), p)
	}
	rel, ok := r.relOf(abs)
	if !ok {
		c, cerr := canonicalPrefix(abs)
		if cerr != nil {
			return "", "", ErrOutsideRoot
		}
		if rel, ok = r.relOf(c); !ok {
			return "", "", ErrOutsideRoot
		}
		abs = c
	}
	return rel, abs, nil
}

// Rel returns the root-relative display path of an absolute path inside the root.
func (r *Root) Rel(abs string) (string, bool) {
	return r.relOf(filepath.Clean(abs))
}

// Abs returns the absolute path of a root-relative display path.
func (r *Root) Abs(rel string) string {
	return filepath.Join(r.path, filepath.FromSlash(rel))
}

func (r *Root) base(cwd string) string {
	if cwd == "" {
		return r.path
	}
	if !filepath.IsAbs(cwd) {
		return r.path
	}
	cwd = filepath.Clean(cwd)
	if _, ok := r.relOf(cwd); ok {
		return cwd
	}
	if c, err := filepath.EvalSymlinks(cwd); err == nil {
		if _, ok := r.relOf(c); ok {
			return c
		}
	}
	return r.path
}

func (r *Root) relOf(abs string) (string, bool) {
	if abs == r.path {
		return ".", true
	}
	prefix := r.path
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	if !strings.HasPrefix(abs, prefix) {
		return "", false
	}
	return filepath.ToSlash(abs[len(prefix):]), true
}

// canonicalPrefix resolves symlinks in the deepest existing ancestor of abs
// and appends the remaining components, so /tmp/x maps to /private/tmp/x on macOS.
func canonicalPrefix(abs string) (string, error) {
	dir, rest := abs, ""
	for {
		if c, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(c, rest), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrOutsideRoot
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}
