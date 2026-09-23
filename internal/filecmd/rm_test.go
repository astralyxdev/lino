package filecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/version"
)

func TestRm(t *testing.T) {
	const text = "one\ntwo\n"
	v := version.Of([]byte(text))
	stale := "000000"
	if stale == v {
		stale = "111111"
	}
	tests := []struct {
		name   string
		cwd    string
		args   []string
		code   int
		gone   string // path that must no longer exist
		kept   string // path that must still exist
		stdout string
		stderr string
	}{
		{name: "with v", args: []string{"a.txt", "--v", v}, gone: "a.txt", stdout: "removed a.txt v=" + v + "\n"},
		{name: "from subdir", cwd: "sub", args: []string{"b.txt", "--v", v}, gone: "sub/b.txt", stdout: "removed sub/b.txt v=" + v},
		{name: "stale v", args: []string{"a.txt", "--v", stale}, code: 6, kept: "a.txt", stdout: "1:", stderr: "changed since"},
		{name: "no v", args: []string{"a.txt"}, code: 6, kept: "a.txt", stderr: "--v or --force"},
		{name: "bad v", args: []string{"a.txt", "--v", "zz"}, code: 2, kept: "a.txt"},
		{name: "v and force", args: []string{"a.txt", "--v", v, "--force"}, code: 2, kept: "a.txt"},
		{name: "force", args: []string{"a.txt", "--force"}, gone: "a.txt", stdout: "removed a.txt"},
		{name: "missing", args: []string{"nope.txt", "--force"}, code: 3},
		{name: "missing with v", args: []string{"nope.txt", "--v", v}, code: 3},
		{name: "binary needs force", args: []string{"bin.dat", "--v", v}, code: 7, kept: "bin.dat", stderr: "--force"},
		{name: "binary force", args: []string{"bin.dat", "--force"}, gone: "bin.dat", stdout: "(binary)"},
		{name: "dir", args: []string{"sub", "--force"}, code: 7, kept: "sub/b.txt"},
		{name: "outside", args: []string{"../x", "--force"}, code: 7},
		{name: "meta", args: []string{".lino/config", "--force"}, code: 7, kept: ".lino/config"},
		{name: "symlink needs force", args: []string{"link.txt", "--v", v}, code: 7, kept: "link.txt"},
		{name: "symlink removes link only", args: []string{"link.txt", "--force"}, gone: "link.txt", kept: "a.txt", stdout: "(symlink)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for p, s := range map[string]string{".lino/config": "", "a.txt": text, "sub/b.txt": text, "bin.dat": "a\x00b"} {
				full := filepath.Join(root, p)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink("a.txt", filepath.Join(root, "link.txt")); err != nil {
				t.Fatal(err)
			}
			out, errOut, code := run(t, filepath.Join(root, tt.cwd), append([]string{"rm"}, tt.args...)...)
			if code != tt.code {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tt.code, out, errOut)
			}
			if !strings.Contains(out, tt.stdout) {
				t.Errorf("stdout %q does not contain %q", out, tt.stdout)
			}
			if !strings.Contains(errOut, tt.stderr) {
				t.Errorf("stderr %q does not contain %q", errOut, tt.stderr)
			}
			if tt.gone != "" {
				if _, err := os.Lstat(filepath.Join(root, tt.gone)); !os.IsNotExist(err) {
					t.Errorf("%s still exists", tt.gone)
				}
			}
			if tt.kept != "" {
				if _, err := os.Lstat(filepath.Join(root, tt.kept)); err != nil {
					t.Errorf("%s removed: %v", tt.kept, err)
				}
			}
		})
	}
}

func TestRmJSON(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".lino"), 0o755)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("x\n"), 0o644)
	out, _, code := run(t, root, "rm", "a.txt", "--force", "--json", "--by", "agent-1")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	var env struct {
		OK      bool   `json:"ok"`
		Outcome string `json:"outcome"`
		Data    RmData `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	want := RmData{Path: "a.txt", Op: "rm", By: "agent-1", OldV: version.Of([]byte("x\n"))}
	if !env.OK || env.Outcome != "ok" || env.Data.Path != want.Path || env.Data.OldV != want.OldV || env.Data.By != want.By || env.Data.Op != want.Op {
		t.Fatalf("got %s", out)
	}
}
