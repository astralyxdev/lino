package paths

import (
	"os"
	"path/filepath"
)

// IsRoot reports whether dir is an initialised lino root: it holds a .lino
// directory. The user's own ~/.lino also holds the process registry, so it
// marks a root only when init has written a config into it.
func IsRoot(dir string) bool {
	meta := filepath.Join(dir, ".lino")
	if st, err := os.Stat(meta); err != nil || !st.IsDir() {
		return false
	}
	if !isUserLinoDir(meta) {
		return true
	}
	_, err := os.Stat(filepath.Join(meta, "config"))
	return err == nil
}

func isUserLinoDir(meta string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	a, err1 := filepath.EvalSymlinks(meta)
	b, err2 := filepath.EvalSymlinks(filepath.Join(home, ".lino"))
	return err1 == nil && err2 == nil && a == b
}
