package index

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/ignore"
)

func reconcile(t *testing.T, db *DB, root string) Summary {
	t.Helper()
	rules, err := ignore.NewRules(root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := db.Reconcile(context.Background(), rules, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type counts struct{ files, added, modified, removed, moved int }

func (s Summary) counts() counts {
	return counts{s.Files, s.Added, s.Modified, s.Removed, s.Moved}
}

func TestReconcile(t *testing.T) {
	root := newRoot(t)
	db := mustOpen(t, root)
	defer db.Close()

	steps := []struct {
		name   string
		mutate func(t *testing.T)
		want   counts
		// expected changes as "op path" or "moved from->to"
		changes []string
	}{
		{
			name: "initial",
			mutate: func(t *testing.T) {
				writeFile(t, root, "a.go", "package a\nfunc Alpha() {}\n")
				writeFile(t, root, "b/b.go", "package b\nfunc Beta() {}\n")
				writeFile(t, root, "c.bin", "x\x00y")
				writeFile(t, root, "skip.log", "ignored\n")
				writeFile(t, root, ".gitignore", "*.log\n")
				writeFile(t, root, ".lino/config", "read.lines=10\n")
			},
			want:    counts{files: 4, added: 4},
			changes: []string{"added .gitignore", "added a.go", "added b/b.go", "added c.bin"},
		},
		{
			name:   "second run is a no-op",
			mutate: func(t *testing.T) {},
			want:   counts{files: 4},
		},
		{
			name: "touch without content change",
			mutate: func(t *testing.T) {
				future := time.Now().Add(time.Hour)
				if err := os.Chtimes(filepath.Join(root, "a.go"), future, future); err != nil {
					t.Fatal(err)
				}
			},
			want: counts{files: 4},
		},
		{
			name: "rename",
			mutate: func(t *testing.T) {
				if err := os.MkdirAll(filepath.Join(root, "d"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(root, "b/b.go"), filepath.Join(root, "d/beta.go")); err != nil {
					t.Fatal(err)
				}
			},
			want:    counts{files: 4, moved: 1},
			changes: []string{"moved b/b.go->d/beta.go"},
		},
		{
			name: "modify add remove",
			mutate: func(t *testing.T) {
				writeFile(t, root, "a.go", "package a\nfunc Gamma() {}\n")
				writeFile(t, root, "e.txt", "epsilon\n")
				if err := os.Remove(filepath.Join(root, "c.bin")); err != nil {
					t.Fatal(err)
				}
			},
			want:    counts{files: 4, added: 1, modified: 1, removed: 1},
			changes: []string{"modified a.go", "removed c.bin", "added e.txt"},
		},
		{
			name: "copy is not a move",
			mutate: func(t *testing.T) {
				writeFile(t, root, "e2.txt", "epsilon\n")
			},
			want:    counts{files: 5, added: 1},
			changes: []string{"added e2.txt"},
		},
		{
			name: "newly ignored file is removed",
			mutate: func(t *testing.T) {
				writeFile(t, root, ".linoignore", "e2.txt\n")
			},
			want:    counts{files: 5, added: 1, removed: 1},
			changes: []string{"added .linoignore", "removed e2.txt"},
		},
	}
	for _, st := range steps {
		st.mutate(t)
		s := reconcile(t, db, root)
		if got := s.counts(); got != st.want {
			t.Errorf("%s: counts %+v, want %+v", st.name, got, st.want)
		}
		var got []string
		for _, u := range s.Changes {
			if u.Op == Moved {
				got = append(got, "moved "+u.From+"->"+u.Path)
			} else {
				got = append(got, u.Op.String()+" "+u.Path)
			}
		}
		if strings.Join(got, ",") != strings.Join(st.changes, ",") {
			t.Errorf("%s: changes %q, want %q", st.name, got, st.changes)
		}
	}

	if n := count(t, db, `SELECT count(*) FROM files WHERE path LIKE '.lino/%'`); n != 0 {
		t.Errorf(".lino indexed: %d rows", n)
	}
	if n := count(t, db, `SELECT count(*) FROM files WHERE path = 'd/beta.go' AND id IN (`+MatchFiles+`)`, `"Bet" AND "eta"`); n != 1 {
		t.Errorf("moved file not searchable under new path: %d", n)
	}
}

func TestReconcileMoveKeepsRow(t *testing.T) {
	root := newRoot(t)
	db := mustOpen(t, root)
	defer db.Close()
	writeFile(t, root, "old.go", "package x\n")
	reconcile(t, db, root)
	before := count(t, db, `SELECT id FROM files WHERE path = 'old.go'`)
	if err := os.Rename(filepath.Join(root, "old.go"), filepath.Join(root, "new.go")); err != nil {
		t.Fatal(err)
	}
	s := reconcile(t, db, root)
	if s.Moved != 1 {
		t.Fatalf("moved = %d, want 1", s.Moved)
	}
	if after := count(t, db, `SELECT id FROM files WHERE path = 'new.go'`); after != before {
		t.Errorf("row id changed %d -> %d: file was re-tokenised", before, after)
	}
}

func dump(t *testing.T, db *DB) string {
	t.Helper()
	rows, err := db.SQL.Query(`SELECT path, hash, size, mode, lines, binary, content FROM files ORDER BY path`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var path, hash, content string
		var size, mode, lines, bin int64
		if err := rows.Scan(&path, &hash, &size, &mode, &lines, &bin, &content); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s %s %d %d %d %d %q\n", path, hash, size, mode, lines, bin, content)
	}
	for _, term := range []string{"alpha", "beta", "gamma", "delta"} {
		var n int
		db.SQL.QueryRow(`SELECT count(*) FROM words WHERE words MATCH ?`, term).Scan(&n)
		fmt.Fprintf(&b, "words %s %d\n", term, n)
	}
	return b.String()
}

func TestReconcileRebuildEqualsIncremental(t *testing.T) {
	root := newRoot(t)
	db := mustOpen(t, root)
	defer db.Close()

	rng := rand.New(rand.NewPCG(1, 2))
	words := []string{"alpha", "beta", "gamma", "delta"}
	name := func() string { return fmt.Sprintf("d%d/f%d.txt", rng.IntN(3), rng.IntN(12)) }
	for round := range 6 {
		for range 8 {
			p := name()
			switch rng.IntN(4) {
			case 0:
				os.Remove(filepath.Join(root, p))
			case 1:
				q := name()
				os.MkdirAll(filepath.Dir(filepath.Join(root, q)), 0o755)
				os.Rename(filepath.Join(root, p), filepath.Join(root, q))
			default:
				writeFile(t, root, p, fmt.Sprintf("%s %d\n%s\n", words[rng.IntN(4)], round, words[rng.IntN(4)]))
			}
		}
		reconcile(t, db, root)
	}

	fresh, err := OpenPath(context.Background(), filepath.Join(root, ".lino", "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	reconcile(t, fresh, root)
	if a, b := dump(t, db), dump(t, fresh); a != b {
		t.Errorf("incremental != rebuild\nincremental:\n%s\nrebuild:\n%s", a, b)
	}
}

// TestReconcileLargeCorpus times a first reconcile on a synthetic 1M-line
// repository. Run with LINO_BIG=1.
func TestReconcileLargeCorpus(t *testing.T) {
	if os.Getenv("LINO_BIG") == "" {
		t.Skip("set LINO_BIG=1 to run")
	}
	root := newRoot(t)
	const files, lines = 5000, 200
	rng := rand.New(rand.NewPCG(3, 4))
	vocab := []string{"func", "return", "err", "nil", "if", "for", "range", "ctx", "Withdraw", "amount", "wallet", "service", "string", "int64", "struct", "package"}
	for i := range files {
		var b strings.Builder
		for l := range lines {
			fmt.Fprintf(&b, "\t%s %s(%s_%d) %s\n", vocab[rng.IntN(len(vocab))], vocab[rng.IntN(len(vocab))], vocab[rng.IntN(len(vocab))], l, vocab[rng.IntN(len(vocab))])
		}
		writeFile(t, root, fmt.Sprintf("pkg%d/sub%d/file%d.go", i%50, i%7, i), b.String())
	}
	db := mustOpen(t, root)
	defer db.Close()
	s := reconcile(t, db, root)
	t.Logf("first reconcile: %d files, %d lines, %v", s.Files, files*lines, s.Duration)
	if s.Added != files {
		t.Errorf("added = %d, want %d", s.Added, files)
	}
	s = reconcile(t, db, root)
	t.Logf("no-op reconcile: %v", s.Duration)
	if s.Duration > 30*time.Second {
		t.Errorf("reconcile too slow: %v", s.Duration)
	}
}

func TestReconcileMarksIgnoredRemovals(t *testing.T) {
	root := newRoot(t)
	db := mustOpen(t, root)
	defer db.Close()
	writeFile(t, root, "keep.go", "package k\n")
	writeFile(t, root, "gone.go", "package g\n")
	writeFile(t, root, "dep/x.js", "x\n")
	reconcile(t, db, root)

	if err := os.Remove(filepath.Join(root, "gone.go")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, ".linoignore", "dep/\n")
	got := map[string]bool{}
	for _, u := range reconcile(t, db, root).Changes {
		if u.Op == Removed {
			got[u.Path] = u.Ignored
		}
	}
	want := map[string]bool{"gone.go": false, "dep/x.js": true}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("removed (path: ignored) = %v, want %v", got, want)
	}
}
