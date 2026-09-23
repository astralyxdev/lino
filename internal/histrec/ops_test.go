package histrec

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/linediff"
)

func latest(t *testing.T, root string) history.Change {
	t.Helper()
	ctx := context.Background()
	st, err := Store(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := st.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

func TestRecordFileOps(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		stdin string
		args  func(root string) []string
		path  string
		extra history.Extra
		frags []linediff.Fragment
		after bool // VAfter is set
	}{
		{name: "write new", stdin: "x\ny\n",
			args: func(string) []string { return []string{"write", "n/new.txt", "--by", "a1"} },
			path: "n/new.txt", extra: history.Extra{Created: true},
			frags: []linediff.Fragment{{Pos: 0, New: []string{"x", "y"}}}, after: true},
		{name: "rm", files: map[string]string{"old.txt": "p\r\nq\r\n"},
			args: func(root string) []string {
				return []string{"rm", "old.txt", "--v", ver(t, root, "old.txt"), "--by", "a1"}
			},
			path: "old.txt", extra: history.Extra{Removed: true, Mode: 0o640, CRLF: true},
			frags: []linediff.Fragment{{Pos: 0, Old: []string{"p", "q"}}}},
		{name: "rm binary", files: map[string]string{"b.dat": "a\x00b"},
			args: func(string) []string { return []string{"rm", "b.dat", "--force", "--by", "a1"} },
			path: "b.dat", extra: history.Extra{Removed: true, Mode: 0o640, Binary: true}},
		{name: "mv", files: map[string]string{"a.txt": "1\n"},
			args: func(string) []string { return []string{"mv", "a.txt", "d/b.txt", "--by", "a1"} },
			path: "d/b.txt", extra: history.Extra{From: "a.txt"}, after: true},
		{name: "mv binary", files: map[string]string{"a.bin": "\x00"},
			args: func(string) []string { return []string{"mv", "a.bin", "b.bin", "--by", "a1"} },
			path: "b.bin", extra: history.Extra{From: "a.bin"}, after: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := initRoot(t, tt.files)
			for p := range tt.files {
				os.Chmod(filepath.Join(root, p), 0o640)
			}
			lino(t, root, tt.stdin, nil, tt.args(root)...)
			ch := latest(t, root)
			if ch.Path != tt.path || ch.Author != "a1" || ch.Source != history.SourceLino || ch.Extra != tt.extra {
				t.Fatalf("change %+v", ch)
			}
			if (ch.VAfter != "") != tt.after {
				t.Fatalf("VAfter = %q", ch.VAfter)
			}
			if !reflect.DeepEqual(norm(ch.Fragments), norm(tt.frags)) {
				t.Fatalf("fragments\n got %+v\nwant %+v", ch.Fragments, tt.frags)
			}
		})
	}
}
