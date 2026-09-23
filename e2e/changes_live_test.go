package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestChangesSinceStart: without --since, a live process lists the changes
// made since it started, and nothing from before.
func TestChangesSinceStart(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	h.Fixture("wallet")
	h.ExpectExit(h.Run("init"), 0)
	h.ExpectExit(h.Exec(Cmd{Args: []string{"write", "wallet/pre.go", "--direct"}, Stdin: "package wallet\n"}), 0)
	startLive(t, h)

	steps := []struct {
		name  string
		cmd   Cmd
		check func(r Result) bool
	}{
		{"changes_live_start", Cmd{Args: []string{"changes"}}, func(r Result) bool {
			return strings.HasPrefix(r.Stdout, "next ")
		}},
		{"", Cmd{Args: []string{"write", "wallet/a.go"}, Stdin: "package wallet\n\nvar A = 1\n"}, nil},
		{"", Cmd{Args: []string{"write", "wallet/b.go"}, Stdin: "package wallet\n\nvar B = 2\n"}, nil},
		{"changes_live_two", Cmd{Args: []string{"changes"}}, func(r Result) bool {
			return strings.Contains(r.Stdout, "wallet/a.go") && strings.Contains(r.Stdout, "wallet/b.go") &&
				!strings.Contains(r.Stdout, "pre.go")
		}},
	}
	for _, s := range steps {
		r := h.Exec(s.cmd)
		h.ExpectExit(r, 0)
		if s.name == "" {
			continue
		}
		if !s.check(r) {
			t.Errorf("%s: unexpected output\n%s", s.name, h.Transcript(r))
		}
		h.Golden(s.name, r)
	}

	// --wait without --since: blocks until a matching change after start.
	got := make(chan Result, 1)
	go func() { got <- h.Exec(Cmd{Args: []string{"changes", "--wait", "10s", "--path", "later/**"}}) }()
	time.Sleep(300 * time.Millisecond)
	h.ExpectExit(h.Exec(Cmd{Args: []string{"write", "later/c.txt"}, Stdin: "c\n"}), 0)
	select {
	case r := <-got:
		h.ExpectExit(r, 0)
		if !strings.Contains(r.Stdout, "later/c.txt") || strings.Contains(r.Stdout, "wallet/") {
			t.Errorf("wait result\n%s", h.Transcript(r))
		}
	case <-time.After(15 * time.Second):
		t.Fatal("changes --wait did not return")
	}
}
