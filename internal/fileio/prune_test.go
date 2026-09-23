package fileio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPruneEmptyDirs(t *testing.T) {
	tests := []struct {
		name  string
		files []string // created before pruning
		dirs  []string // empty dirs created before pruning
		prune string
		want  []string // dirs that must still exist
		gone  []string // dirs that must be removed
	}{
		{"empty chain", nil, []string{"a/b/c"}, "a/b/c", nil, []string{"a/b/c", "a/b", "a"}},
		{"sibling kept", []string{"a/x.txt"}, []string{"a/b/c"}, "a/b/c", []string{"a"}, []string{"a/b"}},
		{"hidden file kept", []string{"a/b/.keep"}, nil, "a/b", []string{"a/b"}, nil},
		{"root never removed", nil, nil, ".", []string{"."}, nil},
		{"meta dir never removed", nil, []string{".lino/x"}, ".lino/x", []string{".lino/x"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for _, d := range tt.dirs {
				if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, f := range tt.files {
				p := filepath.Join(root, f)
				os.MkdirAll(filepath.Dir(p), 0o755)
				if err := os.WriteFile(p, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			PruneEmptyDirs(root, filepath.Join(root, tt.prune))
			for _, d := range tt.want {
				if _, err := os.Stat(filepath.Join(root, d)); err != nil {
					t.Errorf("%s removed: %v", d, err)
				}
			}
			for _, d := range tt.gone {
				if _, err := os.Stat(filepath.Join(root, d)); err == nil {
					t.Errorf("%s still exists", d)
				}
			}
		})
	}
}
