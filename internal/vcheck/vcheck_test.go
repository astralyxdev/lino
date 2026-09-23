package vcheck

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/anchor"
	_ "github.com/astralyx/lino/internal/changelog"
	"github.com/astralyx/lino/internal/cli"
	_ "github.com/astralyx/lino/internal/filecmd"
	_ "github.com/astralyx/lino/internal/histrec"
	"github.com/astralyx/lino/internal/initcmd"
	_ "github.com/astralyx/lino/internal/reindex"
	"github.com/astralyx/lino/internal/version"
)

const base = "line a\nline b\nline c\nline d\nline e\nline f\nline g\nline h\nline i\nline j\n"

func setup(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := initcmd.Init(context.Background(), root, root); err != nil {
		t.Fatal(err)
	}
	return root
}

func run(root, stdin string, args ...string) (int, string) {
	var o, e bytes.Buffer
	code := cli.Default.Main(context.Background(), args,
		cli.Env{Cwd: root, Stdin: strings.NewReader(stdin), Getenv: func(string) string { return "" }}, &o, &e)
	return code, o.String() + e.String()
}

func cur(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func anc(n int, text string) string { return anchor.Of(n, text).String() }

// step is a change made after the agent read v0: through lino (args, with
// --v of the current file appended) or on disk (disk).
type step struct {
	stdin string
	args  []string
	disk  func(string) string
}

func TestRangeAwareCheck(t *testing.T) {
	v0 := version.Of([]byte(base))
	unknown := strings.Repeat("0", version.Len)
	tests := []struct {
		name  string
		prior []step
		stdin string
		args  []string // --v is appended
		v     string   // default v0
		code  int
		want  string // file after, when accepted
	}{
		{name: "same version", stdin: "B\n", args: []string{"edit", "f.txt", "2", "2"},
			want: strings.Replace(base, "line b", "B", 1)},
		{name: "unrelated change elsewhere accepted",
			prior: []step{{stdin: "I\n", args: []string{"edit", "f.txt", "9", "9"}}},
			stdin: "B\n", args: []string{"edit", "f.txt", "2", "2"},
			want: "line a\nB\nline c\nline d\nline e\nline f\nline g\nline h\nI\nline j\n"},
		{name: "adjacent change accepted",
			prior: []step{{stdin: "D\n", args: []string{"edit", "f.txt", "4", "4"}}},
			args:  []string{"delete", "f.txt", "5", "5"},
			want:  "line a\nline b\nline c\nD\nline f\nline g\nline h\nline i\nline j\n"},
		{name: "target changed conflicts",
			prior: []step{{stdin: "E\n", args: []string{"edit", "f.txt", "5", "5"}}},
			stdin: "X\n", args: []string{"edit", "f.txt", "4", "6"}, code: 6},
		{name: "insert inside target conflicts",
			prior: []step{{stdin: "new\n", args: []string{"insert", "f.txt", "--after", "4"}}},
			args:  []string{"delete", "f.txt", "4", "5"}, code: 6},
		{name: "plain lines shifted by insert above conflict",
			prior: []step{{stdin: "top\n", args: []string{"insert", "f.txt", "--at-start"}}},
			stdin: "X\n", args: []string{"edit", "f.txt", "5", "5"}, code: 6},
		{name: "anchors relocated past insert above accepted",
			prior: []step{{stdin: "top\n", args: []string{"insert", "f.txt", "--at-start"}}},
			stdin: "E\n", args: []string{"edit", "f.txt", anc(5, "line e"), anc(5, "line e")},
			want: "top\n" + strings.Replace(base, "line e", "E", 1)},
		{name: "anchors relocated past delete above accepted",
			prior: []step{{args: []string{"delete", "f.txt", "1", "2"}}},
			stdin: "after e\n", args: []string{"insert", "f.txt", "--after", anc(5, "line e")},
			want: "line c\nline d\nline e\nafter e\nline f\nline g\nline h\nline i\nline j\n"},
		{name: "insert at anchor whose line changed conflicts",
			prior: []step{{stdin: "E\n", args: []string{"edit", "f.txt", "5", "5"}}},
			stdin: "x\n", args: []string{"insert", "f.txt", "--before", "5"}, code: 6},
		{name: "insert at end after append accepted",
			prior: []step{{stdin: "k\n", args: []string{"insert", "f.txt", "--at-end"}}},
			stdin: "l\n", args: []string{"insert", "f.txt", "--at-end"},
			want: base + "k\nl\n"},
		{name: "replace with change elsewhere accepted",
			prior: []step{{stdin: "I\n", args: []string{"edit", "f.txt", "9", "9"}}},
			stdin: "line c\n<<<lino>>>\nC\n", args: []string{"replace", "f.txt"},
			want: "line a\nline b\nC\nline d\nline e\nline f\nline g\nline h\nI\nline j\n"},
		{name: "replace of changed lines conflicts",
			prior: []step{{stdin: "line b\nline c\nline c2\n", args: []string{"edit", "f.txt", "2", "3"}}},
			stdin: "line c\nline c2\n<<<lino>>>\nC\n", args: []string{"replace", "f.txt"}, code: 6},
		{name: "external change elsewhere accepted",
			prior: []step{{disk: func(s string) string { return strings.Replace(s, "line i", "ext", 1) }}},
			stdin: "B\n", args: []string{"edit", "f.txt", "2", "2"},
			want: "line a\nB\nline c\nline d\nline e\nline f\nline g\nline h\next\nline j\n"},
		{name: "external change of target conflicts",
			prior: []step{{disk: func(s string) string { return strings.Replace(s, "line b", "ext", 1) }}},
			stdin: "B\n", args: []string{"edit", "f.txt", "2", "2"}, code: 6},
		{name: "several later changes accepted",
			prior: []step{
				{stdin: "I\n", args: []string{"edit", "f.txt", "9", "9"}},
				{stdin: "top\n", args: []string{"insert", "f.txt", "--at-start"}},
				{args: []string{"delete", "f.txt", "1", "1"}},
			},
			stdin: "B\n", args: []string{"edit", "f.txt", "2", "2"},
			want: "line a\nB\nline c\nline d\nline e\nline f\nline g\nline h\nI\nline j\n"},
		{name: "unknown version exact only",
			prior: []step{{stdin: "I\n", args: []string{"edit", "f.txt", "9", "9"}}},
			stdin: "B\n", args: []string{"edit", "f.txt", "2", "2"}, v: unknown, code: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := setup(t)
			for _, s := range tt.prior {
				if s.disk != nil {
					if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte(s.disk(cur(t, root))), 0o644); err != nil {
						t.Fatal(err)
					}
					continue
				}
				v := version.Of([]byte(cur(t, root)))
				if code, out := run(root, s.stdin, append(s.args, "--v", v)...); code != 0 {
					t.Fatalf("prior %v: exit %d\n%s", s.args, code, out)
				}
			}
			before := cur(t, root)
			v := tt.v
			if v == "" {
				v = v0
			}
			code, out := run(root, tt.stdin, append(tt.args, "--v", v)...)
			if code != tt.code {
				t.Fatalf("exit %d, want %d\n%s", code, tt.code, out)
			}
			got := cur(t, root)
			switch {
			case tt.code != 0 && got != before:
				t.Fatalf("conflict wrote the file:\n%s", got)
			case tt.code != 0 && !strings.Contains(out, "changed since v="+v):
				t.Fatalf("conflict output:\n%s", out)
			case tt.code == 0 && got != tt.want:
				t.Fatalf("file:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}
