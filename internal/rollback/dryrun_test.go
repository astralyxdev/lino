package rollback

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/histrec"
)

var update = flag.Bool("update", false, "rewrite golden files")

// service is a stand-in for the scope example's wallet/service.go.
var service = func() string {
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		switch i {
		case 12:
			b.WriteString("func Withdraw(ctx context.Context, id, amt int64) error {\n")
		case 13:
			b.WriteString("    if amt <= 0 {\n")
		case 14:
			b.WriteString("        return ErrInvalidAmount\n")
		case 15:
			b.WriteString("    }\n")
		default:
			fmt.Fprintf(&b, "// line %d\n", i)
		}
	}
	return b.String()
}()

func (e *env) historySnapshot() string {
	e.t.Helper()
	st, err := histrec.Store(context.Background(), e.root)
	if err != nil {
		e.t.Fatal(err)
	}
	cs, err := st.List(context.Background(), history.Filter{Asc: true})
	if err != nil {
		e.t.Fatal(err)
	}
	var b strings.Builder
	for _, c := range cs {
		fmt.Fprintf(&b, "%d %s %s %s %s→%s %+v\n", c.ID, c.Op, c.Path, c.Author, c.VBefore, c.VAfter, c.Extra)
	}
	return b.String()
}

func TestDryRunGolden(t *testing.T) {
	tests := []struct {
		name   string
		files  map[string]string
		config string
		// steps prepares history and returns the rollback arguments.
		steps func(e *env) []string
		code  int
	}{
		{"edit", map[string]string{"wallet/service.go": service}, "", func(e *env) []string {
			a := e.ok("    if amt <= 0 || amt > MaxWithdraw {\n        return ErrInvalidAmount\n    }\n",
				"edit", "wallet/service.go", "13", "15", "--v", e.v("wallet/service.go"), "--by", "agent-2")
			return []string{id(a)}
		}, 0},
		{"edit_multi", map[string]string{"f.txt": base}, "", func(e *env) []string {
			a := e.ok("l1\nB\nl3\nl4\nl5\nl6\nl7\n", "write", "f.txt", "--v", e.v("f.txt"))
			e.ok("top\n", "insert", "f.txt", "--at-start", "--v", e.v("f.txt"))
			return []string{id(a)}
		}, 0},
		{"edit_after_mv", map[string]string{"f.txt": base}, "", func(e *env) []string {
			a := e.ok("X\n", "edit", "f.txt", "2", "2", "--v", e.v("f.txt"))
			e.ok("", "mv", "f.txt", "g.txt")
			return []string{id(a)}
		}, 0},
		{"conflict", map[string]string{"f.txt": base}, "", func(e *env) []string {
			a := e.ok("X\n", "edit", "f.txt", "4", "4", "--v", e.v("f.txt"), "--by", "agent-1")
			e.ok("Y\n", "edit", "f.txt", "4", "4", "--v", e.v("f.txt"), "--by", "agent-2")
			return []string{id(a)}
		}, 6},
		{"rm", map[string]string{"f.txt": base}, "", func(e *env) []string {
			return []string{id(e.ok("", "rm", "f.txt", "--v", e.v("f.txt")))}
		}, 0},
		{"created", nil, "", func(e *env) []string {
			return []string{id(e.ok("n1\nn2\n", "write", "n.txt"))}
		}, 0},
		{"mv", map[string]string{"f.txt": base}, "", func(e *env) []string {
			return []string{id(e.ok("", "mv", "f.txt", "g.txt"))}
		}, 0},
		{"latest_by", map[string]string{"f.txt": base}, "", func(e *env) []string {
			e.ok("X\n", "edit", "f.txt", "1", "1", "--v", e.v("f.txt"), "--by", "agent-1")
			e.ok("Y\n", "edit", "f.txt", "6", "6", "--v", e.v("f.txt"), "--by", "agent-2")
			return []string{"--by", "agent-1"}
		}, 0},
		{"latest_nothing", map[string]string{"f.txt": base}, "", func(e *env) []string {
			return []string{"--by", "nobody"}
		}, 0},
		{"to", map[string]string{"f.txt": base, "g.txt": "g1\n"}, "", func(e *env) []string {
			a := e.ok("X\n", "edit", "f.txt", "1", "1", "--v", e.v("f.txt"))
			e.ok("Y\n", "edit", "f.txt", "6", "6", "--v", e.v("f.txt"))
			e.ok("", "rm", "g.txt", "--force")
			e.ok("new\n", "write", "h.txt")
			return []string{"--to", id(a)}
		}, 0},
		{"to_nothing", map[string]string{"f.txt": base}, "", func(e *env) []string {
			return []string{"--to", id(e.ok("X\n", "edit", "f.txt", "1", "1", "--v", e.v("f.txt")))}
		}, 0},
		{"truncated", map[string]string{"f.txt": base}, "read.lines = 3\n", func(e *env) []string {
			return []string{id(e.ok("", "rm", "f.txt", "--force"))}
		}, 0},
		{"not_found", map[string]string{"f.txt": base}, "", func(e *env) []string {
			return []string{"99"}
		}, 3},
	}
	for _, tt := range tests {
		for _, mode := range []string{"txt", "json"} {
			t.Run(tt.name+"_"+mode, func(t *testing.T) {
				e := setup(t, tt.files)
				if tt.config != "" {
					if err := os.WriteFile(filepath.Join(e.root, ".lino", "config"), []byte(tt.config), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				args := append([]string{"rollback"}, tt.steps(e)...)
				args = append(args, "--dry-run")
				if mode == "json" {
					args = append(args, "--json")
				}
				files, hist := e.snapshot(), e.historySnapshot()
				out, errOut, code := e.run("", args...)
				if code != tt.code {
					t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tt.code, out, errOut)
				}
				if e.snapshot() != files {
					t.Errorf("files changed by dry run:\n%s", e.snapshot())
				}
				if e.historySnapshot() != hist {
					t.Errorf("history changed by dry run:\n%s", e.historySnapshot())
				}
				got := "$ lino " + strings.Join(args, " ") + "\n-- stdout --\n" + out + "-- stderr --\n" + errOut
				golden := filepath.Join("testdata", "dryrun_"+tt.name+"_"+mode+".golden")
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

// TestDryRunThenApply checks that the dry run predicts the real rollback.
func TestDryRunThenApply(t *testing.T) {
	e := setup(t, map[string]string{"f.txt": base})
	a := e.ok("X\nY\n", "edit", "f.txt", "3", "3", "--v", e.v("f.txt"))
	e.ok("top\n", "insert", "f.txt", "--at-start", "--v", e.v("f.txt"))
	out, _, code := e.run("", "rollback", id(a), "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	want := fmt.Sprintf("would undo %d (edit f.txt lines 3-4)\n- 4: X\n- 5: Y\n+ 4: l3\nno conflicts\n", a)
	if out != want {
		t.Fatalf("dry run\n got %q\nwant %q", out, want)
	}
	e.ok("", "rollback", id(a))
	if got := e.read("f.txt"); got != "top\n"+base {
		t.Fatalf("applied %q", got)
	}
}
