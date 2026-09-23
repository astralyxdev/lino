package reindex

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/ignore"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/version"
)

// setup creates an initialised root with files and a reconciled index.
func setup(t *testing.T, files map[string]string, withIndex bool) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".lino"), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, s := range files {
		abs := filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(abs), 0o755)
		if err := os.WriteFile(abs, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if withIndex {
		db, err := index.Open(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		rules, err := ignore.NewRules(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Reconcile(context.Background(), rules, 0); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// search returns the paths whose indexed content contains q, found through
// the trigram index the way search does.
func search(t *testing.T, root, q string) []string {
	t.Helper()
	db, err := index.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var terms []string
	for i := 0; i+3 <= len(q); i++ {
		terms = append(terms, `"`+strings.ToLower(q[i:i+3])+`"`)
	}
	rows, err := db.SQL.Query(`SELECT f.path, f.content FROM tri JOIN files f ON f.id = tri.rowid WHERE tri MATCH ? ORDER BY f.path`,
		strings.Join(terms, " AND "))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p, c string
		if err := rows.Scan(&p, &c); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(c, q) {
			out = append(out, p)
		}
	}
	return out
}

func v(t *testing.T, root, rel string) string {
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return version.Of(b)
}

func TestOwnEditsVisibleToSearch(t *testing.T) {
	ctx := context.Background()
	files := map[string]string{
		"wallet/service.go": "package wallet\n\nfunc Withdraw() error {\n\treturn ErrOldAmount\n}\n",
		"api/handlers.go":   "package api\n\n// calls wallet.Withdraw\n",
		".gitignore":        "build/\n",
	}
	tests := []struct {
		name   string
		mutate func(t *testing.T, root string) error
		query  string
		want   []string
	}{
		{"edit adds text", func(t *testing.T, root string) error {
			_, err := filecmd.Edit(ctx, root, filecmd.EditRequest{Path: "wallet/service.go", Start: "4", End: "4",
				V: v(t, root, "wallet/service.go"), Lines: []string{"\treturn ErrNewAmount"}})
			return err
		}, "ErrNewAmount", []string{"wallet/service.go"}},
		{"edit removes old text", func(t *testing.T, root string) error {
			_, err := filecmd.Edit(ctx, root, filecmd.EditRequest{Path: "wallet/service.go", Start: "4", End: "4",
				V: v(t, root, "wallet/service.go"), Lines: []string{"\treturn ErrNewAmount"}})
			return err
		}, "ErrOldAmount", nil},
		{"insert", func(t *testing.T, root string) error {
			_, err := filecmd.Insert(ctx, root, filecmd.InsertRequest{Path: "api/handlers.go", Where: mutate.AtEnd,
				V: v(t, root, "api/handlers.go"), Stdin: []byte("var inserted = 1\n")})
			return err
		}, "inserted = 1", []string{"api/handlers.go"}},
		{"delete", func(t *testing.T, root string) error {
			_, err := filecmd.Delete(ctx, root, filecmd.DeleteRequest{Path: "api/handlers.go", Start: "3", End: "3",
				V: v(t, root, "api/handlers.go")})
			return err
		}, "calls wallet", nil},
		{"replace", func(t *testing.T, root string) error {
			_, err := filecmd.Replace(ctx, root, filecmd.ReplaceRequest{Path: "api/handlers.go",
				V: v(t, root, "api/handlers.go"), Stdin: []byte("// calls wallet.Withdraw\n<<<lino>>>\n// calls wallet.Deposit\n")})
			return err
		}, "wallet.Deposit", []string{"api/handlers.go"}},
		{"write new file", func(t *testing.T, root string) error {
			_, err := filecmd.Write(ctx, root, filecmd.WriteRequest{Path: "wallet/validate.go", Content: []byte("package wallet\n// Validate checks\n")})
			return err
		}, "Validate checks", []string{"wallet/validate.go"}},
		{"write overwrite", func(t *testing.T, root string) error {
			_, err := filecmd.Write(ctx, root, filecmd.WriteRequest{Path: "api/handlers.go", Force: true, Content: []byte("package api\n// rewritten\n")})
			return err
		}, "rewritten", []string{"api/handlers.go"}},
		{"write into ignored dir is not indexed", func(t *testing.T, root string) error {
			_, err := filecmd.Write(ctx, root, filecmd.WriteRequest{Path: "build/out.go", Content: []byte("// generated artefact\n")})
			return err
		}, "generated artefact", nil},
		{"mv shows new path", func(t *testing.T, root string) error {
			_, err := filecmd.Mv(ctx, root, filecmd.MvRequest{From: "wallet/service.go", To: "wallet/core/service.go"})
			return err
		}, "ErrOldAmount", []string{"wallet/core/service.go"}},
		{"mv into ignored dir drops it", func(t *testing.T, root string) error {
			_, err := filecmd.Mv(ctx, root, filecmd.MvRequest{From: "wallet/service.go", To: "build/service.go"})
			return err
		}, "ErrOldAmount", nil},
		{"rm removes hits", func(t *testing.T, root string) error {
			_, err := filecmd.Rm(ctx, root, filecmd.RmRequest{Path: "wallet/service.go", V: v(t, root, "wallet/service.go")})
			return err
		}, "ErrOldAmount", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := setup(t, files, true)
			if err := tt.mutate(t, root); err != nil {
				t.Fatal(err)
			}
			if got := search(t, root, tt.query); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("search %q = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

func TestMoveKeepsRowAndRefreshesStale(t *testing.T) {
	ctx := context.Background()
	root := setup(t, map[string]string{"a.go": "// alpha one\n"}, true)
	// Change a.go behind the index's back, then move it with lino.
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("// alpha two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := filecmd.Mv(ctx, root, filecmd.MvRequest{From: "a.go", To: "b.go"}); err != nil {
		t.Fatal(err)
	}
	if got := search(t, root, "alpha two"); !slices.Equal(got, []string{"b.go"}) {
		t.Fatalf("stale move: %v", got)
	}
	if got := search(t, root, "alpha one"); got != nil {
		t.Fatalf("old content still indexed: %v", got)
	}
}

func TestNoIndexIsNoop(t *testing.T) {
	ctx := context.Background()
	root := setup(t, map[string]string{"a.go": "x\n"}, false)
	if _, err := filecmd.Edit(ctx, root, filecmd.EditRequest{Path: "a.go", Start: "1", End: "1", V: v(t, root, "a.go"), Lines: []string{"y"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(index.Path(root)); !os.IsNotExist(err) {
		t.Fatalf("hook created an index: %v", err)
	}
}

func TestHookRegistered(t *testing.T) {
	if len(filecmd.Hooks) == 0 {
		t.Fatal("reindex hook not registered")
	}
}
