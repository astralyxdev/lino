package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

// ~/.lino holds the process registry; it must not make HOME look like a root.
func TestProjectInsideHome(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	if err := os.MkdirAll(filepath.Join(h.Home, ".lino", "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	h.Root = filepath.Join(h.Home, "proj")
	h.Write("a.txt", "one\n")

	h.ExpectExit(h.Run("init"), 0)
	h.ExpectExit(h.Run("read", "a.txt", "--direct"), 0)
	if _, err := os.Stat(filepath.Join(h.Root, ".lino", "index.db")); err != nil {
		t.Fatalf("index not built in the project: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.Home, ".lino", "index.db")); err == nil {
		t.Fatal("index built in ~/.lino")
	}

	// An initialised HOME is a real root: a project inside it is refused.
	h2 := New(t)
	h2.Home = shortHome(t)
	h2.Root = h2.Home
	h2.ExpectExit(h2.Run("init"), 0)
	h2.Root = filepath.Join(h2.Home, "proj")
	h2.Write("a.txt", "one\n")
	h2.ExpectExit(h2.Run("init"), 7)
}
