package rollback

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (e *env) exists(rel string) bool {
	_, err := os.Lstat(filepath.Join(e.root, rel))
	return err == nil
}

// want maps a path to its expected content; "" means the file must not exist.
func (e *env) want(files map[string]string) {
	e.t.Helper()
	for p, s := range files {
		switch {
		case s == "" && e.exists(p):
			e.t.Errorf("%s exists: %q", p, e.read(p))
		case s != "" && !e.exists(p):
			e.t.Errorf("%s missing, want %q", p, s)
		case s != "" && e.read(p) != s:
			e.t.Errorf("%s = %q, want %q", p, e.read(p), s)
		}
	}
}

func TestRestoreTo(t *testing.T) {
	const a1 = "A\nl2\nl3\nl4\nl5\nl6\n"
	tests := []struct {
		name  string
		steps func(e *env) // after the point
		args  []string
		want  map[string]string
	}{
		{
			name: "multi-file",
			steps: func(e *env) {
				e.ok("B\n", "edit", "a.txt", "2", "2", "--v", e.v("a.txt"))
				e.ok("top\n", "insert", "b.txt", "--at-start", "--v", e.v("b.txt"))
				e.ok("new\n", "write", "c.txt")
			},
			want: map[string]string{"a.txt": a1, "b.txt": base, "c.txt": ""},
		},
		{
			name: "path limits",
			steps: func(e *env) {
				e.ok("B\n", "edit", "a.txt", "2", "2", "--v", e.v("a.txt"))
				e.ok("top\n", "insert", "b.txt", "--at-start", "--v", e.v("b.txt"))
			},
			args: []string{"--path", "a.txt"},
			want: map[string]string{"a.txt": a1, "b.txt": "top\n" + base},
		},
		{
			name: "path glob",
			steps: func(e *env) {
				e.ok("x\n", "write", "d/x.txt")
				e.ok("y\n", "write", "d/y.txt")
				e.ok("top\n", "insert", "b.txt", "--at-start", "--v", e.v("b.txt"))
			},
			args: []string{"--path", "d/**"},
			want: map[string]string{"d/x.txt": "", "d/y.txt": "", "b.txt": "top\n" + base},
		},
		{
			name: "across mv and rm",
			steps: func(e *env) {
				e.ok("", "mv", "a.txt", "d/moved.txt")
				e.ok("M\n", "edit", "d/moved.txt", "1", "1", "--v", e.v("d/moved.txt"))
				e.ok("", "rm", "b.txt", "--v", e.v("b.txt"))
			},
			want: map[string]string{"a.txt": a1, "d/moved.txt": "", "b.txt": base},
		},
		{
			name: "path on mv target pulls in source",
			steps: func(e *env) {
				e.ok("", "mv", "a.txt", "z.txt")
				e.ok("", "rm", "b.txt", "--v", e.v("b.txt"))
			},
			args: []string{"--path", "z.txt"},
			want: map[string]string{"a.txt": a1, "z.txt": "", "b.txt": ""},
		},
		{
			name: "rm then recreate",
			steps: func(e *env) {
				e.ok("", "rm", "a.txt", "--v", e.v("a.txt"))
				e.ok("other\n", "write", "a.txt")
			},
			want: map[string]string{"a.txt": a1},
		},
		{
			name: "external edits",
			steps: func(e *env) {
				e.writeDisk("b.txt", "ext\n")
			},
			want: map[string]string{"b.txt": base},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t, map[string]string{"a.txt": base, "b.txt": base})
			point := e.ok("A\n", "edit", "a.txt", "1", "1", "--v", e.v("a.txt"))
			tt.steps(e)
			before := e.latest().ID
			out, errOut, code := e.run("", append([]string{"rollback", "--to", id(point)}, tt.args...)...)
			if code != 0 {
				t.Fatalf("exit %d\n%s%s", code, out, errOut)
			}
			e.want(tt.want)
			if !strings.Contains(out, "rollback") || !strings.Contains(out, "to "+id(point)) {
				t.Errorf("output %q", out)
			}
			if c := e.latest(); c.ID <= before || c.Op != "rollback" {
				t.Errorf("latest change %+v, want a rollback after %d", c, before)
			}
		})
	}
}

func TestRestoreToIsRollbackable(t *testing.T) {
	e := setup(t, map[string]string{"a.txt": base, "b.txt": base})
	point := e.ok("A\n", "edit", "a.txt", "1", "1", "--v", e.v("a.txt"))
	e.ok("B\n", "edit", "a.txt", "3", "3", "--v", e.v("a.txt"))
	e.ok("", "mv", "b.txt", "c.txt")
	e.ok("new\n", "write", "n.txt")
	mid := e.latest().ID
	state := map[string]string{"a.txt": e.read("a.txt"), "b.txt": "", "c.txt": base, "n.txt": "new\n"}

	e.ok("", "rollback", "--to", id(point))
	e.want(map[string]string{"a.txt": "A\nl2\nl3\nl4\nl5\nl6\n", "b.txt": base, "c.txt": "", "n.txt": ""})

	// Returning to the point before the restore undoes it.
	e.ok("", "rollback", "--to", id(mid))
	e.want(state)

	// A restore of a modified file is a line change: undo it by id.
	e.ok("", "rollback", "--to", id(point), "--path", "a.txt")
	r := e.latest()
	if r.Path != "a.txt" || r.Op != "rollback" {
		t.Fatalf("latest %+v", r)
	}
	e.ok("", "rollback", id(r.ID))
	e.want(map[string]string{"a.txt": state["a.txt"]})
}

func TestRestoreToOutcomes(t *testing.T) {
	e := setup(t, map[string]string{"a.txt": base})
	point := e.ok("A\n", "edit", "a.txt", "1", "1", "--v", e.v("a.txt"))

	out, _, code := e.run("", "rollback", "--to", id(point))
	if code != 0 || !strings.Contains(out, "nothing to restore") {
		t.Fatalf("latest point: exit %d %q", code, out)
	}
	if _, _, code := e.run("", "rollback", "--to", "999"); code != 3 {
		t.Fatalf("unknown id: exit %d, want 3", code)
	}
	if _, _, code := e.run("", "rollback", "--to", "x"); code != 2 {
		t.Fatalf("bad id: exit %d, want 2", code)
	}
	if _, _, code := e.run("", "rollback", "1", "--to", id(point)); code != 2 {
		t.Fatalf("id and --to: exit %d, want 2", code)
	}
	if _, _, code := e.run("", "rollback", "--path", "a.txt"); code != 2 {
		t.Fatalf("--path without --to: exit %d, want 2", code)
	}

	e.ok("X\n", "edit", "a.txt", "2", "2", "--v", e.v("a.txt"))
	out, _, code = e.run("", "rollback", "--to", id(point), "--json")
	var res struct {
		OK      bool   `json:"ok"`
		Outcome string `json:"outcome"`
		Data    ToData `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || code != 0 {
		t.Fatalf("exit %d: %v\n%s", code, err, out)
	}
	if res.Outcome != "updated" || res.Data.To != point || len(res.Data.Files) != 1 || res.Data.Files[0].Path != "a.txt" || res.Data.Files[0].ID == 0 {
		t.Fatalf("%s", out)
	}

	// Binary content after the point cannot be reconstructed.
	e.ok("", "rm", "a.txt", "--v", e.v("a.txt"))
	if err := os.WriteFile(filepath.Join(e.root, "a.txt"), []byte("bin\x00ary"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, errOut, code := e.run("", "rollback", "--to", id(point)); code != 7 {
		t.Fatalf("binary: exit %d, want 7\n%s%s", code, out, errOut)
	}
}
