package showcmd

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/linediff"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}

var t0 = time.Date(2026, 9, 23, 12, 41, 7, 0, time.UTC)

func frag(pos int, old, new []string) linediff.Fragment {
	return linediff.Fragment{Pos: pos, Old: old, New: new}
}

func l(s ...string) []string { return s }

// fixture holds one change per case, ids 1..n in order.
var fixture = []history.Change{
	// 1: edit with a replacement, a pure insertion and a pure deletion.
	{Source: history.SourceLino, Author: "agent-2", Op: history.OpEdit, Path: "wallet/service.go",
		VBefore: "8c21e0", VAfter: "5f02aa", Fragments: []linediff.Fragment{
			frag(12, l("    if amt <= 0 {"), l("    if amt <= 0 || amt > MaxWithdraw {")),
			frag(20, nil, l("    log.Debug(\"withdraw\")", "")),
			frag(30, l("// old", "// older"), nil),
		}},
	// 2: rm keeps the whole file.
	{Source: history.SourceLino, Op: history.OpRm, Path: "wallet/gone.go", VBefore: "123abc",
		Extra:     history.Extra{Removed: true, Mode: 0o644},
		Fragments: []linediff.Fragment{frag(0, l("package wallet", "", "func Gone() {}"), nil)}},
	// 3: mv stores only the paths.
	{Source: history.SourceLino, Author: "agent-1", Op: history.OpMv, Path: "wallet/new.go",
		Extra: history.Extra{From: "old/w.go"}},
	// 4: external edit.
	{Source: history.SourceExternal, Op: history.OpExternal, Path: "wallet/service.go",
		VBefore: "5f02aa", VAfter: "77c1d3", Fragments: []linediff.Fragment{frag(1, l("b"), l("B", "B2"))}},
	// 5: binary external change.
	{Source: history.SourceExternal, Op: history.OpExternal, Path: "img/logo.png",
		VBefore: "aaaaaa", VAfter: "bbbbbb", Extra: history.Extra{Binary: true}},
	// 6: write of a new file.
	{Source: history.SourceLino, Author: "agent-1", Op: history.OpWrite, Path: "wallet/validate.go",
		VAfter: "77c1d3", Extra: history.Extra{Created: true},
		Fragments: []linediff.Fragment{frag(0, nil, l("package wallet", ""))}},
	// 7: rollback.
	{Source: history.SourceLino, Author: "agent-2", Op: history.OpRollback, Path: "wallet/service.go",
		VBefore: "5f02aa", VAfter: "8c21e0", Extra: history.Extra{Undid: 1},
		Fragments: []linediff.Fragment{frag(12, l("    if amt <= 0 || amt > MaxWithdraw {"), l("    if amt <= 0 {"))}},
	// 8: symlink removed.
	{Source: history.SourceLino, Op: history.OpRm, Path: "link", Extra: history.Extra{Removed: true, Symlink: true}},
	// 9: external removal.
	{Source: history.SourceExternal, Op: history.OpExternal, Path: "notes.txt", VBefore: "abcdef",
		Extra: history.Extra{Removed: true}, Fragments: []linediff.Fragment{frag(0, l("x"), nil)}},
	// 10: big edit, cut by read.lines = 5 in the "limited" root.
	{Source: history.SourceLino, Op: history.OpEdit, Path: "big.txt", VBefore: "111111", VAfter: "222222",
		Fragments: []linediff.Fragment{frag(0, l("a1", "a2", "a3"), l("b1", "b2", "b3")), frag(10, l("c"), l("d"))}},
}

func newRoot(t *testing.T, config string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".lino"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".lino", "config"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := history.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for i, c := range fixture {
		c.Time = t0.Add(time.Duration(i) * time.Second)
		if _, err := st.Add(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func run(cwd string, args ...string) (stdout, stderr string, code int) {
	var o, e bytes.Buffer
	code = cli.Default.Main(context.Background(), args, cli.Env{Cwd: cwd, Stdin: strings.NewReader("")}, &o, &e)
	return o.String(), e.String(), code
}

func TestShowGolden(t *testing.T) {
	root := newRoot(t, "")
	limited := newRoot(t, "read.lines = 5\n")
	tests := []struct {
		name string
		root string
		args []string
		code int
	}{
		{"edit", root, []string{"1"}, 0},
		{"rm", root, []string{"2"}, 0},
		{"mv", root, []string{"3"}, 0},
		{"external", root, []string{"4"}, 0},
		{"binary", root, []string{"5"}, 0},
		{"created", root, []string{"6"}, 0},
		{"rollback", root, []string{"7"}, 0},
		{"symlink", root, []string{"8"}, 0},
		{"external_removed", root, []string{"9"}, 0},
		{"truncated", limited, []string{"10"}, 0},
		{"not_found", root, []string{"99"}, 3},
		{"bad_id", root, []string{"x"}, 2},
		{"zero_id", root, []string{"0"}, 2},
		{"no_id", root, nil, 2},
	}
	for _, tt := range tests {
		for _, mode := range []string{"txt", "json"} {
			t.Run(tt.name+"_"+mode, func(t *testing.T) {
				args := append([]string{"show"}, tt.args...)
				if mode == "json" {
					args = append(args, "--json")
				}
				out, errOut, code := run(tt.root, args...)
				if code != tt.code {
					t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tt.code, out, errOut)
				}
				got := "$ lino " + strings.Join(args, " ") + "\n-- stdout --\n" + out + "-- stderr --\n" + errOut
				golden := filepath.Join("testdata", tt.name+"_"+mode+".golden")
				if *update {
					if err := os.MkdirAll("testdata", 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatalf("%v (run with -update)", err)
				}
				if got != string(want) {
					t.Errorf("mismatch\ngot:\n%s\nwant:\n%s", got, want)
				}
			})
		}
	}
}

func TestShowNotRunning(t *testing.T) {
	_, errOut, code := run(t.TempDir(), "show", "1")
	if code != 8 || !strings.Contains(errOut, "lino init") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

func TestBuildNumbering(t *testing.T) {
	c := history.Change{Fragments: []linediff.Fragment{
		frag(2, l("x"), l("y", "z")),      // +1
		frag(5, nil, l("n1", "n2")),       // +2
		frag(9, l("d1", "d2", "d3"), nil), // -3
		frag(20, l("o"), l("p")),
	}}
	tests := []struct {
		limit int
		want  string
	}{
		{0, "3/3 6/7 10/13 21/21 shown=10 total=10"},
		{4, "3/3 6/7 shown=4 total=10 cut"},
		{3, "3/3 shown=3 total=10 cut"},
		{5, "3/3 6/7 shown=5 total=10 cut"},
	}
	for _, tt := range tests {
		d := Build(c, tt.limit)
		var parts []string
		for _, h := range d.Hunks {
			parts = append(parts, fmt.Sprintf("%d/%d", h.OldStart, h.NewStart))
		}
		got := strings.Join(parts, " ") + fmt.Sprintf(" shown=%d total=%d", d.Shown, d.DiffLines)
		if d.Truncated {
			got += " cut"
		}
		if got != tt.want {
			t.Errorf("limit %d: got %q, want %q", tt.limit, got, tt.want)
		}
	}
}
