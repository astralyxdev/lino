package reindex_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/astralyx/lino/internal/changelog"
	"github.com/astralyx/lino/internal/cli"
	_ "github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/histrec"
	"github.com/astralyx/lino/internal/initcmd"
	_ "github.com/astralyx/lino/internal/reindex"
	"github.com/astralyx/lino/internal/version"
)

func TestDirectMutationRecordsPriorExternalEdit(t *testing.T) {
	const base, edited = "a\nb\nc\n", "a\nB\nc\n"
	v := version.Of([]byte(edited))
	tests := []struct {
		name  string
		stdin string
		args  []string
		op    string
	}{
		{"insert", "top\n", []string{"insert", "f.txt", "--at-start", "--v", v}, history.OpInsert},
		{"edit", "X\n", []string{"edit", "f.txt", "3", "3", "--v", v}, history.OpEdit},
		{"delete", "", []string{"delete", "f.txt", "1", "1", "--v", v}, history.OpDelete},
		{"write", "new\n", []string{"write", "f.txt", "--v", v}, history.OpWrite},
		{"mv", "", []string{"mv", "f.txt", "g.txt"}, history.OpMv},
		{"rm", "", []string{"rm", "f.txt", "--v", v}, history.OpRm},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			f := filepath.Join(root, "f.txt")
			if err := os.WriteFile(f, []byte(base), 0o644); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if _, err := initcmd.Init(ctx, root, root); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f, []byte(edited), 0o644); err != nil {
				t.Fatal(err)
			}
			later := time.Now().Add(time.Second)
			os.Chtimes(f, later, later) // mtime must differ from the indexed one

			var out, errOut bytes.Buffer
			code := cli.Default.Main(ctx, tt.args, cli.Env{Cwd: root, Stdin: strings.NewReader(tt.stdin),
				Getenv: func(string) string { return "" }}, &out, &errOut)
			if code != 0 {
				t.Fatalf("exit %d\n%s%s", code, out.String(), errOut.String())
			}

			st, err := histrec.Store(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			all, err := st.List(ctx, history.Filter{Asc: true})
			if err != nil {
				t.Fatal(err)
			}
			var ext, own []history.Change
			for _, c := range all {
				switch {
				case c.Source == history.SourceExternal:
					ext = append(ext, c)
				case c.Op == tt.op:
					own = append(own, c)
				}
			}
			if len(ext) != 1 || len(own) != 1 {
				t.Fatalf("want one external and one %s change, got %+v", tt.op, all)
			}
			if ext[0].ID >= own[0].ID {
				t.Fatalf("external %d not before %s %d", ext[0].ID, tt.op, own[0].ID)
			}
			if ext[0].VAfter != v || own[0].VBefore != v {
				t.Fatalf("versions: external after %q, %s before %q, want %q", ext[0].VAfter, tt.op, own[0].VBefore, v)
			}
			full, err := st.Get(ctx, ext[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(full.Fragments) != 1 || !reflect.DeepEqual(full.Fragments[0].Old, []string{"b"}) ||
				!reflect.DeepEqual(full.Fragments[0].New, []string{"B"}) {
				t.Fatalf("external fragments %+v", full.Fragments)
			}
		})
	}
}
