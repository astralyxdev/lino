package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIDForDir(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "a")
	b := filepath.Join(base, "b")
	for _, d := range []string{a, b} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	id := func(d string) string {
		t.Helper()
		s, err := IDForDir(d)
		if err != nil {
			t.Fatal(err)
		}
		if !ValidID(s) {
			t.Fatalf("invalid id %q", s)
		}
		return s
	}
	tests := []struct {
		name string
		x, y string
		same bool
	}{
		{"same root", a, a, true},
		{"trailing slash", a, a + "/", true},
		{"dot segments", a, filepath.Join(b, "..", "a"), true},
		{"symlink", a, link, true},
		{"different roots", a, b, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := id(tt.x) == id(tt.y); got != tt.same {
				t.Fatalf("same=%v, want %v", got, tt.same)
			}
		})
	}
}

func TestIDForStable(t *testing.T) {
	if IDFor("/x/y") != IDFor("/x/y") {
		t.Fatal("unstable")
	}
}

func TestValidID(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"k3f9qa", true},
		{"000000", true},
		{"K3F9QA", false},
		{"k3f9q", false},
		{"k3f9qa1", false},
		{"k3f-qa", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := ValidID(tt.in); got != tt.want {
			t.Errorf("ValidID(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
