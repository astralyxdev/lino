package rollback

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/astralyx/lino/internal/changelog"
	"github.com/astralyx/lino/internal/cli"
	_ "github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/histrec"
	"github.com/astralyx/lino/internal/initcmd"
	"github.com/astralyx/lino/internal/live"
	_ "github.com/astralyx/lino/internal/lscmd"
	"github.com/astralyx/lino/internal/registry"
	"github.com/astralyx/lino/internal/reindex"
	"github.com/astralyx/lino/internal/version"
)

const base = "l1\nl2\nl3\nl4\nl5\nl6\n"

type env struct {
	t    *testing.T
	root string
}

func setup(t *testing.T, files map[string]string) *env {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for p, s := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := initcmd.Init(context.Background(), root, root); err != nil {
		t.Fatal(err)
	}
	return &env{t, root}
}

func (e *env) run(stdin string, args ...string) (string, string, int) {
	var o, er bytes.Buffer
	code := cli.Default.Main(context.Background(), args,
		cli.Env{Cwd: e.root, Stdin: strings.NewReader(stdin), Getenv: func(string) string { return "" }}, &o, &er)
	return o.String(), er.String(), code
}

// ok runs a command that must succeed and returns the id of the latest change.
func (e *env) ok(stdin string, args ...string) int64 {
	e.t.Helper()
	out, errOut, code := e.run(stdin, args...)
	if code != 0 {
		e.t.Fatalf("lino %v: exit %d\n%s%s", args, code, out, errOut)
	}
	return e.latest().ID
}

func (e *env) latest() history.Change {
	e.t.Helper()
	st, err := histrec.Store(context.Background(), e.root)
	if err != nil {
		e.t.Fatal(err)
	}
	id, err := st.Latest(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	c, err := st.Get(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *env) v(rel string) string { return version.Of([]byte(e.read(rel))) }

func (e *env) read(rel string) string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.root, rel))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

func (e *env) writeDisk(rel, s string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.root, rel), []byte(s), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func id(n int64) string { return fmt.Sprint(n) }

func TestUndo(t *testing.T) {
	tests := []struct {
		name string
		// steps returns the id to roll back.
		steps func(e *env) int64
		path  string
		want  string
	}{
		{"edit", func(e *env) int64 {
			return e.ok("X\n", "edit", "f.txt", "4", "4", "--v", e.v("f.txt"))
		}, "f.txt", base},
		{"after lines inserted above", func(e *env) int64 {
			a := e.ok("X\nY\n", "edit", "f.txt", "4", "4", "--v", e.v("f.txt"))
			e.ok("n1\nn2\n", "insert", "f.txt", "--at-start", "--v", e.v("f.txt"))
			return a
		}, "f.txt", "n1\nn2\n" + base},
		{"after lines deleted above", func(e *env) int64 {
			a := e.ok("X\n", "edit", "f.txt", "5", "5", "--v", e.v("f.txt"))
			e.ok("", "delete", "f.txt", "1", "2", "--v", e.v("f.txt"))
			return a
		}, "f.txt", "l3\nl4\nl5\nl6\n"},
		{"adjacent edits kept", func(e *env) int64 {
			a := e.ok("X\n", "edit", "f.txt", "3", "3", "--v", e.v("f.txt"))
			e.ok("Y\n", "edit", "f.txt", "4", "4", "--v", e.v("f.txt"))
			e.ok("Z\n", "edit", "f.txt", "2", "2", "--v", e.v("f.txt"))
			return a
		}, "f.txt", "l1\nZ\nl3\nY\nl5\nl6\n"},
		{"insert", func(e *env) int64 {
			return e.ok("i1\ni2\n", "insert", "f.txt", "--after", "2", "--v", e.v("f.txt"))
		}, "f.txt", base},
		{"delete", func(e *env) int64 {
			a := e.ok("", "delete", "f.txt", "2", "3", "--v", e.v("f.txt"))
			e.ok("top\n", "insert", "f.txt", "--at-start", "--v", e.v("f.txt"))
			return a
		}, "f.txt", "top\n" + base},
		{"replace", func(e *env) int64 {
			return e.ok("l5\n<<<lino>>>\nL5\n", "replace", "f.txt", "--v", e.v("f.txt"))
		}, "f.txt", base},
		{"write with several fragments", func(e *env) int64 {
			a := e.ok("l1\nB\nl3\nl4\nl5\nl6\nl7\n", "write", "f.txt", "--v", e.v("f.txt"))
			e.ok("top\n", "insert", "f.txt", "--at-start", "--v", e.v("f.txt"))
			e.ok("M\n", "edit", "f.txt", "5", "5", "--v", e.v("f.txt"))
			return a
		}, "f.txt", "top\nl1\nl2\nl3\nM\nl5\nl6\n"},
		{"external edit", func(e *env) int64 {
			e.writeDisk("f.txt", "l1\nl2\nEXT\nl4\nl5\nl6\n")
			e.ok("", "ls") // stat refresh records the external edit
			e.ok("top\n", "insert", "f.txt", "--at-start", "--v", e.v("f.txt"))
			st, _ := histrec.Store(context.Background(), e.root)
			all, _ := st.List(context.Background(), history.Filter{})
			var cs []history.Change
			for _, c := range all {
				if c.Source == history.SourceExternal {
					cs = append(cs, c)
				}
			}
			if len(cs) != 1 {
				e.t.Fatalf("external changes %+v", all)
			}
			return cs[0].ID
		}, "f.txt", "top\n" + base},
		{"after mv", func(e *env) int64 {
			a := e.ok("X\n", "edit", "f.txt", "2", "2", "--v", e.v("f.txt"))
			e.ok("", "mv", "f.txt", "g.txt")
			e.ok("top\n", "insert", "g.txt", "--at-start", "--v", e.v("g.txt"))
			return a
		}, "g.txt", "top\n" + base},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t, map[string]string{"f.txt": base})
			target := tt.steps(e)
			before := e.v(tt.path)
			out, errOut, code := e.run("", "rollback", id(target), "--by", "agent-9")
			if code != 0 {
				t.Fatalf("exit %d\n%s%s", code, out, errOut)
			}
			if got := e.read(tt.path); got != tt.want {
				t.Fatalf("content\n got %q\nwant %q", got, tt.want)
			}
			c := e.latest()
			if c.Op != history.OpRollback || c.Extra.Undid != target || c.Path != tt.path || c.Author != "agent-9" ||
				c.VBefore != before || c.VAfter != e.v(tt.path) {
				t.Fatalf("recorded %+v", c)
			}
			wantOut := fmt.Sprintf("%d  rollback  %s  undid %d  v=%s→%s\n", c.ID, tt.path, target, before, c.VAfter)
			if out != wantOut {
				t.Fatalf("output %q, want %q", out, wantOut)
			}
		})
	}
}

