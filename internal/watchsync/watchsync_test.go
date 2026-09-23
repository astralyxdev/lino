package watchsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/ignore"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/search"
	"github.com/astralyx/lino/internal/watch"
)

type env struct {
	root  string
	db    *index.DB
	rules *ignore.Rules
}

func setup(t *testing.T, files map[string]string) *env {
	t.Helper()
	dir := t.TempDir()
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	files[".lino/config"] = ""
	for p, s := range files {
		writeFile(t, root, p, s)
	}
	rules, err := ignore.NewRules(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, err := index.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Reconcile(ctx, rules, 0); err != nil {
		t.Fatal(err)
	}
	return &env{root: root, db: db, rules: rules}
}

func writeFile(t *testing.T, root, p, s string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// indexed returns path => content (or "<bin>") from the index.
func (e *env) indexed(t *testing.T) map[string]string {
	t.Helper()
	rows, err := e.db.SQL.Query(`SELECT path, content, binary FROM files`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, c string
		var bin bool
		if err := rows.Scan(&p, &c, &bin); err != nil {
			t.Fatal(err)
		}
		out[p] = c
	}
	return out
}

// onDisk walks the root like a full reconcile would.
func (e *env) onDisk(t *testing.T) map[string]string {
	t.Helper()
	rules, _ := ignore.NewRules(e.root)
	out := map[string]string{}
	err := ignore.Walk(e.root, rules, func(en ignore.Entry) error {
		b, err := os.ReadFile(en.Abs)
		out[en.Rel] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func equal(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestFlush(t *testing.T) {
	ev := func(rel string, op watch.Op) watch.Event { return watch.Event{Rel: rel, Op: op} }
	tests := []struct {
		name   string
		change func(t *testing.T, e *env)
		events []watch.Event
		ops    map[string]index.Op
		full   int64
	}{
		{"modify", func(t *testing.T, e *env) { writeFile(t, e.root, "a.txt", "A2\n") },
			[]watch.Event{ev("a.txt", watch.Write)}, map[string]index.Op{"a.txt": index.Modified}, 0},
		{"create in new dir", func(t *testing.T, e *env) { writeFile(t, e.root, "n/new.txt", "new\n") },
			[]watch.Event{{Rel: "n", Op: watch.Create, Dir: true}, ev("n/new.txt", watch.Create)}, map[string]index.Op{"n/new.txt": index.Added}, 0},
		{"delete", func(t *testing.T, e *env) { os.Remove(filepath.Join(e.root, "a.txt")) },
			[]watch.Event{ev("a.txt", watch.Remove)}, map[string]index.Op{"a.txt": index.Removed}, 0},
		{"rename is a move", func(t *testing.T, e *env) {
			os.Rename(filepath.Join(e.root, "a.txt"), filepath.Join(e.root, "b.txt"))
		}, []watch.Event{ev("a.txt", watch.Rename), ev("b.txt", watch.Create)}, map[string]index.Op{"b.txt": index.Moved}, 0},
		{"dir removed", func(t *testing.T, e *env) { os.RemoveAll(filepath.Join(e.root, "d")) },
			[]watch.Event{{Rel: "d", Op: watch.Remove}}, map[string]index.Op{"d/x.txt": index.Removed, "d/e/y.txt": index.Removed}, 0},
		{"unchanged write", func(t *testing.T, e *env) { writeFile(t, e.root, "a.txt", "A\n") },
			[]watch.Event{ev("a.txt", watch.Write)}, map[string]index.Op{}, 0},
		{"ignored path", func(t *testing.T, e *env) { writeFile(t, e.root, "skip.log", "x") },
			[]watch.Event{ev("skip.log", watch.Create)}, map[string]index.Op{}, 0},
		{"overflow => full reconcile", func(t *testing.T, e *env) {
			writeFile(t, e.root, "a.txt", "silent\n")
			os.Remove(filepath.Join(e.root, "d/x.txt"))
		}, []watch.Event{{Rescan: true, Reason: "event queue overflow"}},
			map[string]index.Op{"a.txt": index.Modified, "d/x.txt": index.Removed}, 1},
		{"bulk => full reconcile", func(t *testing.T, e *env) {
			writeFile(t, e.root, "a.txt", "bulk\n")
		}, []watch.Event{ev("a.txt", watch.Write), ev("x1", watch.Write), ev("x2", watch.Write), ev("x3", watch.Write)},
			map[string]index.Op{"a.txt": index.Modified}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t, map[string]string{
				".gitignore": "*.log\n", "a.txt": "A\n", "d/x.txt": "x\n", "d/e/y.txt": "y\n",
			})
			var got []index.Update
			s := New(e.db, e.rules, Options{Bulk: 3, OnUpdate: func(_ context.Context, u []index.Update) { got = append(got, u...) }})
			tt.change(t, e)
			for _, ev := range tt.events {
				s.Add(ev)
			}
			if s.Pending() == 0 {
				t.Fatal("pending is 0 before flush")
			}
			s.Flush(context.Background())
			ops := map[string]index.Op{}
			for _, u := range got {
				ops[u.Path] = u.Op
			}
			if len(ops) != len(tt.ops) {
				t.Fatalf("updates %+v, want %v", got, tt.ops)
			}
			for p, op := range tt.ops {
				if ops[p] != op {
					t.Fatalf("%s: op %v, want %v (all %+v)", p, ops[p], op, got)
				}
			}
			st := s.Stats()
			if st.Pending != 0 || st.Full != tt.full || st.LastError != "" {
				t.Fatalf("stats %+v", st)
			}
			if in, disk := e.indexed(t), e.onDisk(t); !equal(in, disk) {
				t.Fatalf("index %v != disk %v", keys(in), keys(disk))
			}
			select {
			case <-s.Idle():
			default:
				t.Fatal("not idle after flush")
			}
		})
	}
}

func TestRunDebounce(t *testing.T) {
	e := setup(t, map[string]string{"a.txt": "A\n"})
	var mu sync.Mutex
	batches := 0
	s := New(e.db, e.rules, Options{Quiet: 50 * time.Millisecond, MaxDelay: 300 * time.Millisecond,
		OnUpdate: func(context.Context, []index.Update) { mu.Lock(); batches++; mu.Unlock() }})
	ch := make(chan watch.Event)
	done := make(chan struct{})
	go func() { s.Run(context.Background(), ch); close(done) }()
	for i := 0; i < 5; i++ {
		writeFile(t, e.root, "a.txt", strings.Repeat("x", i+1))
		ch <- watch.Event{Rel: "a.txt", Op: watch.Write}
		time.Sleep(10 * time.Millisecond)
	}
	<-s.Idle()
	deadline := time.After(2 * time.Second)
	for s.Stats().Batches == 0 {
		select {
		case <-deadline:
			t.Fatal("no batch")
		case <-time.After(5 * time.Millisecond):
		}
	}
	close(ch)
	<-done
	mu.Lock()
	defer mu.Unlock()
	if batches != 1 || s.Stats().Batches != 1 {
		t.Fatalf("batches %d (stats %d), want 1 debounced batch", batches, s.Stats().Batches)
	}
	if got := e.indexed(t)["a.txt"]; got != "xxxxx" {
		t.Fatalf("content %q", got)
	}
}

// live starts a real watcher feeding a syncer.
func live(t *testing.T, e *env) *Syncer {
	t.Helper()
	w, err := watch.New(e.rules)
	if err != nil {
		t.Fatal(err)
	}
	s := New(e.db, e.rules, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx, w.Events()); close(done) }()
	t.Cleanup(func() { w.Close(); cancel(); <-done })
	return s
}

func TestExternalWriteVisibleToSearch(t *testing.T) {
	e := setup(t, map[string]string{"src/a.go": "package a\n"})
	live(t, e)
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	writeFile(t, e.root, "src/a.go", "package a\n\nconst Needle = 1\n")
	writeFile(t, e.root, "src/new/b.go", "var needleTwo = Needle\n")
	for {
		r, err := search.Literal(context.Background(), e.db, "Needle", search.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Hits) == 2 {
			t.Logf("visible after %v", time.Since(start))
			return
		}
		if time.Since(start) > time.Second {
			t.Fatalf("not visible within 1s: %+v", r.Hits)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGitCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	e := setup(t, map[string]string{
		".gitignore": ".lino/\n", "main.go": "package main\n", "lib/a.go": "package lib\n// A\n", "lib/old.go": "old\n",
	})
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = e.root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "one")
	git("checkout", "-q", "-b", "other")
	os.Remove(filepath.Join(e.root, "lib/old.go"))
	os.RemoveAll(filepath.Join(e.root, "lib"))
	writeFile(t, e.root, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, e.root, "pkg/b/b.go", "package b\n")
	writeFile(t, e.root, "moved.go", "package lib\n// A\n")
	git("add", "-A")
	git("commit", "-q", "-m", "two")
	git("checkout", "-q", "main")
	if _, err := e.db.Reconcile(context.Background(), e.rules, 0); err != nil {
		t.Fatal(err)
	}

	s := live(t, e)
	time.Sleep(100 * time.Millisecond)
	git("checkout", "-q", "other")
	want := e.onDisk(t)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if s.Pending() == 0 && equal(e.indexed(t), want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("index %v != disk %v (stats %+v)", keys(e.indexed(t)), keys(want), s.Stats())
		}
		time.Sleep(20 * time.Millisecond)
	}
	git("checkout", "-q", "main")
	want = e.onDisk(t)
	deadline = time.Now().Add(3 * time.Second)
	for !(s.Pending() == 0 && equal(e.indexed(t), want)) {
		if time.Now().After(deadline) {
			t.Fatalf("back on main: index %v != disk %v", keys(e.indexed(t)), keys(want))
		}
		time.Sleep(20 * time.Millisecond)
	}
}
