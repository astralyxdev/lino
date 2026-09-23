package paths

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/astralyx/lino/internal/outcome"
)

const maxLinkHops = 40

// Jail resolves a user path like Resolve and then follows symlinks: the
// target (or, for a path that does not exist yet, its deepest existing
// ancestor) must resolve inside the root. rel is the lexical root-relative
// display path; real is the absolute symlink-resolved path to read or write.
// Escapes fail with outcome refused wrapping ErrOutsideRoot.
func (r *Root) Jail(cwd, p string) (rel, real string, err error) {
	rel, abs, err := r.Resolve(cwd, p)
	if err != nil {
		return "", "", refused(p, err)
	}
	real, err = realPath(abs)
	if err != nil {
		return "", "", refused(p, err)
	}
	if _, ok := r.relOf(real); !ok {
		return "", "", refused(p, ErrOutsideRoot)
	}
	return rel, real, nil
}

func refused(p string, err error) error {
	if errors.Is(err, ErrOutsideRoot) {
		return outcome.Wrap(outcome.Refused, err, p+": outside the root")
	}
	return outcome.Wrap(outcome.Refused, err, p+": cannot resolve: "+err.Error())
}

// realPath resolves every symlink in abs, including a dangling final link,
// and keeps non-existent trailing components as they are.
func realPath(abs string) (string, error) {
	for hops := 0; hops < maxLinkHops; hops++ {
		dir, rest := abs, ""
		for {
			if _, err := os.Lstat(dir); err == nil {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				return abs, nil
			}
			rest = filepath.Join(filepath.Base(dir), rest)
			dir = parent
		}
		c, err := filepath.EvalSymlinks(dir)
		if err == nil {
			return filepath.Join(c, rest), nil
		}
		target, lerr := os.Readlink(dir)
		if lerr != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(dir), target)
		}
		abs = filepath.Join(target, rest)
	}
	return "", errors.New("too many levels of symbolic links")
}
