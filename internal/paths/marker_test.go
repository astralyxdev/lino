package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mk := func(p string) {
		t.Helper()
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	proj := filepath.Join(home, "proj")
	plain := filepath.Join(home, "plain")
	mk(filepath.Join(home, ".lino", "run"))
	mk(filepath.Join(proj, ".lino"))
	mk(plain)

	tests := []struct {
		name string
		dir  string
		want bool
	}{
		{"registry-only home", home, false},
		{"project", proj, true},
		{"no marker", plain, false},
	}
	for _, tt := range tests {
		if got := IsRoot(tt.dir); got != tt.want {
			t.Errorf("%s: IsRoot = %v, want %v", tt.name, got, tt.want)
		}
	}

	if err := os.WriteFile(filepath.Join(home, ".lino", "config"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsRoot(home) {
		t.Error("initialised home: IsRoot = false, want true")
	}
}
