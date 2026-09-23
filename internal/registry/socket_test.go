package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/outcome"
)

func shortSocketDir(t *testing.T, dir string) {
	t.Helper()
	old := ShortSocketDir
	ShortSocketDir = dir
	t.Cleanup(func() { ShortSocketDir = old })
}

func TestSocketPath(t *testing.T) {
	shortSocketDir(t, "/tmp/lino-test")
	long := "/" + strings.Repeat("h", 150) + "/.lino/run"
	tests := []struct {
		name, dir, want string
	}{
		{"fits", "/home/u/.lino/run", "/home/u/.lino/run/abc123.sock"},
		{"exactly at limit", "/" + strings.Repeat("d", MaxSocketPath-len("/abc123.sock")-1), "/" + strings.Repeat("d", MaxSocketPath-len("/abc123.sock")-1) + "/abc123.sock"},
		{"one over limit", "/" + strings.Repeat("d", MaxSocketPath-len("/abc123.sock")), "/tmp/lino-test/abc123.sock"},
		{"long home", long, "/tmp/lino-test/abc123.sock"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (&Registry{Dir: tt.dir}).SocketPath("abc123"); got != tt.want {
				t.Errorf("SocketPath = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrepareSocket(t *testing.T) {
	base, err := os.MkdirTemp("", "ls")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	longReg := &Registry{Dir: filepath.Join(base, strings.Repeat("h", 150), "run")}

	tests := []struct {
		name    string
		setup   func(t *testing.T, dir string)
		dir     string // short socket dir; "" means base/<name>
		reg     *Registry
		refused bool
	}{
		{"default dir", nil, "", &Registry{Dir: filepath.Join(base, "run")}, false},
		{"fresh short dir", nil, "", longReg, false},
		{"existing loose mode tightened", func(t *testing.T, dir string) { mkdir(t, dir, 0o755) }, "", longReg, false},
		{"symlink refused", func(t *testing.T, dir string) {
			mkdir(t, dir+".target", 0o700)
			if err := os.Symlink(dir+".target", dir); err != nil {
				t.Fatal(err)
			}
		}, "", longReg, true},
		{"file refused", func(t *testing.T, dir string) {
			if err := os.WriteFile(dir, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "", longReg, true},
		{"short dir too long", nil, "/" + strings.Repeat("x", MaxSocketPath), longReg, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.dir
			if dir == "" {
				dir = filepath.Join(base, strings.ReplaceAll(tt.name, " ", "-"))
			}
			shortSocketDir(t, dir)
			if tt.setup != nil {
				tt.setup(t, dir)
			}
			p, err := tt.reg.PrepareSocket("abc123")
			if tt.refused {
				if !outcome.Is(err, outcome.Refused) {
					t.Fatalf("err = %v, want refused", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p != tt.reg.SocketPath("abc123") {
				t.Errorf("path %q, SocketPath %q", p, tt.reg.SocketPath("abc123"))
			}
			fi, err := os.Lstat(filepath.Dir(p))
			if err != nil {
				t.Fatal(err)
			}
			if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
				t.Errorf("socket dir mode %v, want drwx------", fi.Mode())
			}
		})
	}
}

func mkdir(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(dir, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
}
