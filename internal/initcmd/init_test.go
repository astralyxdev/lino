package initcmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/config"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/registry"
)

func tempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInit(t *testing.T) {
	ctx := context.Background()
	root := tempDir(t)
	write(t, filepath.Join(root, "a.go"), "package a\n")
	write(t, filepath.Join(root, "sub", "b.txt"), "b\n")
	write(t, filepath.Join(root, ".gitignore"), "ignored.txt\n")
	write(t, filepath.Join(root, "ignored.txt"), "x\n")

	res, err := Init(ctx, filepath.Join(root, "sub"), "..")
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(Data)
	if res.Outcome != outcome.Created || d.Existed || d.Root != root || d.ID != registry.IDFor(root) {
		t.Fatalf("first init: %v %+v", res.Outcome, d)
	}
	if d.Files != 3 || d.Added != 3 || d.GitExcluded {
		t.Fatalf("summary %+v, want 3 files added (a.go, sub/b.txt, .gitignore)", d)
	}
	if _, err := config.Load(root); err != nil {
		t.Fatalf("default config does not load: %v", err)
	}
	cfg, _ := os.ReadFile(filepath.Join(root, ".lino", "config"))
	if c, _ := config.Parse(strings.NewReader(string(cfg)), "config"); c != config.Default() {
		t.Fatal("default config overrides a default")
	}
	if _, err := os.Stat(index.Path(root)); err != nil {
		t.Fatalf("no index: %v", err)
	}

	// Re-init is safe and picks up changes.
	write(t, filepath.Join(root, "c.txt"), "c\n")
	write(t, filepath.Join(root, ".lino", "config"), "read.lines = 7\n")
	res, err = Init(ctx, root, "")
	if err != nil {
		t.Fatal(err)
	}
	d = res.Data.(Data)
	if res.Outcome != outcome.OK || !d.Existed || d.Added != 1 || d.Files != 4 {
		t.Fatalf("re-init: %v %+v", res.Outcome, d)
	}
	if c, _ := config.Load(root); c.ReadLines != 7 {
		t.Fatal("re-init overwrote the config")
	}
}

func TestInitNested(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name  string
		setup func(t *testing.T, base string)
		dir   string
	}{
		{"inside existing root", func(t *testing.T, base string) {
			os.MkdirAll(filepath.Join(base, ".lino"), 0o755)
			os.MkdirAll(filepath.Join(base, "a", "b"), 0o755)
		}, "a/b"},
		{"contains existing root", func(t *testing.T, base string) {
			os.MkdirAll(filepath.Join(base, "x", "deep", ".lino"), 0o755)
		}, "."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := tempDir(t)
			tt.setup(t, base)
			_, err := Init(ctx, base, tt.dir)
			if outcome.Of(err) != outcome.Refused {
				t.Fatalf("got %v, want refused", err)
			}
			if _, err := os.Stat(filepath.Join(base, tt.dir, ".lino", "index.db")); err == nil {
				t.Fatal("index created despite refusal")
			}
		})
	}
}

func TestInitErrors(t *testing.T) {
	base := tempDir(t)
	write(t, filepath.Join(base, "f"), "x")
	tests := []struct {
		dir  string
		want outcome.Outcome
	}{
		{"missing", outcome.NotFound},
		{"f", outcome.Refused},
	}
	for _, tt := range tests {
		if _, err := Init(context.Background(), base, tt.dir); outcome.Of(err) != tt.want {
			t.Errorf("%s: got %v, want %v", tt.dir, err, tt.want)
		}
	}
}

func TestGitExclude(t *testing.T) {
	tests := []struct {
		name    string
		exclude *string // nil = no file
		want    string
	}{
		{"no exclude file", nil, "/.lino/\n"},
		{"appends", ptr("*.log"), "*.log\n/.lino/\n"},
		{"appends after newline", ptr("*.log\n"), "*.log\n/.lino/\n"},
		{"already there", ptr("# x\n.lino/\n"), "# x\n.lino/\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := tempDir(t)
			os.MkdirAll(filepath.Join(root, ".git"), 0o755)
			p := filepath.Join(root, ".git", "info", "exclude")
			if tt.exclude != nil {
				write(t, p, *tt.exclude)
			}
			for i := 0; i < 2; i++ { // idempotent across re-inits
				res, err := Init(context.Background(), root, "")
				if err != nil {
					t.Fatal(err)
				}
				if !res.Data.(Data).GitExcluded {
					t.Fatal("GitExcluded false")
				}
			}
			got, _ := os.ReadFile(p)
			if string(got) != tt.want {
				t.Fatalf("exclude = %q, want %q", got, tt.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }
