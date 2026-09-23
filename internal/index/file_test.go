package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t testing.TB, root, rel, s string) string {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return abs
}

func triHits(t *testing.T, db *DB, term string) int {
	var q []string
	for i := 0; i+3 <= len(term); i++ {
		q = append(q, `"`+term[i:i+3]+`"`)
	}
	return count(t, db, `SELECT count(*) FROM tri WHERE tri MATCH ?`, strings.Join(q, " AND "))
}

func wordHits(t *testing.T, db *DB, term string) int {
	return count(t, db, `SELECT count(*) FROM words WHERE words MATCH ?`, term)
}

func TestIndexFile(t *testing.T) {
	ctx := context.Background()
	root := newRoot(t)
	db := mustOpen(t, root)
	defer db.Close()

	type step struct {
		name    string
		content *string // nil = delete the file
		op      Op
		binary  bool
		lines   int
		find    string // trigram and word expected to hit
		gone    string // expected not to hit
	}
	s := func(v string) *string { return &v }
	steps := []step{
		{"insert", s("package alpha\nfunc Withdraw() {}\n"), Added, false, 2, "Withdraw", ""},
		{"unchanged", s("package alpha\nfunc Withdraw() {}\n"), Unchanged, false, 2, "Withdraw", ""},
		{"update", s("package alpha\r\nfunc Deposit() {}"), Modified, false, 2, "Deposit", "Withdraw"},
		{"binary", s("ab\x00Deposit"), Modified, true, 0, "", "Deposit"},
		{"text_again", s("Refund\n"), Modified, false, 1, "Refund", ""},
		{"remove", nil, Removed, false, 0, "", "Refund"},
		{"remove_again", nil, Unchanged, false, 0, "", ""},
	}
	var prev string
	for _, st := range steps {
		abs := filepath.Join(root, "a.go")
		if st.content != nil {
			writeFile(t, root, "a.go", *st.content)
		} else {
			os.Remove(abs)
		}
		u, err := db.IndexFile(ctx, "a.go", abs, 0)
		if err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if u.Op != st.op || u.OldHash != prev || u.Binary != st.binary {
			t.Fatalf("%s: got %+v, want op %v old %q binary %v", st.name, u, st.op, prev, st.binary)
		}
		prev = u.Hash
		fi, ok, err := db.File(ctx, "a.go")
		if err != nil {
			t.Fatal(err)
		}
		if st.content == nil {
			if ok {
				t.Fatalf("%s: still indexed", st.name)
			}
		} else {
			if !ok || fi.Hash != Hash([]byte(*st.content)) || fi.Lines != st.lines || fi.Binary != st.binary {
				t.Fatalf("%s: row %+v", st.name, fi)
			}
			if want := *st.content; !st.binary && fi.Content != want {
				t.Fatalf("%s: content %q", st.name, fi.Content)
			}
			if st.binary && fi.Content != "" {
				t.Fatalf("%s: binary content stored", st.name)
			}
		}
		if st.find != "" && (triHits(t, db, st.find) != 1 || wordHits(t, db, st.find) != 1) {
			t.Errorf("%s: %q not found in FTS", st.name, st.find)
		}
		if st.gone != "" && (triHits(t, db, st.gone) != 0 || wordHits(t, db, st.gone) != 0) {
			t.Errorf("%s: %q still in FTS", st.name, st.gone)
		}
	}
}

func TestIndexUnchangedRefreshesStat(t *testing.T) {
	ctx := context.Background()
	root := newRoot(t)
	db := mustOpen(t, root)
	defer db.Close()
	abs := writeFile(t, root, "x.txt", "hello\n")
	if _, err := db.IndexFile(ctx, "x.txt", abs, 0); err != nil {
		t.Fatal(err)
	}
	mt := time.Unix(1700000000, 42)
	os.Chtimes(abs, mt, mt)
	os.Chmod(abs, 0o600)
	u, err := db.IndexFile(ctx, "x.txt", abs, 0)
	if err != nil || u.Op != Unchanged {
		t.Fatalf("%+v %v", u, err)
	}
	fi, _, _ := db.File(ctx, "x.txt")
	if !fi.ModTime.Equal(mt) || fi.Mode != 0o600 {
		t.Fatalf("stat not refreshed: %+v", fi)
	}
}

func TestIndexLargeFileHashOnly(t *testing.T) {
	ctx := context.Background()
	root := newRoot(t)
	db := mustOpen(t, root)
	defer db.Close()
	content := strings.Repeat("large text line\n", 100)
	abs := writeFile(t, root, "big.txt", content)
	u, err := db.IndexFile(ctx, "big.txt", abs, 100)
	if err != nil || u.Op != Added || !u.Binary || u.Hash != Hash([]byte(content)) {
		t.Fatalf("%+v %v", u, err)
	}
	if triHits(t, db, "large") != 0 {
		t.Fatal("large file content indexed")
	}
}

func TestIndexDirIsRemoved(t *testing.T) {
	ctx := context.Background()
	root := newRoot(t)
	db := mustOpen(t, root)
	defer db.Close()
	abs := writeFile(t, root, "d", "x\n")
	db.IndexFile(ctx, "d", abs, 0)
	os.Remove(abs)
	os.Mkdir(abs, 0o755)
	u, err := db.IndexFile(ctx, "d", abs, 0)
	if err != nil || u.Op != Removed {
		t.Fatalf("%+v %v", u, err)
	}
}

// BenchmarkIndexFileModified re-indexes a 500-line file whose content changes
// every iteration (the own-edit path: read, hash, FTS replace, commit).
func BenchmarkIndexFileModified(b *testing.B) {
	ctx := context.Background()
	root := b.TempDir()
	os.Mkdir(filepath.Join(root, ".lino"), 0o755)
	db, err := Open(ctx, root)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&sb, "\tresult%d := compute(ctx, input%d, options) // line %d\n", i, i, i)
	}
	body := sb.String()
	abs := filepath.Join(root, "f.go")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		os.WriteFile(abs, []byte(fmt.Sprintf("// rev %d\n%s", i, body)), 0o644)
		b.StartTimer()
		if _, err := db.IndexFile(ctx, "f.go", abs, 0); err != nil {
			b.Fatal(err)
		}
	}
}
