package lscmd

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/outcome"
)

var update = flag.Bool("update", false, "rewrite golden files")

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, s := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func testRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".lino/config":             "",
		".gitignore":               "build/\n",
		"README.md":                "# demo\n\nhello\n",
		"build/out.txt":            "ignored\n",
		"wallet/service.go":        strings.Repeat("x\n", 240),
		"wallet/errors.go":         "package wallet\n",
		"wallet/img/logo.png":      "\x89PNG\x00\x00" + strings.Repeat("\x00", 4000),
		"wallet/internal/db/db.go": "package db\n\nfunc Open() {}\n",
		"empty/.gitkeep":           "",
	})
	if err := os.MkdirAll(filepath.Join(root, "nothing"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func manyRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{".lino/config": ""}
	for d := 0; d < 5; d++ {
		for f := 0; f < 50; f++ {
			files[fmt.Sprintf("d%d/f%02d.txt", d, f)] = "a\n"
		}
	}
	for f := 0; f < 210; f++ {
		files[fmt.Sprintf("flat/f%03d.txt", f)] = "a\n"
	}
	writeTree(t, root, files)
	return root
}

func run(cwd string, args ...string) (stdout, stderr string, code int) {
	var o, e bytes.Buffer
	code = cli.Default.Main(context.Background(), args, cli.Env{Cwd: cwd, Stdin: strings.NewReader("")}, &o, &e)
	return o.String(), e.String(), code
}

func TestLsGolden(t *testing.T) {
	root := testRoot(t)
	many := manyRoot(t)
	tests := []struct {
		name string
		root string
		cwd  string
		args []string
		code int
	}{
		{"root", root, "", nil, 0},
		{"subpath", root, "", []string{"wallet"}, 0},
		{"subpath_slash", root, "", []string{"wallet/"}, 0},
		{"depth1", root, "", []string{"--depth", "1"}, 0},
		{"depth2_sub", root, "", []string{"wallet", "--depth", "2"}, 0},
		{"from_subdir", root, "wallet", nil, 0},
		{"file", root, "", []string{"wallet/service.go"}, 0},
		{"binary", root, "", []string{"wallet/img"}, 0},
		{"no_indexed", root, "", []string{"nothing"}, 0},
		{"ignored", root, "", []string{"build"}, 0},
		{"ignored_file", root, "", []string{"build/out.txt"}, 0},
		{"missing", root, "", []string{"nope"}, 3},
		{"outside", root, "", []string{"../x"}, 7},
		{"bad_depth", root, "", []string{"--depth", "-1"}, 2},
		{"truncated", many, "", nil, 0},
		{"truncated_flat", many, "", []string{"flat"}, 0},
		{"fits_depth1", many, "", []string{"--depth", "1"}, 0},
	}
	for _, tt := range tests {
		for _, mode := range []string{"txt", "json"} {
			t.Run(tt.name+"_"+mode, func(t *testing.T) {
				args := append([]string{"ls"}, tt.args...)
				if mode == "json" {
					args = append(args, "--json")
				}
				out, errOut, code := run(filepath.Join(tt.root, tt.cwd), args...)
				if code != tt.code {
					t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tt.code, out, errOut)
				}
				got := "stdout:\n" + out + "stderr:\n" + strings.ReplaceAll(errOut, tt.root, "$ROOT")
				path := filepath.Join("testdata", "ls_"+tt.name+"."+mode+".golden")
				if *update {
					if err := os.MkdirAll("testdata", 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("%v (run with -update)", err)
				}
				if got != string(want) {
					t.Errorf("got:\n%s\nwant:\n%s", got, want)
				}
			})
		}
	}
}

func TestLsNotRunning(t *testing.T) {
	_, errOut, code := run(t.TempDir(), "ls")
	if code != 8 || !strings.Contains(errOut, "lino init") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0B"},
		{1023, "1023B"},
		{1024, "1.0K"},
		{7800, "7.6K"},
		{319488, "312K"},
		{1 << 20, "1.0M"},
		{5 << 30, "5.0G"},
	}
	for _, tt := range tests {
		if got := FormatSize(tt.n); got != tt.want {
			t.Errorf("FormatSize(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestListLimit(t *testing.T) {
	files := []File{{Path: "a/x"}, {Path: "a/y"}, {Path: "b"}, {Path: "c/d/e"}}
	tests := []struct {
		name    string
		depth   int
		limit   int
		entries int
		total   int
		out     outcome.Outcome
		hint    string
	}{
		{"all", 0, 0, 7, 7, "", ""},
		{"depth1", 1, 0, 3, 3, "", ""},
		{"cut", 0, 4, 4, 7, outcome.Truncated, "lino ls --depth 1"},
		{"cut_depth2", 2, 5, 5, 6, outcome.Truncated, "lino ls --depth 1"},
		{"exact", 0, 7, 7, 7, "", ""},
		{"too_narrow", 0, 2, 2, 7, outcome.Truncated, "lino ls a --depth 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := List(".", files, tt.depth, tt.limit)
			d := r.Data.(Data)
			if len(d.Entries) != tt.entries || d.Total != tt.total || r.Outcome != tt.out || r.Hint != tt.hint {
				t.Fatalf("entries %d total %d outcome %q hint %q", len(d.Entries), d.Total, r.Outcome, r.Hint)
			}
			if d.Files != 4 {
				t.Fatalf("files %d, want 4", d.Files)
			}
		})
	}
}
