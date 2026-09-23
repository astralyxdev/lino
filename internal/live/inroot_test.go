package live

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInRoot(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(root, "sub", "deep"), 0o755)
	for _, c := range []struct {
		dir  string
		want bool
	}{
		{"", false},
		{root, true},
		{filepath.Join(root, "sub", "deep"), true},
		{filepath.Dir(root), false},
		{root + "x", false},
		{"/", false},
	} {
		if got := inRoot(root, c.dir); got != c.want {
			t.Errorf("inRoot(%q) = %v, want %v", c.dir, got, c.want)
		}
	}
}
