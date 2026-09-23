package filecmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/version"
)

func TestSharedHooks(t *testing.T) {
	const content = "a\nb\nc\n"
	v := version.Of([]byte(content))
	tests := []struct {
		op    string
		args  []string
		stdin string
		path  string
	}{
		{"edit", []string{"edit", "f.txt", "2", "2", "--v", v}, "x\n", "f.txt"},
		{"insert", []string{"insert", "f.txt", "--at-end", "--v", v}, "x\n", "f.txt"},
		{"delete", []string{"delete", "f.txt", "2", "2", "--v", v}, "", "f.txt"},
		{"replace", []string{"replace", "f.txt", "--v", v}, "b\n" + DefaultSep + "\nx\n", "f.txt"},
		{"write", []string{"write", "f.txt", "--v", v}, "new\n", "f.txt"},
		{"mv", []string{"mv", "f.txt", "g.txt"}, "", "g.txt"},
		{"rm", []string{"rm", "f.txt", "--v", v}, "", "f.txt"},
	}
	var got []*mutate.Commit
	Hooks = []mutate.Hook{func(_ context.Context, c *mutate.Commit) error { got = append(got, c); return nil }}
	t.Cleanup(func() { Hooks = nil })
	for _, tt := range tests {
		t.Run(tt.op, func(t *testing.T) {
			got = nil
			root := t.TempDir()
			os.MkdirAll(filepath.Join(root, ".lino"), 0o755)
			if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			var o, e bytes.Buffer
			code := cli.Default.Main(context.Background(), tt.args, cli.Env{Cwd: root, Stdin: strings.NewReader(tt.stdin)}, &o, &e)
			if code != 0 {
				t.Fatalf("exit %d: %s %s", code, o.String(), e.String())
			}
			if len(got) != 1 {
				t.Fatalf("hook called %d times", len(got))
			}
			if got[0].Op != tt.op || got[0].Path != tt.path {
				t.Fatalf("commit op=%q path=%q", got[0].Op, got[0].Path)
			}
		})
	}
}

func TestPipelineSharedPerRoot(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".lino", "x"), 0o755)
	a, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(filepath.Join(root, ".lino", "x"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Pipeline() != b.Pipeline() {
		t.Fatal("pipelines differ for one root")
	}
	other, _ := Open(func() string { d := t.TempDir(); os.MkdirAll(filepath.Join(d, ".lino"), 0o755); return d }())
	if other.Pipeline() == a.Pipeline() {
		t.Fatal("pipelines shared across roots")
	}
}
