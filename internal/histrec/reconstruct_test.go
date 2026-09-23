package histrec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/index"
)

func TestReconstructFromIndex(t *testing.T) {
	root := initRoot(t, map[string]string{"f.txt": lines(6)})
	type snap struct{ path, v, content string }
	var snaps []snap
	take := func(rel string) {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		snaps = append(snaps, snap{rel, ver(t, root, rel), string(b)})
	}
	take("f.txt")
	lino(t, root, "B\n", nil, "edit", "f.txt", "2", "2", "--v", ver(t, root, "f.txt"))
	take("f.txt")
	lino(t, root, "", nil, "delete", "f.txt", "5", "6", "--v", ver(t, root, "f.txt"))
	take("f.txt")
	lino(t, root, "", nil, "mv", "f.txt", "g.txt")
	snaps[len(snaps)-1].path = "g.txt" // the newest place with that version wins
	lino(t, root, "top\n", nil, "insert", "g.txt", "--at-start", "--v", ver(t, root, "g.txt"))
	take("g.txt")
	lino(t, root, "whole\nnew\n", nil, "write", "g.txt", "--force")
	take("g.txt")

	ctx := context.Background()
	db, err := index.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range snaps {
		got, err := Reconstruct(ctx, db, "g.txt", s.v)
		if err != nil {
			t.Fatalf("%s %s: %v", s.path, s.v, err)
		}
		if string(got.Content) != s.content || got.Path != s.path {
			t.Fatalf("%s %s: got %s %q, want %q", s.path, s.v, got.Path, got.Content, s.content)
		}
	}
	if _, err := Reconstruct(ctx, db, "g.txt", "000000"); !errors.Is(err, history.ErrNotFound) {
		t.Fatalf("unknown version: %v", err)
	}
}