func TestConflict(t *testing.T) {
	tests := []struct {
		name  string
		steps func(e *env) (target, overlap int64)
	}{
		{"same line edited", func(e *env) (int64, int64) {
			a := e.ok("X\n", "edit", "f.txt", "4", "4", "--v", e.v("f.txt"))
			return a, e.ok("Y\n", "edit", "f.txt", "4", "4", "--v", e.v("f.txt"))
		}},
		{"insert inside range", func(e *env) (int64, int64) {
			a := e.ok("X\nY\n", "edit", "f.txt", "3", "3", "--v", e.v("f.txt"))
			return a, e.ok("mid\n", "insert", "f.txt", "--after", "3", "--v", e.v("f.txt"))
		}},
		{"deleted lines re-added at the spot", func(e *env) (int64, int64) {
			a := e.ok("", "delete", "f.txt", "3", "4", "--v", e.v("f.txt"))
			return a, e.ok("new\n", "insert", "f.txt", "--after", "2", "--v", e.v("f.txt"))
		}},
		{"external edit on the lines", func(e *env) (int64, int64) {
			a := e.ok("X\n", "edit", "f.txt", "4", "4", "--v", e.v("f.txt"))
			e.writeDisk("f.txt", "l1\nl2\nl3\nEXT\nl5\nl6\n")
			return a, a + 1 // recorded by the refresh before mapping
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t, map[string]string{"f.txt": base})
			target, overlap := tt.steps(e)
			before := e.read("f.txt")
			out, errOut, code := e.run("", "rollback", id(target))
			if code != 6 {
				t.Fatalf("exit %d, want 6\n%s%s", code, out, errOut)
			}
			if !strings.Contains(errOut, "same lines: "+id(overlap)) {
				t.Fatalf("stderr does not name %d:\n%s", overlap, errOut)
			}
			if e.read("f.txt") != before {
				t.Fatal("file changed on conflict")
			}
			if c := e.latest(); c.Op == history.OpRollback {
				t.Fatalf("rollback recorded: %+v", c)
			}
		})
	}
}

