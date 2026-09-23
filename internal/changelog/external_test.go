package changelog

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/cli"
	_ "github.com/astralyx/lino/internal/search"
	_ "github.com/astralyx/lino/internal/statuscmd"
)

func TestExternalFoundOutsideWatcher(t *testing.T) {
	tests := []struct {
		name   string
		edit   func(root string) error
		args   []string
		kind   Kind
		ranges []Range
	}{
		{"direct search refresh", func(root string) error {
			return os.WriteFile(filepath.Join(root, "a.txt"), []byte("one needle\nand more\n"), 0o644)
		}, []string{"search", "needle", "--direct"}, Modified, []Range{{1, 2}}},
		{"direct search removed file", func(root string) error {
			return os.Remove(filepath.Join(root, "a.txt"))
		}, []string{"search", "needle", "--direct"}, Removed, nil},
		{"lino index modify", func(root string) error {
			return os.WriteFile(filepath.Join(root, "a.txt"), []byte("changed by an IDE\n"), 0o644)
		}, []string{"index", "--direct"}, Modified, []Range{{1, 1}}},
		{"lino index new file", func(root string) error {
			return os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o644)
		}, []string{"index", "--direct"}, Added, []Range{{1, 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", tempDir(t, "lh"))
			root := initRoot(t, map[string]string{"a.txt": "one\n"})
			before := len(entries(t, root, 0))
			if err := tt.edit(root); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ { // the second call must not log again
				var out, errOut bytes.Buffer
				code := cli.Default.Main(context.Background(), tt.args, cli.Env{Cwd: root, Stdin: strings.NewReader("")}, &out, &errOut)
				if code != 0 {
					t.Fatalf("%v: exit %d: %s%s", tt.args, code, out.String(), errOut.String())
				}
			}
			es := entries(t, root, 0)[before:]
			if len(es) != 1 {
				t.Fatalf("want 1 new entry, got %+v", es)
			}
			if es[0].Source != SourceExternal || es[0].Kind != tt.kind || !reflect.DeepEqual(es[0].Ranges, tt.ranges) {
				t.Fatalf("entry %+v", es[0])
			}
		})
	}
}
