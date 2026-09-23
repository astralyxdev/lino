package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRefreshAll(t *testing.T) {
	later := time.Now().Add(time.Hour)
	tests := []struct {
		name   string
		change func(root string)
		want   map[string]Op
		hits   map[string]int // trigram hits after refresh
	}{
		{"nothing changed", func(string) {}, map[string]Op{}, map[string]int{"alpha": 1}},
		{"content changed", func(root string) {
			p := filepath.Join(root, "a.go")
			os.WriteFile(p, []byte("// gamma\n"), 0o644)
			os.Chtimes(p, later, later)
		}, map[string]Op{"a.go": Modified}, map[string]int{"alpha": 0, "gamma": 1}},
		{"removed", func(root string) { os.Remove(filepath.Join(root, "b/b.go")) },
			map[string]Op{"b/b.go": Removed}, map[string]int{"beta": 0}},
		{"touched only", func(root string) { os.Chtimes(filepath.Join(root, "a.go"), later, later) },
			map[string]Op{}, map[string]int{"alpha": 1}},
		{"dir removed", func(root string) { os.RemoveAll(filepath.Join(root, "b")) },
			map[string]Op{"b/b.go": Removed}, map[string]int{"beta": 0, "alpha": 1}},
		{"dir replaced by file", func(root string) {
			os.RemoveAll(filepath.Join(root, "b"))
			os.WriteFile(filepath.Join(root, "b"), []byte("x"), 0o644)
		}, map[string]Op{"b/b.go": Removed}, map[string]int{"beta": 0}},
		{"file replaced by dir", func(root string) {
			os.Remove(filepath.Join(root, "a.go"))
			os.Mkdir(filepath.Join(root, "a.go"), 0o755)
		}, map[string]Op{"a.go": Removed}, map[string]int{"alpha": 0, "beta": 1}},
		{"new file not discovered", func(root string) { writeFile(t, root, "c.go", "// delta\n") },
			map[string]Op{}, map[string]int{"delta": 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRoot(t)
			writeFile(t, root, "a.go", "// alpha\n")
			writeFile(t, root, "b/b.go", "// beta\n")
			db := mustOpen(t, root)
			defer db.Close()
			ctx := context.Background()
			for _, p := range []string{"a.go", "b/b.go"} {
				if _, err := db.IndexFile(ctx, p, filepath.Join(root, p), 0); err != nil {
					t.Fatal(err)
				}
			}
			tt.change(root)
			us, err := db.RefreshAll(ctx, root, 0)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]Op{}
			for _, u := range us {
				got[u.Path] = u.Op
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Fatalf("updates %v, want %v", got, tt.want)
			}
			for term, n := range tt.hits {
				if h := triHits(t, db, term); h != n {
					t.Errorf("hits(%q) = %d, want %d", term, h, n)
				}
			}
		})
	}
}

// TestRefreshAllLargeCorpus times the direct-mode pre-query stat pass on a
// synthetic 1M-line repository (5,000 files). Run with LINO_BIG=1.
func TestRefreshAllLargeCorpus(t *testing.T) {
	if os.Getenv("LINO_BIG") == "" {
		t.Skip("set LINO_BIG=1 to run")
	}
	root := newRoot(t)
	const files, lines = 5000, 200
	line := "\treturn wallet.Withdraw(ctx, amount) // padding padding\n"
	var content string
	for range lines {
		content += line
	}
	db := mustOpen(t, root)
	defer db.Close()
	ctx := context.Background()
	for i := range files {
		p := fmt.Sprintf("pkg%d/sub%d/file%d.go", i%50, i%7, i)
		writeFile(t, root, p, content)
		if _, err := db.IndexFile(ctx, p, filepath.Join(root, p), 0); err != nil {
			t.Fatal(err)
		}
	}
	var best time.Duration
	for i := range 10 {
		start := time.Now()
		us, err := db.RefreshAll(ctx, root, 0)
		d := time.Since(start)
		if err != nil || len(us) != 0 {
			t.Fatalf("refresh: %v, %d updates", err, len(us))
		}
		if i == 0 || d < best {
			best = d
		}
		t.Logf("run %d: %v", i, d)
	}
	t.Logf("RefreshAll over %d files, %d lines: best %v", files, files*lines, best)
}
