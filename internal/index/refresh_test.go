package index

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestRefresh(t *testing.T) {
	ctx := context.Background()
	later := time.Now().Add(time.Hour)
	tests := []struct {
		name   string
		change func(t *testing.T, abs string)
		op     Op // Unchanged = no update reported
	}{
		{"untouched", func(*testing.T, string) {}, Unchanged},
		{"edited", func(t *testing.T, abs string) {
			os.WriteFile(abs, []byte("fresh text\n"), 0o644)
			os.Chtimes(abs, later, later)
		}, Modified},
		{"same size same mtime is trusted", func(t *testing.T, abs string) {
			st, _ := os.Stat(abs)
			os.WriteFile(abs, []byte("old-text\n"), 0o644)
			os.Chtimes(abs, st.ModTime(), st.ModTime())
		}, Unchanged},
		{"touched only", func(t *testing.T, abs string) { os.Chtimes(abs, later, later) }, Unchanged},
		{"deleted", func(t *testing.T, abs string) { os.Remove(abs) }, Removed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRoot(t)
			db := mustOpen(t, root)
			defer db.Close()
			abs := writeFile(t, root, "a.txt", "old text\n")
			if _, err := db.IndexFile(ctx, "a.txt", abs, 0); err != nil {
				t.Fatal(err)
			}
			tt.change(t, abs)
			got, err := db.Refresh(ctx, root, []string{"a.txt", "a.txt", "not/indexed.txt"}, 0)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tt.op == Unchanged && len(got) != 0:
				t.Fatalf("got %+v, want no updates", got)
			case tt.op != Unchanged && (len(got) != 1 || got[0].Op != tt.op):
				t.Fatalf("got %+v, want one %v", got, tt.op)
			}
			if tt.name == "touched only" {
				fi, _, _ := db.File(ctx, "a.txt")
				if !fi.ModTime.Equal(later) {
					t.Fatalf("mtime not refreshed: %v", fi.ModTime)
				}
			}
		})
	}
}

func TestWatched(t *testing.T) {
	ctx := context.Background()
	if Watched(ctx) || !Watched(WithWatched(ctx)) {
		t.Fatal("Watched marker wrong")
	}
}
