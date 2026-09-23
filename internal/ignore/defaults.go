package ignore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/astralyx/lino/internal/fileio"
)

// LinoignoreFile is the name of lino's own ignore file.
const LinoignoreFile = ".linoignore"

// DefaultLinoignore lists the patterns `lino init` (or `lino run`) writes to a new .linoignore.
var DefaultLinoignore = []string{
	".git/",
	"node_modules/",
	".venv/",
	"venv/",
	"__pycache__/",
	".mypy_cache/",
	".pytest_cache/",
	".tox/",
	"target/",
	"dist/",
	"build/",
	".next/",
	".nuxt/",
	".cache/",
	"coverage/",
	".idea/",
	".vscode/",
	".DS_Store",
	"*.min.js",
	"*.min.css",
	"*.map",
}

const linoignoreHeader = "# created by lino; same syntax as .gitignore\n"

// DefaultLinoignoreContent returns the bytes of a default .linoignore.
func DefaultLinoignoreContent() []byte {
	return []byte(linoignoreHeader + strings.Join(DefaultLinoignore, "\n") + "\n")
}

// EnsureLinoignore writes the default .linoignore into root unless a file (or
// anything else) already exists at that path. It reports whether it created one.
func EnsureLinoignore(root string) (bool, error) {
	p := filepath.Join(root, LinoignoreFile)
	if _, err := os.Lstat(p); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := fileio.WriteAtomic(p, DefaultLinoignoreContent(), 0o644); err != nil {
		return false, err
	}
	return true, nil
}
