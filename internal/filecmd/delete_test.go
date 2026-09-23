package filecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/anchor"
	"github.com/astralyx/lino/internal/version"
)

func TestDelete(t *testing.T) {
	five := "l1\nl2\nl3\nl4\nl5\n"
	a := func(n int, content string) string {
		lines := strings.Split(content, "\n")
		return anchor.Of(n, strings.TrimSuffix(lines[n-1], "\r")).String()
	}
	tests := []struct {
		name       string
		content    string
		start, end string
		v          string // "" = current version; "-" = omit --v
		code       int
		want       string // file after success; on failure the file must be unchanged
		stdout     string // substring expected in stdout
	}{
		{"middle", five, a(2, five), a(4, five), "", 0, "l1\nl5\n", "deleted lines 2-4"},
		{"first", five, a(1, five), a(1, five), "", 0, "l2\nl3\nl4\nl5\n", "deleted lines 1-1"},
		{"last", five, a(5, five), a(5, five), "", 0, "l1\nl2\nl3\nl4\n", "deleted lines 5-5"},
		{"whole file", five, a(1, five), a(5, five), "", 0, "", "deleted lines 1-5"},
		{"plain numbers", five, "2", "3", "", 0, "l1\nl4\nl5\n", "deleted lines 2-3"},
		{"crlf no final newline", "a\r\nb\r\nc", "3", "3", "", 0, "a\r\nb", "deleted lines 3-3"},
		{"relocated anchor", five, "1:" + anchor.Hash("l3"), "1:" + anchor.Hash("l3"), "", 0, "l1\nl2\nl4\nl5\n", "deleted lines 3-3"},
		{"stale anchor", five, "2:" + anchor.Hash("gone"), "3", "", 4, "", "2:"},
		{"end before start", five, "3", "2", "", 2, "", ""},
		{"beyond eof", five, "4", "9", "", 4, "", ""},
		{"missing --v", five, "1", "1", "-", 6, "", ""},
		{"stale --v", five, "1", "1", version.Of([]byte("other")), 6, "", "1:"},
		{"bad anchor", five, "x", "1", "", 2, "", ""},
		{"single anchor", five, a(2, five), "", "", 0, "l1\nl3\nl4\nl5\n", "deleted lines 2-2"},
		{"single anchor relocated", five, "1:" + anchor.Hash("l4"), "", "", 0, "l1\nl2\nl3\nl5\n", "deleted lines 4-4"},
		{"single anchor stale", five, "2:" + anchor.Hash("gone"), "", "", 4, "", "2:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := testRoot(t)
			p := filepath.Join(root, "f.txt")
			if err := os.WriteFile(p, []byte(tt.content), 0o640); err != nil {
				t.Fatal(err)
			}
			args := []string{"delete", "f.txt", tt.start}
			if tt.end != "" {
				args = append(args, tt.end)
			}
			switch tt.v {
			case "":
				args = append(args, "--v", version.Of([]byte(tt.content)))
			case "-":
			default:
				args = append(args, "--v", tt.v)
			}
			out, errOut, code := run(t, root, args...)
			if code != tt.code {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tt.code, out, errOut)
			}
			if !strings.Contains(out, tt.stdout) {
				t.Fatalf("stdout %q lacks %q", out, tt.stdout)
			}
			got, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			want := tt.want
			if tt.code != 0 {
				want = tt.content
			}
			if string(got) != want {
				t.Fatalf("file = %q, want %q", got, want)
			}
			if tt.code == 0 {
				st, _ := os.Stat(p)
				if st.Mode().Perm() != 0o640 {
					t.Fatalf("mode = %v", st.Mode().Perm())
				}
				if !strings.Contains(out, "v="+version.Of([]byte(tt.content))+"→"+version.Of(got)) {
					t.Fatalf("stdout %q lacks versions", out)
				}
			}
		})
	}
}

func TestDeleteJSON(t *testing.T) {
	root := testRoot(t)
	content, _ := os.ReadFile(filepath.Join(root, "a.go"))
	v := version.Of(content)
	out, errOut, code := run(t, root, "delete", "a.go", "2", "3", "--v", v, "--json", "--by", "agent-x")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	var env struct {
		OK      bool       `json:"ok"`
		Outcome string     `json:"outcome"`
		Data    DeleteData `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	want := DeleteData{Path: "a.go", Op: "delete", OldV: v, Version: version.Of([]byte("package a\n")), Start: 2, End: 3, Total: 1, By: "agent-x"}
	if !env.OK || env.Outcome != "updated" || env.Data != want {
		t.Fatalf("got %+v, want %+v", env, want)
	}
}

func TestDeleteArgCount(t *testing.T) {
	root := testRoot(t)
	for _, args := range [][]string{{"delete", "a.go"}, {"delete", "a.go", "1", "2", "3"}} {
		if _, _, code := run(t, root, args...); code != 2 {
			t.Fatalf("%v: exit %d, want 2", args, code)
		}
	}
}
