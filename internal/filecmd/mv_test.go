package filecmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/version"
)

func TestMv(t *testing.T) {
	tests := []struct {
		name     string
		cwd      string
		args     []string
		code     int
		gone     string // must not exist afterwards
		want     string // must exist afterwards with wantData
		wantData string
		stdout   string
	}{
		{name: "rename", args: []string{"a.go", "b.go"}, gone: "a.go", want: "b.go",
			wantData: "package a\n\nfunc A() {}\n", stdout: "moved a.go → b.go v="},
		{name: "new dir", args: []string{"a.go", "x/y/z.go"}, gone: "a.go", want: "x/y/z.go",
			wantData: "package a\n\nfunc A() {}\n"},
		{name: "binary", args: []string{"bin.dat", "sub/bin2.dat"}, gone: "bin.dat", want: "sub/bin2.dat",
			wantData: "ab\x00cd"},
		{name: "relative to cwd", cwd: "sub", args: []string{"nested.txt", "../top.txt"}, gone: "sub/nested.txt",
			want: "top.txt", wantData: "x\ny\n", stdout: "moved sub/nested.txt → top.txt"},
		{name: "target exists", args: []string{"a.go", "crlf.txt"}, code: 6, want: "a.go",
			wantData: "package a\n\nfunc A() {}\n"},
		{name: "onto itself", args: []string{"a.go", "a.go"}, code: 6},
		{name: "missing source", args: []string{"nope.go", "b.go"}, code: 3, gone: "b.go"},
		{name: "target escapes", args: []string{"a.go", "../out.go"}, code: 7, want: "a.go",
			wantData: "package a\n\nfunc A() {}\n"},
		{name: "source escapes", args: []string{"../../etc/hosts", "h"}, code: 7, gone: "h"},
		{name: "into meta", args: []string{"a.go", ".lino/a.go"}, code: 7, gone: ".lino/a.go"},
		{name: "from meta", args: []string{".lino/config", "c"}, code: 7, gone: "c"},
		{name: "directory", args: []string{"sub", "sub2"}, code: 7, gone: "sub2"},
		{name: "one arg", args: []string{"a.go"}, code: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := testRoot(t)
			out, errOut, code := run(t, filepath.Join(root, tt.cwd), append([]string{"mv"}, tt.args...)...)
			if code != tt.code {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tt.code, out, errOut)
			}
			if tt.stdout != "" && !strings.HasPrefix(out, tt.stdout) {
				t.Errorf("stdout %q, want prefix %q", out, tt.stdout)
			}
			if tt.gone != "" {
				if _, err := os.Lstat(filepath.Join(root, tt.gone)); !os.IsNotExist(err) {
					t.Errorf("%s still exists (%v)", tt.gone, err)
				}
			}
			if tt.want != "" {
				b, err := os.ReadFile(filepath.Join(root, tt.want))
				if err != nil || string(b) != tt.wantData {
					t.Errorf("%s = %q, %v", tt.want, b, err)
				}
			}
		})
	}
}

func TestMvSymlinkEscape(t *testing.T) {
	root := testRoot(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, _, code := run(t, root, "mv", "a.go", "link/a.go"); code != 7 {
		t.Fatalf("exit %d, want 7", code)
	}
	if _, err := os.Stat(filepath.Join(outside, "a.go")); !os.IsNotExist(err) {
		t.Fatal("file escaped the root")
	}
}

func TestMvModeJSONAndHook(t *testing.T) {
	root := testRoot(t)
	if err := os.Chmod(filepath.Join(root, "a.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	var got *mutate.Commit
	Hooks = []mutate.Hook{func(_ context.Context, c *mutate.Commit) error { got = c; return nil }}
	t.Cleanup(func() { Hooks = nil })

	out, _, code := run(t, root, "mv", "a.go", "b.go", "--json", "--by", "agent-9")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	v := version.Of([]byte("package a\n\nfunc A() {}\n"))
	want := `{"version":1,"ok":true,"outcome":"updated","data":{"from":"a.go","path":"b.go","version":"` + v + `","by":"agent-9"}}` + "\n"
	if out != want {
		t.Errorf("json %s\nwant %s", out, want)
	}
	st, err := os.Stat(filepath.Join(root, "b.go"))
	if err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("mode %v, %v", st, err)
	}
	if got == nil || got.Op != "mv" || got.From != "a.go" || got.Path != "b.go" || got.Real != filepath.Join(mustCanon(t, root), "b.go") {
		t.Errorf("hook commit %+v", got)
	}
}

func mustCanon(t *testing.T, p string) string {
	t.Helper()
	c, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
