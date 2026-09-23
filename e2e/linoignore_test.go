package e2e

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/ignore"
)

const createdNote = "created .linoignore with defaults"

func TestInitCreatesDefaultLinoignore(t *testing.T) {
	cases := []struct {
		name     string
		existing *string // nil = no .linoignore before init
	}{
		{"missing", nil},
		{"empty", ptr("")},
		{"custom", ptr("# mine\n*.log\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := New(t)
			h.Home = shortHome(t)
			h.Write("a.txt", "needle here\n").Write("node_modules/pkg/index.js", "needle in dep\n")
			if tc.existing != nil {
				h.Write(".linoignore", *tc.existing)
			}
			r := h.Exec(Cmd{Args: []string{"init"}, Env: map[string]string{"LINO_BY": "me"}})
			h.ExpectExit(r, 0)
			stopOnCleanup(t, h)
			if note := strings.Contains(r.Stdout, createdNote); note != (tc.existing == nil) {
				t.Errorf("note from init = %v:\n%s", note, h.Transcript(r))
			}
			want := string(ignore.DefaultLinoignoreContent())
			if tc.existing != nil {
				want = *tc.existing
			}
			if got := h.Read(".linoignore"); got != want {
				t.Fatalf(".linoignore = %q, want %q", got, want)
			}
			depWanted := tc.existing != nil

			// The initial index already applies the defaults.
			for _, args := range [][]string{{"ls", "--direct"}, {"search", "needle", "--direct"}} {
				r = h.Run(args...)
				h.ExpectExit(r, 0)
				if dep := strings.Contains(r.Stdout, "node_modules"); dep != depWanted {
					t.Errorf("%v: node_modules listed = %v:\n%s", args, dep, h.Transcript(r))
				}
			}

			r = h.Run("run")
			h.ExpectExit(r, 0)
			if strings.Contains(r.Stderr, "created .linoignore") {
				t.Errorf("run recreated .linoignore:\n%s", h.Transcript(r))
			}
			r = h.Run("history")
			h.ExpectExit(r, 0)
			if strings.Contains(r.Stdout, "external") {
				t.Errorf("history has external entries:\n%s", h.Transcript(r))
			}
			if logged := hasLine(r.Stdout, "lino", "me", "write", ".linoignore", "created"); logged != (tc.existing == nil) {
				t.Errorf(".linoignore creation logged as lino write by me = %v:\n%s", logged, h.Transcript(r))
			}

			// Restarting the live process and an auto-start after stop leave
			// the file alone.
			h.ExpectExit(h.Run("run"), 0)
			h.ExpectExit(h.Run("stop"), 0)
			r = h.Run("search", "needle")
			h.ExpectExit(r, 0)
			if strings.Contains(r.Stderr, "created .linoignore") {
				t.Errorf("auto-start recreated .linoignore:\n%s", h.Transcript(r))
			}
			if got := h.Read(".linoignore"); got != want {
				t.Fatalf(".linoignore rewritten: %q", got)
			}
			if dep := strings.Contains(r.Stdout, "node_modules/"); dep != depWanted {
				t.Errorf("node_modules indexed = %v:\n%s", dep, h.Transcript(r))
			}
			if !strings.Contains(r.Stdout, "a.txt") {
				t.Errorf("a.txt not found:\n%s", h.Transcript(r))
			}
		})
	}
}

// A root initialised before init wrote .linoignore gets it from run, logged as
// a lino write; files it newly ignores leave the index without history.
func TestRunCreatesLinoignoreForOlderRoot(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	h.Write("a.txt", "needle here\n").Write(".linoignore", "")
	h.ExpectExit(h.Run("init"), 0)
	stopOnCleanup(t, h)
	// Files indexed while nothing ignored them, then .linoignore gone.
	h.Write("node_modules/pkg/index.js", "needle in dep\n")
	h.ExpectExit(h.Run("index", "--direct"), 0)
	if err := os.Remove(h.Path(".linoignore")); err != nil {
		t.Fatal(err)
	}
	h.ExpectExit(h.Run("history", "--direct"), 0) // log the removal before run

	r := h.Exec(Cmd{Args: []string{"run"}, Env: map[string]string{"LINO_BY": "starter"}})
	h.ExpectExit(r, 0)
	if !strings.Contains(r.Stderr, createdNote) {
		t.Errorf("no note from run:\n%s", h.Transcript(r))
	}
	r = h.Run("search", "needle")
	h.ExpectExit(r, 0)
	if strings.Contains(r.Stdout, "node_modules") {
		t.Errorf("node_modules still indexed:\n%s", h.Transcript(r))
	}
	r = h.Run("history")
	h.ExpectExit(r, 0)
	if !hasLine(r.Stdout, "lino", "starter", "write", ".linoignore", "created") {
		t.Errorf(".linoignore creation not logged as a lino write:\n%s", h.Transcript(r))
	}
	if hasLine(r.Stdout, "node_modules", "removed") {
		t.Errorf("newly ignored files logged:\n%s", h.Transcript(r))
	}
}

// Editing .linoignore while live drops newly ignored files from the index
// without an external history entry per file.
func TestLinoignoreEditLive(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	h.Write("a.txt", "needle here\n").Write("logs/x.log", "needle in log\n")
	h.ExpectExit(h.Run("init"), 0)
	stopOnCleanup(t, h)
	h.ExpectExit(h.Run("run"), 0)
	r := h.Run("search", "needle")
	h.ExpectExit(r, 0)
	if !strings.Contains(r.Stdout, "logs/x.log") {
		t.Fatalf("log not indexed before the edit:\n%s", h.Transcript(r))
	}

	h.ExpectExit(h.Exec(Cmd{Args: []string{"write", ".linoignore", "--force"}, Stdin: "*.log\n"}), 0)
	if !waitFor(5*time.Second, func() bool {
		r = h.Run("search", "needle")
		return r.Exit == 0 && !strings.Contains(r.Stdout, "x.log")
	}) {
		t.Errorf("ignored log still indexed:\n%s", h.Transcript(r))
	}
	r = h.Run("history")
	h.ExpectExit(r, 0)
	if strings.Contains(r.Stdout, "x.log") {
		t.Errorf("newly ignored file logged:\n%s", h.Transcript(r))
	}
}

// hasLine reports whether a line of out has all fields, in order.
func hasLine(out string, fields ...string) bool {
	for line := range strings.Lines(out) {
		f := strings.Fields(line)
		i := 0
		for _, w := range f {
			if i < len(fields) && w == fields[i] {
				i++
			}
		}
		if i == len(fields) {
			return true
		}
	}
	return false
}

func ptr(s string) *string { return &s }
