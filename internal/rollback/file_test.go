package rollback

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/astralyx/lino/internal/statuscmd"
)

const absent = "\x00absent"

func (e *env) remove(rel string) {
	e.t.Helper()
	if err := os.Remove(filepath.Join(e.root, rel)); err != nil {
		e.t.Fatal(err)
	}
}

func TestUndoFile(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		// steps returns the id to roll back.
		steps func(e *env) int64
		want  map[string]string // path => content, or absent
		text  string            // substring of the rollback output
	}{
		{"rm restores the file", nil, func(e *env) int64 {
			return e.ok("", "rm", "f.txt", "--v", e.v("f.txt"))
		}, map[string]string{"f.txt": base}, "restored v="},
		{"rm keeps line style", map[string]string{"c.txt": "\ufeffa\r\nb\r\nc"}, func(e *env) int64 {
			return e.ok("", "rm", "c.txt", "--force")
		}, map[string]string{"c.txt": "\ufeffa\r\nb\r\nc"}, "restored"},
		{"rm of empty file", map[string]string{"e.txt": ""}, func(e *env) int64 {
			return e.ok("", "rm", "e.txt", "--force")
		}, map[string]string{"e.txt": ""}, "restored"},
		{"rm in removed directory", map[string]string{"d/x.txt": "x\n"}, func(e *env) int64 {
			id := e.ok("", "rm", "d/x.txt", "--force")
			os.Remove(filepath.Join(e.root, "d"))
			return id
		}, map[string]string{"d/x.txt": "x\n"}, "restored"},
		{"new file removed", nil, func(e *env) int64 {
			return e.ok("n1\nn2\n", "write", "n.txt")
		}, map[string]string{"n.txt": absent, "f.txt": base}, "removed v="},
		{"new file removed after a move", nil, func(e *env) int64 {
			a := e.ok("n1\n", "write", "n.txt")
			e.ok("", "mv", "n.txt", "m.txt")
			return a
		}, map[string]string{"n.txt": absent, "m.txt": absent}, "removed"},
		{"mv moved back", nil, func(e *env) int64 {
			return e.ok("", "mv", "f.txt", "sub/g.txt")
		}, map[string]string{"f.txt": base, "sub/g.txt": absent}, "moved back from sub/g.txt"},
		{"mv of a binary file", map[string]string{"b.bin": "\x00\x01"}, func(e *env) int64 {
			return e.ok("", "mv", "b.bin", "c.bin")
		}, map[string]string{"b.bin": "\x00\x01", "c.bin": absent}, "moved back"},
		{"mv after an edit before it", nil, func(e *env) int64 {
			e.ok("X\n", "edit", "f.txt", "1", "1", "--v", e.v("f.txt"))
			return e.ok("", "mv", "f.txt", "g.txt")
		}, map[string]string{"f.txt": "X\n" + base[3:], "g.txt": absent}, "moved back"},
		{"external removal", nil, func(e *env) int64 {
			e.remove("f.txt")
			return e.ok("", "ls")
		}, map[string]string{"f.txt": base}, "restored"},
		{"external new file", nil, func(e *env) int64 {
			e.writeDisk("x.txt", "ext\n")
			return e.ok("", "index")
		}, map[string]string{"x.txt": absent}, "removed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"f.txt": base}
			for p, s := range tt.files {
				files[p] = s
			}
			e := setup(t, files)
			n := tt.steps(e)
			out, errOut, code := e.run("", "rollback", id(n))
			if code != 0 {
				t.Fatalf("exit %d\n%s%s", code, out, errOut)
			}
			if !strings.Contains(out, "undid "+id(n)) || !strings.Contains(out, tt.text) {
				t.Errorf("output %q, want %q", out, tt.text)
			}
			e.check(tt.want)
			rb := e.latest()
			if rb.Op != "rollback" || rb.Extra.Undid != n {
				t.Fatalf("latest change %+v, want rollback of %d", rb, n)
			}

			// Rolling back the rollback redoes the change; once more undoes it.
			if out, errOut, code := e.run("", "rollback", id(rb.ID)); code != 0 {
				t.Fatalf("rollback of rollback: exit %d\n%s%s", code, out, errOut)
			}
			if out, errOut, code := e.run("", "rollback", id(e.latest().ID)); code != 0 {
				t.Fatalf("third rollback: exit %d\n%s%s", code, out, errOut)
			}
			e.check(tt.want)
		})
	}
}

func (e *env) check(want map[string]string) {
	e.t.Helper()
	for p, s := range want {
		switch {
		case s == absent:
			if e.exists(p) {
				e.t.Errorf("%s exists, want absent", p)
			}
		default:
			if got := e.read(p); got != s {
				e.t.Errorf("%s = %q, want %q", p, got, s)
			}
		}
	}
	// The index follows: ls lists exactly the files on disk.
	out, errOut, code := e.run("", "ls", "--json")
	if code != 0 {
		e.t.Fatalf("ls: exit %d\n%s%s", code, out, errOut)
	}
	for p, s := range want {
		name := filepath.Base(p)
		listed := strings.Contains(out, `"`+name+`"`) || strings.Contains(out, `/`+name+`"`)
		if present := s != absent; present != listed {
			e.t.Errorf("ls lists %s = %v, want %v\n%s", p, listed, present, out)
		}
	}
}

