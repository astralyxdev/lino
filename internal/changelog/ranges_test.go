package changelog

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/index"
)

func TestExternalRanges(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		before string // "" = not indexed
		after  string // "" = removed
		want   []Range
	}{
		{"line changed", "a\nb\nc\n", "a\nB\nc\n", []Range{{2, 2}}},
		{"lines inserted", "a\nc\n", "a\nb1\nb2\nc\n", []Range{{2, 3}}},
		{"line deleted", "a\nb\nc\n", "a\nc\n", []Range{{2, 1}}},
		{"two hunks", "1\n2\n3\n4\n5\n", "one\n2\n3\n4\nfive\n", []Range{{1, 1}, {5, 5}}},
		{"crlf kept", "a\r\nb\r\n", "a\r\nX\r\n", []Range{{2, 2}}},
		{"added", "", "x\ny\n", []Range{{1, 2}}},
		{"added empty", "", "\n", []Range{{1, 1}}},
		{"removed", "gone\n", "", nil},
		{"to binary", "a\n", "a\x00", nil},
		{"from binary", "a\x00", "a\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := tempDir(t, "rr")
			os.Mkdir(filepath.Join(root, ".lino"), 0o755)
			db, err := index.Open(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			abs := filepath.Join(root, "f.txt")
			if tt.before != "" {
				os.WriteFile(abs, []byte(tt.before), 0o644)
				if _, err := db.IndexFile(ctx, "f.txt", abs, 0); err != nil {
					t.Fatal(err)
				}
			}
			if tt.after == "" {
				os.Remove(abs)
			} else {
				os.WriteFile(abs, []byte(tt.after), 0o644)
				os.Chtimes(abs, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
			}
			u, err := db.IndexFile(ctx, "f.txt", abs, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got := ExternalRanges(ctx, db, u); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ranges %v, want %v", got, tt.want)
			}
		})
	}
}
