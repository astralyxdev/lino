package changelog

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/initcmd"
	"github.com/astralyx/lino/internal/live"
	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/registry"
	"github.com/astralyx/lino/internal/reindex"
	"github.com/astralyx/lino/internal/version"
)

// tempDir is short: unix socket paths are limited to ~104 bytes.
func tempDir(t *testing.T, prefix string) string {
	t.Helper()
	d, err := os.MkdirTemp("", prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	d, err = filepath.EvalSymlinks(d)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func initRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := tempDir(t, "lc")
	for p, s := range files {
		abs := filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(abs), 0o755)
		if err := os.WriteFile(abs, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := initcmd.Init(context.Background(), root, root); err != nil {
		t.Fatal(err)
	}
	return root
}

func openLog(t *testing.T, root string) (*Log, *index.DB) {
	t.Helper()
	db, err := index.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	l, err := Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return l, db
}

func entries(t *testing.T, root string, after int64) []Entry {
	t.Helper()
	l, _ := openLog(t, root)
	es, err := l.Since(context.Background(), after, 0)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func ver(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return version.Of(b)
}

func TestOwnEdits(t *testing.T) {
	ctx := context.Background()
	src := "l1\nl2\nl3\nl4\nl5\n"
	tests := []struct {
		name string
		run  func(t *testing.T, root string) error
		want Entry
	}{
		{"edit", func(t *testing.T, root string) error {
			_, err := filecmd.Edit(ctx, root, filecmd.EditRequest{Path: "a.txt", Start: "2", End: "3", V: ver(t, root, "a.txt"), Lines: []string{"x"}, By: "agent-1"})
			return err
		}, Entry{Source: SourceLino, Author: "agent-1", Op: "edit", Path: "a.txt", Kind: Modified, Ranges: []Range{{2, 2}}}},
		{"insert", func(t *testing.T, root string) error {
			_, err := filecmd.Insert(ctx, root, filecmd.InsertRequest{Path: "a.txt", Where: mutate.AtEnd, V: ver(t, root, "a.txt"), Stdin: []byte("n1\nn2\n")})
			return err
		}, Entry{Source: SourceLino, Op: "insert", Path: "a.txt", Kind: Modified, Ranges: []Range{{6, 7}}}},
		{"delete", func(t *testing.T, root string) error {
			_, err := filecmd.Delete(ctx, root, filecmd.DeleteRequest{Path: "a.txt", Start: "2", End: "3", V: ver(t, root, "a.txt")})
			return err
		}, Entry{Source: SourceLino, Op: "delete", Path: "a.txt", Kind: Modified, Ranges: []Range{{2, 1}}}},
		{"write new", func(t *testing.T, root string) error {
			_, err := filecmd.Write(ctx, root, filecmd.WriteRequest{Path: "sub/new.txt", Content: []byte("a\nb\n")})
			return err
		}, Entry{Source: SourceLino, Op: "write", Path: "sub/new.txt", Kind: Added, Ranges: []Range{{1, 2}}}},
		{"write over", func(t *testing.T, root string) error {
			_, err := filecmd.Write(ctx, root, filecmd.WriteRequest{Path: "a.txt", Force: true, Content: []byte("l1\nl2\nX\nl4\nl5\nl6\n")})
			return err
		}, Entry{Source: SourceLino, Op: "write", Path: "a.txt", Kind: Modified, Ranges: []Range{{3, 3}, {6, 6}}}},
		{"mv", func(t *testing.T, root string) error {
			_, err := filecmd.Mv(ctx, root, filecmd.MvRequest{From: "a.txt", To: "b/a.txt"})
			return err
		}, Entry{Source: SourceLino, Op: "mv", Path: "b/a.txt", From: "a.txt", Kind: Moved}},
		{"rm", func(t *testing.T, root string) error {
			_, err := filecmd.Rm(ctx, root, filecmd.RmRequest{Path: "a.txt", V: ver(t, root, "a.txt")})
			return err
		}, Entry{Source: SourceLino, Op: "rm", Path: "a.txt", Kind: Removed}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := initRoot(t, map[string]string{"a.txt": src})
			before := ver(t, root, "a.txt")
			if err := tt.run(t, root); err != nil {
				t.Fatal(err)
			}
			es := entries(t, root, 0)
			if len(es) != 1 {
				t.Fatalf("entries %+v, want exactly 1", es)
			}
			got := es[0]
			if got.Seq != 1 || time.Since(got.Time) > time.Minute {
				t.Fatalf("seq/time %d %v", got.Seq, got.Time)
			}
			switch got.Kind {
			case Added:
				if got.VBefore != "" || got.VAfter == "" {
					t.Fatalf("versions %q→%q", got.VBefore, got.VAfter)
				}
			case Removed:
				if got.VBefore != before || got.VAfter != "" {
					t.Fatalf("versions %q→%q", got.VBefore, got.VAfter)
				}
			default:
				if got.VBefore != before || got.VAfter != ver(t, root, got.Path) {
					t.Fatalf("versions %q→%q", got.VBefore, got.VAfter)
				}
			}
			got.Seq, got.Time, got.VBefore, got.VAfter = 0, time.Time{}, "", ""
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("entry\n got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestUnchangedWriteNotLogged(t *testing.T) {
	root := initRoot(t, map[string]string{"a.txt": "a\n"})
	if _, err := filecmd.Write(context.Background(), root, filecmd.WriteRequest{Path: "a.txt", Force: true, Content: []byte("a\n")}); err != nil {
		t.Fatal(err)
	}
	if es := entries(t, root, 0); len(es) != 0 {
		t.Fatalf("entries %+v", es)
	}
}

func startLive(t *testing.T, root, regDir string) *live.Process {
	t.Helper()
	p, err := live.Start(context.Background(), live.Options{Dir: root, Registry: &registry.Registry{Dir: regDir}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopLive(t, p) })
	return p
}

func stopLive(t *testing.T, p *live.Process) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Error(err)
	}
	reindex.Open = reindex.Direct
}

func TestExternalEdits(t *testing.T) {
	root := initRoot(t, map[string]string{"a.txt": "a\n", "gone.txt": "g\n"})
	regDir := filepath.Join(tempDir(t, "lr"), "run")
	p := startLive(t, root, regDir)
	l, err := Open(context.Background(), p.DB)
	if err != nil {
		t.Fatal(err)
	}
	if es, _ := l.Since(context.Background(), 0, 0); len(es) != 0 {
		t.Fatalf("startup after init logged %+v", es)
	}

	wake := l.Changed()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
	case <-time.After(2 * time.Second):
		t.Fatal("no change log notification within 2s")
	}
	es, err := l.Since(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := Entry{Seq: 1, Source: SourceExternal, Op: "external", Path: "a.txt", Kind: Modified,
		VBefore: version.Of([]byte("a\n")), VAfter: version.Of([]byte("changed\n")), Ranges: []Range{{1, 1}}}
	if len(es) != 1 {
		t.Fatalf("entries %+v, want exactly 1", es)
	}
	es[0].Time = time.Time{}
	if !reflect.DeepEqual(es[0], want) {
		t.Fatalf("entry\n got %+v\nwant %+v", es[0], want)
	}

	// Own edit through the live process's index: one lino entry, no echo from the watcher.
	if _, err := filecmd.Edit(context.Background(), root, filecmd.EditRequest{Path: "a.txt", Start: "1", End: "1", V: ver(t, root, "a.txt"), Lines: []string{"own"}}); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitSynced(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if err := p.WaitSynced(context.Background()); err != nil {
		t.Fatal(err)
	}
	es, _ = l.Since(context.Background(), 1, 0)
	if len(es) != 1 || es[0].Source != SourceLino || es[0].Seq != 2 {
		t.Fatalf("after own edit %+v", es)
	}

	// Edits while no process runs are seen at the next startup.
	stopLive(t, p)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("offline\n"), 0o644)
	os.Remove(filepath.Join(root, "gone.txt"))
	p = startLive(t, root, regDir)
	l, err = Open(context.Background(), p.DB)
	if err != nil {
		t.Fatal(err)
	}
	es, _ = l.Since(context.Background(), 2, 0)
	if len(es) != 2 {
		t.Fatalf("startup entries %+v, want 2", es)
	}
	got := map[string]Kind{}
	for i, e := range es {
		if e.Seq != int64(3+i) || e.Source != SourceExternal {
			t.Fatalf("startup entry %+v", e)
		}
		got[e.Path] = e.Kind
	}
	if !reflect.DeepEqual(got, map[string]Kind{"a.txt": Modified, "gone.txt": Removed}) {
		t.Fatalf("startup kinds %v", got)
	}
}

func TestSeqIncreasingAcrossRestart(t *testing.T) {
	ctx := context.Background()
	root := initRoot(t, nil)
	var last int64
	appendN := func(n int) {
		t.Helper()
		db, err := index.Open(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		l, err := Open(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		for range n {
			seq, err := l.Append(ctx, Entry{Source: SourceLino, Path: "x", Kind: Modified})
			if err != nil {
				t.Fatal(err)
			}
			if seq <= last {
				t.Fatalf("seq %d after %d", seq, last)
			}
			last = seq
		}
		if got, _ := l.Latest(ctx); got != last {
			t.Fatalf("Latest %d, want %d", got, last)
		}
	}
	appendN(3)
	appendN(2)
	if last != 5 {
		t.Fatalf("last %d", last)
	}

	// A rebuilt index continues after the highest id in history.
	h, err := history.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Add(ctx, history.Change{ID: 40, Source: history.SourceLino, Path: "x", Op: history.OpEdit}); err != nil {
		t.Fatal(err)
	}
	h.Close()
	for _, p := range []string{"", "-wal", "-shm"} {
		os.Remove(index.Path(root) + p)
	}
	appendN(1)
	if last != 41 {
		t.Fatalf("after rebuild seq %d, want 41", last)
	}
}

func TestSinceLimit(t *testing.T) {
	ctx := context.Background()
	l, _ := openLog(t, initRoot(t, nil))
	for i := range 5 {
		if _, err := l.Append(ctx, Entry{Source: SourceLino, Path: "p", Kind: Modified, Ranges: []Range{{i + 1, i + 2}}}); err != nil {
			t.Fatal(err)
		}
	}
	es, err := l.Since(ctx, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 || es[0].Seq != 2 || es[1].Seq != 3 || !reflect.DeepEqual(es[1].Ranges, []Range{{3, 4}}) {
		t.Fatalf("since %+v", es)
	}
}

func TestDiffRanges(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want []Range
	}{
		{"same", []string{"a", "b"}, []string{"a", "b"}, nil},
		{"replace middle", []string{"a", "b", "c"}, []string{"a", "X", "c"}, []Range{{2, 2}}},
		{"insert top shifts later", []string{"a", "b", "c", "d"}, []string{"N", "N", "a", "b", "c", "D"}, []Range{{1, 2}, {6, 6}}},
		{"delete", []string{"a", "b", "c"}, []string{"a", "c"}, []Range{{2, 1}}},
		{"from empty", nil, []string{"a"}, []Range{{1, 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DiffRanges(tt.a, tt.b); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