func TestUndoFileMode(t *testing.T) {
	e := setup(t, map[string]string{"f.txt": base})
	if err := os.Chmod(filepath.Join(e.root, "f.txt"), 0o750); err != nil {
		t.Fatal(err)
	}
	n := e.ok("", "rm", "f.txt", "--force")
	e.ok("", "rollback", id(n))
	st, err := os.Stat(filepath.Join(e.root, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o750 {
		t.Errorf("mode %v, want 0750", st.Mode().Perm())
	}
}

func TestUndoFileConflicts(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		steps func(e *env) int64
		code  int
		msg   string
	}{
		{"rm: path exists again", nil, func(e *env) int64 {
			n := e.ok("", "rm", "f.txt", "--force")
			e.ok("other\n", "write", "f.txt")
			return n
		}, 6, "exists again"},
		{"rm: path recreated externally", nil, func(e *env) int64 {
			n := e.ok("", "rm", "f.txt", "--force")
			e.writeDisk("f.txt", "ext\n")
			return n
		}, 6, "exists again"},
		{"new file: modified since", nil, func(e *env) int64 {
			n := e.ok("a\nb\n", "write", "n.txt")
			e.ok("B\n", "edit", "n.txt", "2", "2", "--v", e.v("n.txt"))
			return n
		}, 6, "modified since"},
		{"new file: modified externally", nil, func(e *env) int64 {
			n := e.ok("a\n", "write", "n.txt")
			e.writeDisk("n.txt", "ext\n")
			return n
		}, 6, "modified since"},
		{"new file: removed since", nil, func(e *env) int64 {
			n := e.ok("a\n", "write", "n.txt")
			e.ok("", "rm", "n.txt", "--force")
			return n
		}, 6, "was removed by change"},
		{"mv: source occupied", nil, func(e *env) int64 {
			n := e.ok("", "mv", "f.txt", "g.txt")
			e.ok("new\n", "write", "f.txt")
			return n
		}, 6, "f.txt exists again"},
		{"mv: target modified", nil, func(e *env) int64 {
			n := e.ok("", "mv", "f.txt", "g.txt")
			e.ok("X\n", "edit", "g.txt", "1", "1", "--v", e.v("g.txt"))
			return n
		}, 6, "modified since the move"},
		{"mv: target modified externally", nil, func(e *env) int64 {
			n := e.ok("", "mv", "f.txt", "g.txt")
			e.writeDisk("g.txt", "ext\n")
			return n
		}, 6, "modified since the move"},
		{"mv: moved again", nil, func(e *env) int64 {
			n := e.ok("", "mv", "f.txt", "g.txt")
			e.ok("", "mv", "g.txt", "h.txt")
			return n
		}, 6, "moved again to h.txt"},
		{"mv: target removed", nil, func(e *env) int64 {
			n := e.ok("", "mv", "f.txt", "g.txt")
			e.ok("", "rm", "g.txt", "--force")
			return n
		}, 6, "was removed by change"},
		{"binary rm refused", map[string]string{"b.bin": "\x00\x01"}, func(e *env) int64 {
			return e.ok("", "rm", "b.bin", "--force")
		}, 7, "binary"},
		{"binary new file refused", nil, func(e *env) int64 {
			e.writeDisk("x.bin", "\x00\x01")
			return e.ok("", "index")
		}, 7, "binary"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"f.txt": base}
			for p, s := range tt.files {
				files[p] = s
			}
			e := setup(t, files)
			n := tt.steps(e)
			e.ok("", "ls") // record external edits so the snapshot is stable
			snap := e.snapshot()
			last := e.latest().ID
			out, errOut, code := e.run("", "rollback", id(n))
			if code != tt.code {
				t.Fatalf("exit %d, want %d\n%s%s", code, tt.code, out, errOut)
			}
			if !strings.Contains(out+errOut, tt.msg) {
				t.Errorf("output %q, want %q", out+errOut, tt.msg)
			}
			if got := e.snapshot(); got != snap {
				t.Errorf("files changed:\n%s\nwant\n%s", got, snap)
			}
			if got := e.latest().ID; got != last {
				t.Errorf("history grew to %d, want %d", got, last)
			}
		})
	}
}

func (e *env) snapshot() string {
	e.t.Helper()
	var b strings.Builder
	filepath.WalkDir(e.root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".lino" {
				return filepath.SkipDir
			}
			return nil
		}
		data, _ := os.ReadFile(p)
		rel, _ := filepath.Rel(e.root, p)
		b.WriteString(rel + "=" + string(data) + "\n")
		return nil
	})
	return b.String()
}

func TestUndoFileJSON(t *testing.T) {
	e := setup(t, map[string]string{"f.txt": base})
	n := e.ok("", "mv", "f.txt", "g.txt")
	out, errOut, code := e.run("", "rollback", id(n), "--json")
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
	d := env.Data
	if d.Path != "f.txt" || d.From != "g.txt" || d.Undid != n || d.ID == 0 || d.Op != "rollback" {
		t.Errorf("data %+v", d)
	}
}

func TestUndoFileThenLines(t *testing.T) {
	e := setup(t, map[string]string{"f.txt": base})
	rm := e.ok("", "rm", "f.txt", "--force")
	e.ok("", "rollback", id(rm))
	ed := e.ok("X\n", "edit", "f.txt", "2", "2", "--v", e.v("f.txt"))
	e.ok("", "rollback", id(ed))
	if got := e.read("f.txt"); got != base {
		t.Fatalf("f.txt = %q, want %q", got, base)
	}
}
