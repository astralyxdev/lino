package histrec

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/changelog"
	"github.com/astralyx/lino/internal/cli"
	_ "github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/initcmd"
	"github.com/astralyx/lino/internal/linediff"
	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/textfile"
	"github.com/astralyx/lino/internal/version"
)

func initRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
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
	t.Cleanup(func() {
		if v, ok := stores.LoadAndDelete(root); ok {
			v.(*history.Store).Close()
		}
	})
	return root
}

func ver(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return version.Of(b)
}

func lino(t *testing.T, root, stdin string, env map[string]string, args ...string) {
	t.Helper()
	var o, e bytes.Buffer
	code := cli.Default.Main(context.Background(), args,
		cli.Env{Cwd: root, Stdin: strings.NewReader(stdin), Getenv: func(k string) string { return env[k] }}, &o, &e)
	if code != 0 {
		t.Fatalf("lino %v: exit %d\n%s%s", args, code, o.String(), e.String())
	}
}

func lines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString("line ")
		b.WriteByte(byte('a' + i - 1))
		b.WriteByte('\n')
	}
	return b.String()
}

func TestRecordLineOps(t *testing.T) {
	base := lines(6) // line a .. line f
	tests := []struct {
		name   string
		stdin  string
		env    map[string]string
		args   func(v string) []string
		op     string
		author string
		frags  []linediff.Fragment
	}{
		{"edit", "new b\nnew c\nextra\n", nil,
			func(v string) []string { return []string{"edit", "f.txt", "2", "3", "--v", v, "--by", "agent-1"} },
			"edit", "agent-1",
			[]linediff.Fragment{{Pos: 1, Old: []string{"line b", "line c"}, New: []string{"new b", "new c", "extra"}}}},
		{"insert", "ins\n", map[string]string{"LINO_BY": "env-agent"},
			func(v string) []string { return []string{"insert", "f.txt", "--after", "4", "--v", v} },
			"insert", "env-agent",
			[]linediff.Fragment{{Pos: 4, Old: []string{}, New: []string{"ins"}}}},
		{"delete", "", nil,
			func(v string) []string { return []string{"delete", "f.txt", "5", "6", "--v", v} },
			"delete", "",
			[]linediff.Fragment{{Pos: 4, Old: []string{"line e", "line f"}, New: []string{}}}},
		{"replace", "line c\n<<<lino>>>\nLINE C\n", map[string]string{"LINO_BY": "env"},
			func(v string) []string { return []string{"replace", "f.txt", "--v", v, "--by", "flag"} },
			"replace", "flag",
			[]linediff.Fragment{{Pos: 2, Old: []string{"line c"}, New: []string{"LINE C"}}}},
		{"write_overwrite", "line a\nline B\nline c\nline d\nline e\nline f\nline g\n", nil,
			func(v string) []string { return []string{"write", "f.txt", "--v", v, "--by", "w"} },
			"write", "w",
			[]linediff.Fragment{
				{Pos: 1, Old: []string{"line b"}, New: []string{"line B"}},
				{Pos: 6, Old: []string{}, New: []string{"line g"}},
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := initRoot(t, map[string]string{"f.txt": base})
			before := ver(t, root, "f.txt")
			lino(t, root, tt.stdin, tt.env, tt.args(before)...)
			after := ver(t, root, "f.txt")

			st, err := Store(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			id, err := st.Latest(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ch, err := st.Get(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if ch.Op != tt.op || ch.Author != tt.author || ch.Path != "f.txt" || ch.Source != history.SourceLino ||
				ch.VBefore != before || ch.VAfter != after {
				t.Fatalf("change %+v", ch)
			}
			if !reflect.DeepEqual(norm(ch.Fragments), norm(tt.frags)) {
				t.Fatalf("fragments\n got %+v\nwant %+v", ch.Fragments, tt.frags)
			}
			// The fragments turn the old file into the new one.
			cur, _ := os.ReadFile(filepath.Join(root, "f.txt"))
			got, err := linediff.Apply(textfile.Parse([]byte(base)).Lines, ch.Fragments)
			if err != nil || !reflect.DeepEqual(got, textfile.Parse(cur).Lines) {
				t.Fatalf("apply: %v %q", err, got)
			}
			// The history id is the change log sequence number.
			db, err := index.Open(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			l, err := changelog.Open(context.Background(), db)
			if err != nil {
				t.Fatal(err)
			}
			es, err := l.Since(context.Background(), 0, 0)
			if err != nil || len(es) != 1 || es[0].Seq != id || !es[0].Time.Equal(ch.Time) {
				t.Fatalf("changelog %+v, %v; history id %d", es, err, id)
			}
		})
	}
}

// norm makes nil and empty line slices compare equal.
func norm(fs []linediff.Fragment) []linediff.Fragment {
	out := make([]linediff.Fragment, len(fs))
	for i, f := range fs {
		out[i] = linediff.Fragment{Pos: f.Pos, Old: append([]string{}, f.Old...), New: append([]string{}, f.New...)}
	}
	return out
}

func TestSequentialIDs(t *testing.T) {
	root := initRoot(t, map[string]string{"f.txt": lines(3)})
	for i := range 3 {
		lino(t, root, "x"+string(rune('0'+i))+"\n", nil, "edit", "f.txt", "1", "1", "--v", ver(t, root, "f.txt"))
	}
	st, err := Store(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := st.List(context.Background(), history.Filter{Asc: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 3 || cs[1].ID != cs[0].ID+1 || cs[2].ID != cs[1].ID+1 {
		t.Fatalf("changes %+v", cs)
	}
}

func TestBuild(t *testing.T) {
	crlf := []byte("\xef\xbb\xbfa\r\nb")
	tests := []struct {
		name  string
		e     changelog.Entry
		c     mutate.Commit
		extra history.Extra
		frags []linediff.Fragment
		vAft  string
	}{
		{"mv", changelog.Entry{Path: "b.txt", From: "a.txt"},
			mutate.Commit{Result: mutate.Result{Op: "mv", OldV: "abc123", NewV: "abc123"}},
			history.Extra{From: "a.txt"}, nil, "abc123"},
		{"rm", changelog.Entry{},
			mutate.Commit{Result: mutate.Result{Op: "rm", OldV: "abc123"}, Before: []byte("a\nb\n"), Mode: 0o755},
			history.Extra{Removed: true, Mode: 0o755},
			[]linediff.Fragment{{Pos: 0, Old: []string{"a", "b"}}}, ""},
		{"rm_crlf_bom", changelog.Entry{},
			mutate.Commit{Result: mutate.Result{Op: "rm", OldV: "abc123"}, Before: crlf, Mode: 0o600},
			history.Extra{Removed: true, Mode: 0o600, CRLF: true, BOM: true, NoFinalNewline: true},
			[]linediff.Fragment{{Pos: 0, Old: []string{"a", "b"}}}, ""},
		{"rm_empty", changelog.Entry{},
			mutate.Commit{Result: mutate.Result{Op: "rm", OldV: "abc123"}, Before: []byte{}, Mode: 0o644},
			history.Extra{Removed: true, Mode: 0o644}, nil, ""},
		{"rm_binary", changelog.Entry{},
			mutate.Commit{Result: mutate.Result{Op: "rm", OldV: "abc123"}, Before: []byte("a\x00b"), Mode: 0o644},
			history.Extra{Removed: true, Mode: 0o644, Binary: true}, nil, ""},
		{"rm_symlink", changelog.Entry{},
			mutate.Commit{Result: mutate.Result{Op: "rm"}, Mode: 0o777},
			history.Extra{Removed: true, Mode: 0o777, Symlink: true}, nil, ""},
		{"write_new", changelog.Entry{},
			mutate.Commit{Result: mutate.Result{Op: "write", NewV: "abc123"}, After: []byte("a\n")},
			history.Extra{Created: true},
			[]linediff.Fragment{{Pos: 0, New: []string{"a"}}}, "abc123"},
		{"write_new_binary", changelog.Entry{},
			mutate.Commit{Result: mutate.Result{Op: "write", NewV: "abc123"}, After: []byte("\x00")},
			history.Extra{Created: true, Binary: true}, nil, "abc123"},
		{"write_over", changelog.Entry{},
			mutate.Commit{Result: mutate.Result{Op: "write", OldV: "abc123", NewV: "def456"}, Before: []byte("a\n"), After: []byte("b\n")},
			history.Extra{}, []linediff.Fragment{{Pos: 0, Old: []string{"a"}, New: []string{"b"}}}, "def456"},
		{"rollback", changelog.Entry{},
			mutate.Commit{Result: mutate.Result{Op: "rollback", OldV: "abc123", NewV: "def456"}, Before: []byte("a\n"), After: []byte("b\n")},
			history.Extra{}, []linediff.Fragment{{Pos: 0, Old: []string{"a"}, New: []string{"b"}}}, "def456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch, ok := Build(tt.e, &tt.c)
			if !ok {
				t.Fatal("not recorded")
			}
			if ch.Extra != tt.extra || ch.Op != tt.c.Op || ch.VBefore != tt.c.OldV || ch.VAfter != tt.vAft {
				t.Fatalf("change %+v", ch)
			}
			if !reflect.DeepEqual(norm(ch.Fragments), norm(tt.frags)) {
				t.Fatalf("fragments\n got %+v\nwant %+v", ch.Fragments, tt.frags)
			}
		})
	}
}

func TestHistoryDBRecreated(t *testing.T) {
	root := initRoot(t, map[string]string{"f.txt": "a\n"})
	lino(t, root, "b\n", nil, "edit", "f.txt", "1", "1", "--v", ver(t, root, "f.txt"))
	for _, suffix := range []string{"", "-wal", "-shm"} {
		os.Remove(history.Path(root) + suffix)
	}
	lino(t, root, "c\n", nil, "edit", "f.txt", "1", "1", "--v", ver(t, root, "f.txt"))
	st, err := Store(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := st.List(context.Background(), history.Filter{})
	if err != nil || len(cs) != 1 || cs[0].VAfter != ver(t, root, "f.txt") {
		t.Fatalf("changes %+v, %v", cs, err)
	}
}
