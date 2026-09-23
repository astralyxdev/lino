package paths

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newTestRoot(t *testing.T) *Root {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNewRootCanonical(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{real, link, link + "/", real + "/./", filepath.Join(real, "..", "real")} {
		r, err := NewRoot(in)
		if err != nil {
			t.Fatalf("NewRoot(%q): %v", in, err)
		}
		if r.Path() != want {
			t.Errorf("NewRoot(%q) = %q, want %q", in, r.Path(), want)
		}
	}
	if _, err := NewRoot(filepath.Join(dir, "missing")); err == nil {
		t.Error("NewRoot(missing): want error")
	}
	f := filepath.Join(dir, "file")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRoot(f); err == nil {
		t.Error("NewRoot(file): want error")
	}
}

func TestResolve(t *testing.T) {
	r := newTestRoot(t)
	root := r.Path()
	sub := filepath.Join(root, "a", "b")
	outside := filepath.Dir(root)

	tests := []struct {
		name    string
		cwd     string
		in      string
		want    string
		wantErr bool
	}{
		{"relative from root", root, "a/b/f.go", "a/b/f.go", false},
		{"empty cwd means root", "", "a/f.go", "a/f.go", false},
		{"relative from subdir", sub, "f.go", "a/b/f.go", false},
		{"absolute inside", sub, filepath.Join(root, "x.go"), "x.go", false},
		{"trailing slash", root, "a/b/", "a/b", false},
		{"dot slash", root, "./a/./b/f.go", "a/b/f.go", false},
		{"dotdot inside root", sub, "../f.go", "a/f.go", false},
		{"dotdot to root", sub, "../..", ".", false},
		{"root itself", root, ".", ".", false},
		{"empty path", root, "", ".", false},
		{"empty path from subdir", sub, "", "a/b", false},
		{"cwd outside uses root", outside, "a/f.go", "a/f.go", false},
		{"relative cwd uses root", "rel", "a/f.go", "a/f.go", false},
		{"dotdot escapes", root, "../x", "", true},
		{"dotdot escapes from subdir", sub, "../../../x", "", true},
		{"absolute outside", root, filepath.Join(outside, "x"), "", true},
		{"sibling with root prefix", root, root + "-other/x", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rel, abs, err := r.Resolve(tt.cwd, tt.in)
			if tt.wantErr {
				if !errors.Is(err, ErrOutsideRoot) {
					t.Fatalf("err = %v, want ErrOutsideRoot", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if rel != tt.want {
				t.Errorf("rel = %q, want %q", rel, tt.want)
			}
			if wantAbs := r.Abs(tt.want); abs != wantAbs {
				t.Errorf("abs = %q, want %q", abs, wantAbs)
			}
		})
	}
}

func TestResolveNonCanonicalPrefix(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	r, err := NewRoot(real)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, cwd, in, want string
	}{
		{"absolute via symlinked root", "", filepath.Join(link, "sub", "new.go"), "sub/new.go"},
		{"cwd via symlinked root", filepath.Join(link, "sub"), "f.go", "sub/f.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rel, _, err := r.Resolve(tt.cwd, tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if rel != tt.want {
				t.Errorf("rel = %q, want %q", rel, tt.want)
			}
		})
	}
}

func TestRel(t *testing.T) {
	r := newTestRoot(t)
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{r.Path(), ".", true},
		{filepath.Join(r.Path(), "a", "b") + "/", "a/b", true},
		{filepath.Dir(r.Path()), "", false},
	}
	for _, tt := range tests {
		got, ok := r.Rel(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Rel(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}
