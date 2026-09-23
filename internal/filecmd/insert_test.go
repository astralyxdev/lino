package filecmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/anchor"
	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/version"
)

func TestInsert(t *testing.T) {
	v := func(s string) string { return version.Of([]byte(s)) }
	abc := "a\nb\nc\n"
	tests := []struct {
		name    string
		content string
		args    []string // after the file name; --v is added unless noV
		stdin   string
		noV     bool
		code    int
		want    string // file content after
		stdout  string
	}{
		{"before anchor", abc, []string{"--before", "2:" + anchor.Hash("b")}, "x\n", false, 0, "a\nx\nb\nc\n", "lines 2-2"},
		{"after anchor", abc, []string{"--after", "2:" + anchor.Hash("b")}, "x\ny\n", false, 0, "a\nb\nx\ny\nc\n", "lines 3-4"},
		{"before plain number", abc, []string{"--before", "1"}, "x", false, 0, "x\na\nb\nc\n", "lines 1-1"},
		{"after last", abc, []string{"--after", "3"}, "x\n", false, 0, "a\nb\nc\nx\n", "lines 4-4"},
		{"at start", abc, []string{"--at-start"}, "x\n", false, 0, "x\na\nb\nc\n", "lines 1-1"},
		{"at end", abc, []string{"--at-end"}, "x\n", false, 0, "a\nb\nc\nx\n", "lines 4-4"},
		{"moved anchor relocates", "z\n" + abc, []string{"--after", "2:" + anchor.Hash("b")}, "x\n", false, 0, "z\na\nb\nx\nc\n", "lines 4-4"},
		{"empty file at end", "", []string{"--at-end"}, "x\n", false, 0, "x\n", "lines 1-1"},
		{"empty file at start", "", []string{"--at-start"}, "x\ny", false, 0, "x\ny\n", "lines 1-2"},
		{"no final newline at end", "a\nb", []string{"--at-end"}, "x\n", false, 0, "a\nb\nx", "lines 3-3"},
		{"no final newline at start", "a\nb", []string{"--at-start"}, "x\n", false, 0, "x\na\nb", "lines 1-1"},
		{"crlf keeps style", "a\r\nb\r\n", []string{"--after", "1"}, "x\n", false, 0, "a\r\nx\r\nb\r\n", "lines 2-2"},
		{"empty stdin", abc, []string{"--at-end"}, "", false, 2, abc, ""},
		{"no position", abc, nil, "x\n", false, 2, abc, ""},
		{"two positions", abc, []string{"--at-start", "--at-end"}, "x\n", false, 2, abc, ""},
		{"before and after", abc, []string{"--before", "1", "--after", "1"}, "x\n", false, 2, abc, ""},
		{"bad anchor", abc, []string{"--before", "x:1"}, "x\n", false, 2, abc, ""},
		{"stale anchor", abc, []string{"--before", "2:" + anchor.Hash("zzz")}, "x\n", false, 4, abc, ""},
		{"missing v", abc, []string{"--at-end"}, "x\n", true, 6, abc, ""},
		{"stale v", abc, []string{"--at-end", "--v", v("old\n")}, "x\n", true, 6, abc, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".lino"), 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "f.txt")
			if err := os.WriteFile(path, []byte(tt.content), 0o640); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"insert", "f.txt"}, tt.args...)
			if !tt.noV {
				args = append(args, "--v", v(tt.content))
			}
			var o, e bytes.Buffer
			code := cli.Default.Main(context.Background(), args, cli.Env{Cwd: root, Stdin: strings.NewReader(tt.stdin)}, &o, &e)
			if code != tt.code {
				t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", code, tt.code, o.String(), e.String())
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("content = %q, want %q", got, tt.want)
			}
			if tt.code != 0 {
				return
			}
			wantOut := "updated f.txt v=" + v(tt.content) + "→" + v(tt.want) + " " + tt.stdout + "\n"
			if o.String() != wantOut {
				t.Fatalf("stdout = %q, want %q", o.String(), wantOut)
			}
			if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
				t.Fatalf("mode = %v", st.Mode())
			}
		})
	}
}

func TestInsertJSON(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".lino"), 0o755)
	os.WriteFile(filepath.Join(root, "f.txt"), []byte("a\n"), 0o644)
	var o, e bytes.Buffer
	args := []string{"insert", "f.txt", "--at-end", "--v", version.Of([]byte("a\n")), "--json", "--by", "agent-x"}
	if code := cli.Default.Main(context.Background(), args, cli.Env{Cwd: root, Stdin: strings.NewReader("b\n")}, &o, &e); code != 0 {
		t.Fatalf("exit %d: %s %s", code, o.String(), e.String())
	}
	for _, want := range []string{`"outcome":"updated"`, `"path":"f.txt"`, `"op":"insert"`, `"by":"agent-x"`, `"start":2`, `"end":2`, `"total":2`} {
		if !strings.Contains(o.String(), want) {
			t.Errorf("json %s missing %s", o.String(), want)
		}
	}
}