func TestConflictJSONShowsLines(t *testing.T) {
	e := setup(t, map[string]string{"f.txt": base})
	a := e.ok("X\n", "edit", "f.txt", "4", "4", "--v", e.v("f.txt"))
	e.ok("Y\n", "edit", "f.txt", "4", "4", "--v", e.v("f.txt"))
	out, _, code := e.run("", "rollback", id(a), "--json")
	var env struct {
		Outcome string `json:"outcome"`
		Data    struct {
			Path  string            `json:"path"`
			Lines []json.RawMessage `json:"lines"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || code != 6 {
		t.Fatalf("exit %d: %v\n%s", code, err, out)
	}
	if env.Outcome != "conflict" || env.Data.Path != "f.txt" || len(env.Data.Lines) == 0 || !strings.Contains(out, `"Y"`) {
		t.Fatalf("%s", out)
	}
}

func TestRollbackOfRollback(t *testing.T) {
	e := setup(t, map[string]string{"f.txt": base})
	a := e.ok("X\nY\n", "edit", "f.txt", "2", "3", "--v", e.v("f.txt"))
	edited := e.read("f.txt")
	r1 := e.ok("", "rollback", id(a))
	if e.read("f.txt") != base {
		t.Fatalf("after first rollback %q", e.read("f.txt"))
	}
	e.ok("top\n", "insert", "f.txt", "--at-start", "--v", e.v("f.txt"))
	r2 := e.ok("", "rollback", id(r1))
	if got := e.read("f.txt"); got != "top\n"+edited {
		t.Fatalf("after rollback of rollback %q", got)
	}
	if c := e.latest(); c.ID != r2 || c.Extra.Undid != r1 {
		t.Fatalf("recorded %+v", c)
	}
	// And once more: undoing the second rollback removes the edit again.
	e.ok("", "rollback", id(r2))
	if got := e.read("f.txt"); got != "top\n"+base {
		t.Fatalf("third rollback %q", got)
	}
	// The original edit is now overlapped by the rollbacks.
	if _, _, code := e.run("", "rollback", id(a)); code != 6 {
		t.Fatalf("rollback of a again: exit %d, want 6", code)
	}
}

func TestErrors(t *testing.T) {
	e := setup(t, map[string]string{"f.txt": base, "b.bin": "\x00\x01\x02"})
	rm := e.ok("", "rm", "b.bin", "--force")
	tests := []struct {
		name string
		args []string
		code int
	}{
		{"unknown id", []string{"99"}, 3},
		{"bad id", []string{"x"}, 2},
		{"zero id", []string{"0"}, 2},
		{"two ids", []string{"1", "2"}, 2},
		{"binary rm refused", []string{id(rm)}, 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errOut, code := e.run("", append([]string{"rollback"}, tt.args...)...)
			if code != tt.code {
				t.Fatalf("exit %d, want %d\n%s%s", code, tt.code, out, errOut)
			}
		})
	}
}

func TestJSON(t *testing.T) {
	e := setup(t, map[string]string{"f.txt": base})
	a := e.ok("X\n", "edit", "f.txt", "1", "1", "--v", e.v("f.txt"))
	out, errOut, code := e.run("", "rollback", id(a), "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	var env struct {
		Outcome string `json:"outcome"`
		Data    Data   `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	want := Data{ID: a + 1, Op: "rollback", Path: "f.txt", Undid: a, OldV: env.Data.OldV, NewV: version.Of([]byte(base))}
	if env.Outcome != "updated" || env.Data != want {
		t.Fatalf("%+v", env)
	}
}

// TestLive rolls back inside a live process: the process's index is used,
// and its watcher does not record the rollback's write a second time.
func TestLive(t *testing.T) {
	tmp, err := os.MkdirTemp("", "rb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	e := setup(t, map[string]string{"f.txt": base})
	ctx := context.Background()
	p, err := live.Start(ctx, live.Options{Dir: e.root, Registry: &registry.Registry{Dir: filepath.Join(tmp, "run")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		p.Stop(ctx)
		reindex.Open = reindex.Direct
	})
	a := e.ok("X\n", "edit", "f.txt", "3", "3", "--v", e.v("f.txt"))
	e.writeDisk("f.txt", "top\n"+e.read("f.txt"))
	r := e.ok("", "rollback", id(a))
	if got := e.read("f.txt"); got != "top\n"+base {
		t.Fatalf("content %q", got)
	}
	time.Sleep(1500 * time.Millisecond)
	if c := e.latest(); c.ID != r || c.Op != history.OpRollback {
		t.Fatalf("latest %+v, want rollback %d", c, r)
	}
	if ext := r - 1; ext == a {
		t.Fatal("external edit not recorded before the rollback")
	}
}
