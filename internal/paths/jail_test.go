package paths

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/astralyx/lino/internal/outcome"
)

func TestJail(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	out := filepath.Join(base, "out")
	for _, d := range []string{root + "/src/sub", out + "/dir"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p string) {
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, name string) {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	write(root + "/src/a.go")
	write(out + "/secret")
	link(out+"/secret", "file-out")
	link(out+"/dir", "dir-out")
	link("src/a.go", "file-in")
	link("src", "dir-in")
	link("../out/secret", "rel-out")
	link(out+"/missing", "dangling-out")
	link("src/new.go", "dangling-in")
	link("loop-b", "loop-a")
	link("loop-a", "loop-b")

	r, err := NewRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	rp := r.Path()

	tests := []struct {
		name     string
		cwd, p   string
		wantRel  string
		wantReal string
		refused  bool
	}{
		{name: "plain file", p: "src/a.go", wantRel: "src/a.go", wantReal: rp + "/src/a.go"},
		{name: "new file", p: "src/sub/new/x.go", wantRel: "src/sub/new/x.go", wantReal: rp + "/src/sub/new/x.go"},
		{name: "cwd subdir", cwd: rp + "/src", p: "a.go", wantRel: "src/a.go", wantReal: rp + "/src/a.go"},
		{name: "traversal", p: "../out/secret", refused: true},
		{name: "traversal from cwd", cwd: rp + "/src", p: "../../out/secret", refused: true},
		{name: "abs outside", p: out + "/secret", refused: true},
		{name: "abs inside", p: root + "/src/a.go", wantRel: "src/a.go", wantReal: rp + "/src/a.go"},
		{name: "symlinked file out", p: "file-out", refused: true},
		{name: "relative symlink out", p: "rel-out", refused: true},
		{name: "symlinked dir out", p: "dir-out/x", refused: true},
		{name: "new file under escaping dir", p: "dir-out/new/y.go", refused: true},
		{name: "dangling link out", p: "dangling-out", refused: true},
		{name: "in-root file symlink", p: "file-in", wantRel: "file-in", wantReal: rp + "/src/a.go"},
		{name: "in-root dir symlink", p: "dir-in/a.go", wantRel: "dir-in/a.go", wantReal: rp + "/src/a.go"},
		{name: "new file under in-root dir symlink", p: "dir-in/z.go", wantRel: "dir-in/z.go", wantReal: rp + "/src/z.go"},
		{name: "dangling link in", p: "dangling-in", wantRel: "dangling-in", wantReal: rp + "/src/new.go"},
		{name: "symlink loop", p: "loop-a", refused: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rel, real, err := r.Jail(tt.cwd, tt.p)
			if tt.refused {
				if !outcome.Is(err, outcome.Refused) {
					t.Fatalf("err = %v, want refused", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if rel != tt.wantRel || real != filepath.FromSlash(tt.wantReal) {
				t.Errorf("got (%q, %q), want (%q, %q)", rel, real, tt.wantRel, tt.wantReal)
			}
		})
	}
}

func TestJailOutsideRootIs(t *testing.T) {
	r, err := NewRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = r.Jail("", "../x")
	if !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("err = %v, want ErrOutsideRoot in chain", err)
	}
}
