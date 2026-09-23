package changescmd

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/changelog"
	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/index"
)

var update = flag.Bool("update", false, "rewrite golden files")

func run(cwd string, args ...string) (stdout, stderr string, code int) {
	var o, e bytes.Buffer
	code = cli.Default.Main(context.Background(), args, cli.Env{Cwd: cwd, Stdin: strings.NewReader("")}, &o, &e)
	return o.String(), e.String(), code
}

// newRoot creates an initialised root with the given config whose change
// log holds entries, numbered from 1.
func newRoot(t *testing.T, config string, entries []changelog.Entry) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".lino"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".lino", "config"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, err := index.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	l, err := changelog.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if _, err := l.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func rng(a, b int) changelog.Range { return changelog.Range{Start: a, End: b} }

func filler(n int) []changelog.Entry {
	var out []changelog.Entry
	for i := range n {
		out = append(out, changelog.Entry{Source: changelog.SourceLino, Op: "edit", Path: "other/f.txt",
			Kind: changelog.Modified, VBefore: "aaaaaa", VAfter: "bbbbbb", Ranges: []changelog.Range{rng(i+1, i+1)}})
	}
	return out
}

// scopeEntries reproduces the scope's example: 1042 changes, then 1043 and 1044.
func scopeEntries() []changelog.Entry {
	es := filler(1042)
	return append(es,
		changelog.Entry{Source: changelog.SourceExternal, Op: "external", Path: "wallet/service.go", Kind: changelog.Modified,
			VBefore: "8c21e0", VAfter: "5f02aa", Ranges: []changelog.Range{rng(12, 18)}},
		changelog.Entry{Source: changelog.SourceLino, Op: "write", Path: "wallet/validate.go", Kind: changelog.Added,
			VAfter: "77c1d3", Ranges: []changelog.Range{rng(1, 9)}},
	)
}

func TestChangesGolden(t *testing.T) {
	scope := newRoot(t, "", scopeEntries())
	small := newRoot(t, "changes.events=3\n", append(filler(4), []changelog.Entry{
		{Source: changelog.SourceLino, Op: "mv", Path: "wallet/new.go", From: "old/w.go", Kind: changelog.Moved},
		{Source: changelog.SourceLino, Op: "rm", Path: "wallet/gone.go", Kind: changelog.Removed, VBefore: "123abc"},
		{Source: changelog.SourceLino, Op: "delete", Path: "wallet/service.go", Kind: changelog.Modified,
			VBefore: "111111", VAfter: "222222", Ranges: []changelog.Range{rng(7, 6), rng(20, 20)}},
	}...))
	empty := newRoot(t, "", nil)
	if err := os.MkdirAll(filepath.Join(small, "wallet"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		root string
		cwd  string
		args []string
		code int
	}{
		{"scope_example", scope, "", []string{"--since", "1042", "--path", "wallet/**"}, 0},
		{"scope_all_after", scope, "", []string{"--since", "1042"}, 0},
		{"at_head", scope, "", []string{"--since", "1044"}, 0},
		{"filtered_out", scope, "", []string{"--since", "1000", "--path", "docs/**"}, 0},
		{"no_since", scope, "", nil, 2},
		{"truncated", small, "", []string{"--since", "0"}, 0},
		{"truncated_next_page", small, "", []string{"--since", "3"}, 0},
		{"filter_skips_to_matches", small, "", []string{"--since", "0", "--path", "wallet"}, 0},
		{"moved_matches_from", small, "", []string{"--since", "0", "--path", "old/*"}, 0},
		{"glob_basename", small, "", []string{"--since", "0", "--path", "*.go", "--path", "*.md"}, 0},
		{"path_from_subdir", small, "wallet", []string{"--since", "0", "--path", "gone.go"}, 0},
		{"empty_log", empty, "", []string{"--since", "0"}, 0},
		{"bad_since", scope, "", []string{"--since", "x"}, 2},
		{"extra_arg", scope, "", []string{"x", "--since", "1"}, 2},
	}
	for _, tt := range tests {
		for _, mode := range []string{"txt", "json"} {
			t.Run(tt.name+"_"+mode, func(t *testing.T) {
				args := append([]string{"changes"}, tt.args...)
				if mode == "json" {
					args = append(args, "--json")
				}
				out, errOut, code := run(filepath.Join(tt.root, tt.cwd), args...)
				if code != tt.code {
					t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tt.code, out, errOut)
				}
				got := "$ lino " + strings.Join(args, " ") + "\n-- stdout --\n" + out + "-- stderr --\n" + errOut
				got = timeRE(got)
				golden := filepath.Join("testdata", tt.name+"_"+mode+".golden")
				if *update {
					if err := os.MkdirAll("testdata", 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatal(err)
				}
				if got != string(want) {
					t.Errorf("mismatch\ngot:\n%s\nwant:\n%s", got, want)
				}
			})
		}
	}
}

// timeRE blanks JSON timestamps so goldens are stable.
func timeRE(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, `"time":"`)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		j := strings.IndexByte(s[i+8:], '"')
		b.WriteString(s[:i+8])
		b.WriteString("T")
		s = s[i+8+j:]
	}
}

func TestFormatRanges(t *testing.T) {
	tests := []struct {
		in   []changelog.Range
		want string
	}{
		{nil, ""},
		{[]changelog.Range{rng(12, 18)}, "12-18"},
		{[]changelog.Range{rng(3, 3), rng(9, 8), rng(20, 22)}, "3,9(del),20-22"},
	}
	for _, tt := range tests {
		if got := FormatRanges(tt.in); got != tt.want {
			t.Errorf("FormatRanges(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
