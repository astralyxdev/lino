package search

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/output"
)

func TestDirectFreshness(t *testing.T) {
	later := time.Now().Add(time.Hour)
	tests := []struct {
		name    string
		change  func(root string)
		watched bool
		query   string
		want    []string // substrings of the text output
		notWant []string
	}{
		{
			name: "external edit shows fresh text",
			change: func(root string) {
				p := filepath.Join(root, "a.go")
				os.WriteFile(p, []byte("// marker renamed to NewName\n"), 0o644)
				os.Chtimes(p, later, later)
			},
			query:   "marker",
			want:    []string{"NewName"},
			notWant: []string{"OldName"},
		},
		{
			name: "stale hit is dropped",
			change: func(root string) {
				p := filepath.Join(root, "a.go")
				os.WriteFile(p, []byte("// nothing here\n"), 0o644)
				os.Chtimes(p, later, later)
			},
			query:   "marker",
			want:    []string{"b.go"},
			notWant: []string{"a.go"},
		},
		{
			name:    "deleted file disappears",
			change:  func(root string) { os.Remove(filepath.Join(root, "a.go")) },
			query:   "marker",
			want:    []string{"b.go"},
			notWant: []string{"a.go"},
		},
		{
			name:    "watched process trusts the index",
			change:  func(root string) { os.Remove(filepath.Join(root, "a.go")) },
			watched: true,
			query:   "marker",
			want:    []string{"a.go", "b.go"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRoot(t, map[string]string{
				".lino/config": "",
				"a.go":         "// marker OldName\n",
				"b.go":         "// marker too\n",
			})
			ctx := context.Background()
			if _, err := Search(ctx, root, Request{Query: tt.query}); err != nil {
				t.Fatal(err)
			}
			tt.change(root)
			if tt.watched {
				ctx = index.WithWatched(ctx)
			}
			res, err := Search(ctx, root, Request{Query: tt.query})
			if err != nil {
				t.Fatal(err)
			}
			var sb strings.Builder
			(&output.Printer{Stdout: &sb, Stderr: &sb}).Result(res)
			out := sb.String()
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Errorf("missing %q in:\n%s", w, out)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(out, w) {
					t.Errorf("unexpected %q in:\n%s", w, out)
				}
			}
		})
	}
}
