package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUpdateCarriesOldContent(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		before    string // "" = not indexed
		after     string // "" = removed
		op        Op
		old       string
		oldBinary bool
	}{
		{"added", "", "new\n", Added, "", false},
		{"modified", "a\nb\n", "a\nB\n", Modified, "a\nb\n", false},
		{"text to binary", "a\n", "a\x00", Modified, "a\n", false},
		{"binary to text", "a\x00", "a\n", Modified, "", true},
		{"removed", "gone\n", "", Removed, "gone\n", false},
		{"unchanged", "same\n", "same\n", Unchanged, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRoot(t)
			db := mustOpen(t, root)
			abs := filepath.Join(root, "f.txt")
			if tt.before != "" {
				writeFile(t, root, "f.txt", tt.before)
				if _, err := db.IndexFile(ctx, "f.txt", abs, 0); err != nil {
					t.Fatal(err)
				}
			}
			if tt.after == "" {
				os.Remove(abs)
			} else {
				writeFile(t, root, "f.txt", tt.after)
			}
			u, err := db.IndexFile(ctx, "f.txt", abs, 0)
			if err != nil {
				t.Fatal(err)
			}
			if u.Op != tt.op || u.Old != tt.old || u.OldBinary != tt.oldBinary {
				t.Errorf("update %+v", u)
			}
		})
	}
}

// The hook of an external change sees the old content in the update and the
// new content in the index.
func TestExternalHookOldAndNew(t *testing.T) {
	ctx := context.Background()
	root := newRoot(t)
	db := mustOpen(t, root)
	writeFile(t, root, "a.txt", "one\ntwo\n")
	reconcile(t, db, root)

	var got []Update
	var cur string
	prev := External
	External = func(ctx context.Context, d *DB, ups []Update) {
		got = ups
		fi, _, _ := d.File(ctx, "a.txt")
		cur = fi.Content
	}
	t.Cleanup(func() { External = prev })

	abs := writeFile(t, root, "a.txt", "one\nTWO\nthree\n")
	os.Chtimes(abs, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	if _, err := db.Refresh(ctx, root, []string{"a.txt"}, 0); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Op != Modified || got[0].Old != "one\ntwo\n" || cur != "one\nTWO\nthree\n" {
		t.Fatalf("hook got %+v, current %q", got, cur)
	}
}

func TestReconcileChangesCarryOld(t *testing.T) {
	root := newRoot(t)
	db := mustOpen(t, root)
	writeFile(t, root, "a.txt", "old\n")
	writeFile(t, root, "b.txt", "bye\n")
	reconcile(t, db, root)
	abs := writeFile(t, root, "a.txt", "new content\n")
	os.Chtimes(abs, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	os.Remove(filepath.Join(root, "b.txt"))
	s := reconcile(t, db, root)
	old := map[string]string{}
	for _, u := range s.Changes {
		old[u.Path] = u.Old
	}
	if old["a.txt"] != "old\n" || old["b.txt"] != "bye\n" || len(old) != 2 {
		t.Fatalf("changes %+v", s.Changes)
	}
}
