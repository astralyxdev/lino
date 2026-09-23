package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/ignore"
)

func start(t *testing.T, files map[string]string) (string, *Watcher) {
	t.Helper()
	root := t.TempDir()
	for p, s := range files {
		write(t, root, p, s)
	}
	rules, err := ignore.NewRules(root)
	if err != nil {
		t.Fatal(err)
	}
	w, err := New(rules)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return rules.Root(), w
}

func write(t *testing.T, root, rel, s string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitFor collects events until one satisfies ok, failing after a timeout.
// It returns every event seen, the matching one last.
func waitFor(t *testing.T, w *Watcher, what string, ok func(Event) bool) []Event {
	t.Helper()
	var seen []Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e := <-w.Events():
			seen = append(seen, e)
			if ok(e) {
				return seen
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s; saw %v", what, seen)
		}
	}
}

func is(rel string, op Op) func(Event) bool {
	return func(e Event) bool { return e.Rel == rel && e.Op&op != 0 }
}

func TestEvents(t *testing.T) {
	root, w := start(t, map[string]string{"keep.txt": "k\n", "old/dir/f.txt": "f\n"})

	type want struct {
		rel string
		op  Op
	}
	tests := []struct {
		name string
		do   func()
		want []want // all must arrive, in any order
	}{
		{"create", func() { write(t, root, "a.txt", "a\n") }, []want{{"a.txt", Create}}},
		{"modify", func() { write(t, root, "keep.txt", "k2\n") }, []want{{"keep.txt", Write}}},
		{"delete", func() { os.Remove(filepath.Join(root, "a.txt")) }, []want{{"a.txt", Remove}}},
		{"create in nested new dir", func() { write(t, root, "n1/n2/n3.txt", "x\n") },
			[]want{{"n1", Create}, {"n1/n2/n3.txt", Create}}},
		{"modify in nested new dir", func() {
			time.Sleep(50 * time.Millisecond)
			write(t, root, "n1/n2/n3.txt", "y\n")
		}, []want{{"n1/n2/n3.txt", Write}}},
		{"rename in nested new dir", func() {
			os.Rename(filepath.Join(root, "n1/n2/n3.txt"), filepath.Join(root, "n1/n2/moved.txt"))
		}, []want{{"n1/n2/n3.txt", Rename | Remove}, {"n1/n2/moved.txt", Create}}},
		{"delete in nested new dir", func() { os.Remove(filepath.Join(root, "n1/n2/moved.txt")) },
			[]want{{"n1/n2/moved.txt", Remove}}},
		{"modify in existing nested dir", func() { write(t, root, "old/dir/f.txt", "g\n") },
			[]want{{"old/dir/f.txt", Write}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.do()
			pending := append([]want(nil), tt.want...)
			waitFor(t, w, tt.name, func(e Event) bool {
				for i, x := range pending {
					if e.Rel == x.rel && e.Op&x.op != 0 {
						pending = append(pending[:i], pending[i+1:]...)
						break
					}
				}
				return len(pending) == 0
			})
		})
	}
}

func TestRemovedDirIsDropped(t *testing.T) {
	root, w := start(t, map[string]string{"d/e/f.txt": "f\n"})
	if got := w.Stats().Dirs; got != 3 {
		t.Fatalf("dirs = %d, want 3 (root, d, d/e)", got)
	}
	if err := os.RemoveAll(filepath.Join(root, "d")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, w, "remove d", func(e Event) bool { return e.Rel == "d" && e.Op&(Remove|Rename) != 0 })
	if got := w.Stats().Dirs; got != 1 {
		t.Fatalf("dirs after remove = %d, want 1", got)
	}

	write(t, root, "d/again.txt", "x\n")
	waitFor(t, w, "recreated dir file", is("d/again.txt", Create))
}

func TestIgnoredPathsProduceNoEvents(t *testing.T) {
	root, w := start(t, map[string]string{
		".gitignore":     "*.log\nbuild/\n",
		".linoignore":    "secret/\n",
		".lino/index.db": "",
		"build/old.txt":  "",
	})
	st := w.Stats()
	if st.Dirs != 1 {
		t.Errorf("dirs = %d, want only the root (build/ and .lino/ ignored)", st.Dirs)
	}

	write(t, root, "app.log", "x\n")
	write(t, root, "build/out.txt", "x\n")
	write(t, root, "build/new/deep.txt", "x\n")
	write(t, root, "secret/key.txt", "x\n")
	write(t, root, ".lino/index.db", "changed")
	write(t, root, ".lino/history.db", "new")
	time.Sleep(100 * time.Millisecond)
	write(t, root, "sentinel.txt", "s\n")

	seen := waitFor(t, w, "sentinel", is("sentinel.txt", Create))
	for _, e := range seen {
		if e.Rel != "sentinel.txt" {
			t.Errorf("unexpected event %+v", e)
		}
	}
}

func TestIgnoreFileChangeRescans(t *testing.T) {
	root, w := start(t, map[string]string{".gitignore": "gen/\n", "gen/a.txt": "a\n"})
	write(t, root, ".gitignore", "")
	waitFor(t, w, "rescan", func(e Event) bool { return e.Rescan })
	if got := w.Stats().Dirs; got != 2 {
		t.Fatalf("dirs = %d, want 2 after gen/ is no longer ignored", got)
	}
	write(t, root, "gen/a.txt", "b\n")
	waitFor(t, w, "write in re-included dir", is("gen/a.txt", Write))
}

func TestCloseClosesEvents(t *testing.T) {
	_, w := start(t, nil)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-w.Events(); ok {
		t.Fatal("events channel still open")
	}
	if err := w.Close(); err != nil {
		t.Fatal("second Close:", err)
	}
}

func TestStats(t *testing.T) {
	_, w := start(t, nil)
	st := w.Stats()
	if st.Backend == "" || st.Dirs != 1 || st.Failed != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestOpString(t *testing.T) {
	tests := []struct {
		op   Op
		want string
	}{
		{0, ""},
		{Create, "create"},
		{Remove | Rename, "remove|rename"},
		{Create | Write | Chmod, "create|write|chmod"},
	}
	for _, tt := range tests {
		if got := tt.op.String(); got != tt.want {
			t.Errorf("%d.String() = %q, want %q", tt.op, got, tt.want)
		}
	}
}
