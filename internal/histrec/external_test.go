package histrec

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/astralyx/lino/internal/changelog"
	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/linediff"
	"github.com/astralyx/lino/internal/live"
	"github.com/astralyx/lino/internal/registry"
	"github.com/astralyx/lino/internal/textfile"
)

func TestBuildExternal(t *testing.T) {
	tests := []struct {
		name  string
		u     index.Update
		cur   string
		curOK bool
		extra history.Extra
		frags []linediff.Fragment
	}{
		{"modified", index.Update{Op: index.Modified, Old: "a\nb\nc\n"}, "a\nB\nc\nd\n", true,
			history.Extra{}, []linediff.Fragment{{Pos: 1, Old: []string{"b"}, New: []string{"B"}}, {Pos: 3, New: []string{"d"}}}},
		{"modified content lost", index.Update{Op: index.Modified, Old: "a\n"}, "", false,
			history.Extra{Binary: true}, nil},
		{"modified to binary", index.Update{Op: index.Modified, Old: "a\n", Binary: true}, "", false,
			history.Extra{Binary: true}, nil},
		{"modified from binary", index.Update{Op: index.Modified, OldBinary: true}, "a\n", true,
			history.Extra{Binary: true}, nil},
		{"added", index.Update{Op: index.Added}, "x\ny", true,
			history.Extra{Created: true}, []linediff.Fragment{{Pos: 0, New: []string{"x", "y"}}}},
		{"added binary", index.Update{Op: index.Added, Binary: true}, "", false,
			history.Extra{Created: true, Binary: true}, nil},
		{"removed", index.Update{Op: index.Removed, Old: "p\r\nq"}, "", false,
			history.Extra{Removed: true, CRLF: true, NoFinalNewline: true}, []linediff.Fragment{{Pos: 0, Old: []string{"p", "q"}}}},
		{"removed binary", index.Update{Op: index.Removed, Binary: true, OldBinary: true}, "", false,
			history.Extra{Removed: true, Binary: true}, nil},
		{"moved", index.Update{Op: index.Moved, From: "old.txt"}, "", false,
			history.Extra{From: "old.txt"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := changelog.Entry{Path: "f", VBefore: "aaaaaa", VAfter: "bbbbbb"}
			ch := BuildExternal(e, tt.u, []byte(tt.cur), tt.curOK)
			if ch.Op != history.OpExternal || ch.Source != history.SourceExternal || ch.Path != "f" ||
				ch.VBefore != "aaaaaa" || ch.VAfter != "bbbbbb" || ch.Extra != tt.extra {
				t.Fatalf("change %+v", ch)
			}
			if !reflect.DeepEqual(norm(ch.Fragments), norm(tt.frags)) {
				t.Fatalf("fragments\n got %+v\nwant %+v", ch.Fragments, tt.frags)
			}
		})
	}
}

// TestExternalWhileDown edits files while no process runs; the startup
// reconcile records one external change per file, with net fragments and the
// same id as its change log entry.
func TestExternalWhileDown(t *testing.T) {
	root := initRoot(t, map[string]string{
		"mod.txt":  "one\ntwo\nthree\n",
		"gone.txt": "g1\ng2\n",
		"mv.txt":   "moving content\n",
		"b.bin":    "a\x00b",
	})
	write := func(rel, s string) {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("mod.txt", "one\nTWO\nthree\nfour\n")
	write("new.txt", "fresh\n")
	write("b.bin", "c\x00d")
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "mv.txt"), filepath.Join(root, "moved.txt")); err != nil {
		t.Fatal(err)
	}

	regDir, err := os.MkdirTemp("", "lh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(regDir) })
	ctx := context.Background()
	p, err := live.Start(ctx, live.Options{Dir: root, Registry: &registry.Registry{Dir: filepath.Join(regDir, "run")}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop(ctx)

	st, err := Store(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := st.List(ctx, history.Filter{Asc: true})
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]history.Change{}
	for _, c := range cs {
		if c.Source != history.SourceExternal || c.Op != history.OpExternal {
			t.Errorf("change %+v", c)
		}
		if _, dup := byPath[c.Path]; dup {
			t.Errorf("two changes for %s", c.Path)
		}
		full, err := st.Get(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		byPath[c.Path] = full
	}
	want := map[string]struct {
		extra history.Extra
		frags []linediff.Fragment
	}{
		"mod.txt": {history.Extra{}, []linediff.Fragment{
			{Pos: 1, Old: []string{"two"}, New: []string{"TWO"}}, {Pos: 3, New: []string{"four"}}}},
		"new.txt":   {history.Extra{Created: true}, []linediff.Fragment{{Pos: 0, New: []string{"fresh"}}}},
		"gone.txt":  {history.Extra{Removed: true}, []linediff.Fragment{{Pos: 0, Old: []string{"g1", "g2"}}}},
		"moved.txt": {history.Extra{From: "mv.txt"}, nil},
		"b.bin":     {history.Extra{Binary: true}, nil},
	}
	if len(byPath) != len(want) {
		t.Fatalf("recorded %d changes, want %d: %+v", len(byPath), len(want), cs)
	}
	for path, w := range want {
		c, ok := byPath[path]
		if !ok {
			t.Errorf("no change for %s", path)
			continue
		}
		if c.Extra != w.extra || !reflect.DeepEqual(norm(c.Fragments), norm(w.frags)) {
			t.Errorf("%s: extra %+v fragments %+v", path, c.Extra, c.Fragments)
		}
	}

	// Net fragments turn the old content into the current one.
	cur, _ := os.ReadFile(filepath.Join(root, "mod.txt"))
	got, err := linediff.Apply([]string{"one", "two", "three"}, byPath["mod.txt"].Fragments)
	if err != nil || !reflect.DeepEqual(got, textfile.Parse(cur).Lines) {
		t.Errorf("apply: %v %q", err, got)
	}

	// History ids are the change log sequence numbers.
	l, err := changelog.Open(ctx, p.DB)
	if err != nil {
		t.Fatal(err)
	}
	es, err := l.Since(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		c, ok := byPath[e.Path]
		if !ok || c.ID != e.Seq || c.VBefore != e.VBefore || c.VAfter != e.VAfter {
			t.Errorf("changelog %+v vs history %+v", e, c)
		}
	}
}
