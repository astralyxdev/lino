package histrec

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/vcache"
)

func withCache(t *testing.T, root string, max int64) *vcache.Cache {
	t.Helper()
	vc := vcache.New(max)
	vcache.Register(root, vc)
	t.Cleanup(func() { vcache.Register(root, nil) })
	return vc
}

type step func(t *testing.T, root string)

func edit(start, end, text string) step {
	return func(t *testing.T, root string) {
		lino(t, root, text, nil, "edit", "f.txt", start, end, "--v", ver(t, root, "f.txt"))
	}
}

func write(text string) step {
	return func(t *testing.T, root string) { lino(t, root, text, nil, "write", "f.txt", "--force") }
}

func external(text string) step {
	return func(t *testing.T, root string) {
		if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		db, err := index.Open(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Refresh(context.Background(), root, []string{"f.txt"}, 0); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCachedReconstructMatchesHistory(t *testing.T) {
	tests := []struct {
		name  string
		steps []step
	}{
		{"line edits", []step{edit("2", "2", "B\n"), edit("4", "5", "D\n"), edit("1", "1", "top\nA\n")}},
		{"version returns", []step{write("x\n"), write("y\n"), write("x\n"), write("z\n")}},
		{"external edits", []step{external("ext1\n"), edit("1", "1", "own\n"), external("ext2\n")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			root := initRoot(t, map[string]string{"f.txt": lines(6)})
			vc := withCache(t, root, 0)
			var vs []string
			for _, s := range tt.steps {
				vs = append(vs, ver(t, root, "f.txt"))
				s(t, root)
			}
			db, err := index.Open(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			st, err := Store(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range vs {
				if _, ok := vc.Get("f.txt", v); !ok {
					t.Fatalf("version %s not cached when it was replaced", v)
				}
				if _, ok := cached(ctx, vc, st, "f.txt", v); !ok {
					t.Fatalf("version %s: cache entry not usable", v)
				}
				want, err := st.Reconstruct(ctx, Current(db), "f.txt", v)
				if err != nil {
					t.Fatal(err)
				}
				got, err := Reconstruct(ctx, db, "f.txt", v)
				if err != nil {
					t.Fatal(err)
				}
				if got.Path != want.Path || string(got.Content) != string(want.Content) || got.Next != want.Next {
					t.Fatalf("%s: cached %s %q next %d, history %s %q next %d", v, got.Path, got.Content, got.Next, want.Path, want.Content, want.Next)
				}
			}
		})
	}
}

func TestCachedNextFollowsLaterChanges(t *testing.T) {
	ctx := context.Background()
	root := initRoot(t, map[string]string{"f.txt": "x\n"})
	vc := withCache(t, root, 0)
	x := ver(t, root, "f.txt")
	lino(t, root, "y\n", nil, "write", "f.txt", "--force")
	_, m, ok := vc.GetMeta("f.txt", x)
	if !ok {
		t.Fatal("x not cached")
	}
	first := m.Next
	vc.Remove("f.txt", x)
	lino(t, root, "x\n", nil, "write", "f.txt", "--force")
	lino(t, root, "z\n", nil, "write", "f.txt", "--force")
	// Pretend the cache only saw the first change leaving x.
	vc.PutMeta("f.txt", x, []byte("x\n"), m)

	db, err := index.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := Reconstruct(ctx, db, "f.txt", x)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := Store(ctx, root)
	want, _ := st.Reconstruct(ctx, Current(db), "f.txt", x)
	if got.Next != want.Next || got.Next <= first {
		t.Fatalf("next %d, want %d (> %d)", got.Next, want.Next, first)
	}
	if _, m, _ := vc.GetMeta("f.txt", x); m.Next != want.Next || m.AsOf <= first {
		t.Fatalf("entry not refreshed: %+v", m)
	}
}

func TestCacheMissAfterMove(t *testing.T) {
	ctx := context.Background()
	root := initRoot(t, map[string]string{"f.txt": lines(3)})
	vc := withCache(t, root, 0)
	v0 := ver(t, root, "f.txt")
	edit("1", "1", "A\n")(t, root)
	lino(t, root, "", nil, "mv", "f.txt", "g.txt")
	// A stale entry for g.txt at v0 must not be trusted across the move.
	vc.PutMeta("g.txt", v0, []byte("bogus\n"), vcache.Meta{Path: "g.txt", Next: 1, AsOf: 1})

	db, err := index.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := Reconstruct(ctx, db, "g.txt", v0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "f.txt" || string(got.Content) != lines(3) {
		t.Fatalf("got %s %q", got.Path, got.Content)
	}
}

func TestCurrentVersionNotFromCache(t *testing.T) {
	ctx := context.Background()
	root := initRoot(t, map[string]string{"f.txt": "x\n"})
	withCache(t, root, 0)
	x := ver(t, root, "f.txt")
	lino(t, root, "y\n", nil, "write", "f.txt", "--force")
	lino(t, root, "x\n", nil, "write", "f.txt", "--force")
	db, err := index.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := Reconstruct(ctx, db, "f.txt", x)
	if err != nil || got.Next != 0 || string(got.Content) != "x\n" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestNoCacheInDirectMode(t *testing.T) {
	root := initRoot(t, map[string]string{"f.txt": "x\n"})
	lino(t, root, "y\n", nil, "write", "f.txt", "--force")
	if vcache.For(root) != nil {
		t.Fatal("cache registered without a live process")
	}
	db, err := index.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got, err := Reconstruct(context.Background(), db, "f.txt", "000000"); err == nil {
		t.Fatalf("unknown version found: %+v", got)
	}
}
