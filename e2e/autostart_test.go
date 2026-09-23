package e2e

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/registry"
)

func liveEntry(t *testing.T, h *Harness) (registry.Entry, bool) {
	t.Helper()
	reg := &registry.Registry{Dir: filepath.Join(h.Home, ".lino", "run")}
	e, err := reg.Read(registry.IDFor(h.Root))
	return e, err == nil && registry.Check(e) == registry.Live
}

func TestAutoStart(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	h.Write("a.txt", "one\ntwo\n").Mkdir("sub")
	h.ExpectExit(h.Run("init"), 0)
	t.Cleanup(func() { h.Run("stop") })

	for round := 0; round < 2; round++ {
		if _, ok := liveEntry(t, h); ok {
			t.Fatalf("round %d: process already live", round)
		}
		r := h.Exec(Cmd{Args: []string{"read", "../a.txt", "--lines", "2"}, Dir: "sub"})
		h.ExpectExit(r, 0)
		if !strings.HasSuffix(r.Stdout, "lines 2-2 of 2\ntwo\n") {
			t.Errorf("round %d stdout %q", round, r.Stdout)
		}
		if !strings.HasPrefix(r.Stderr, "auto-started: lino "+registry.IDFor(h.Root)) || !strings.Contains(r.Stderr, "reconciled") {
			t.Errorf("round %d: no startup note on stderr: %q", round, r.Stderr)
		}
		e, ok := liveEntry(t, h)
		if !ok {
			t.Fatalf("round %d: no live process after auto-start", round)
		}

		r = h.Run("read", "a.txt")
		h.ExpectExit(r, 0)
		if r.Stderr != "" {
			t.Errorf("second call stderr %q", r.Stderr)
		}
		if e2, _ := liveEntry(t, h); e2.PID != e.PID {
			t.Errorf("second call started another process: %d != %d", e2.PID, e.PID)
		}
		h.ExpectExit(h.Run("stop"), 0)
	}
}

func TestAutoStartNotInitialised(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	h.Write("a.txt", "x\n")
	for _, args := range [][]string{{"read", "a.txt"}, {"search", "abc"}, {"read", "a.txt", "--direct"}} {
		r := h.Run(args...)
		h.ExpectExit(r, 8)
		if !strings.Contains(r.Stderr, "lino init "+h.Root) {
			t.Errorf("%v: no init hint:\n%s", args, h.Transcript(r))
		}
	}
	if _, ok := liveEntry(t, h); ok {
		t.Error("process started outside an initialised root")
	}
}

func TestDirectWhileLive(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	h.Write("a.txt", "one\n")
	h.ExpectExit(h.Run("init"), 0)
	h.ExpectExit(h.Run("read", "a.txt", "--direct"), 0)

	_, done := startLive(t, h)
	for _, c := range []Cmd{
		{Args: []string{"read", "a.txt", "--direct"}},
		{Args: []string{"search", "one", "--direct"}},
		{Args: []string{"write", "b.txt", "--direct"}, Stdin: "b\n"},
		{Args: []string{"read", "a.txt", "--direct", "--json"}},
	} {
		r := h.Exec(c)
		h.ExpectExit(r, 9)
	}
	r := h.Run("read", "a.txt", "--direct", "--json")
	if !strings.Contains(r.Stdout, `"outcome":"live_exists"`) {
		t.Errorf("json: %s", r.Stdout)
	}
	if h.Tree()[0] != "a.txt" || len(h.Tree()) != 1 {
		t.Errorf("direct write went through: %v", h.Tree())
	}
	h.ExpectExit(h.Run("stop"), 0)
	expectGone(t, h, done)
	h.ExpectExit(h.Run("read", "a.txt", "--direct"), 0)
}

func TestAutoStartKeepsName(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	h.Write("a.txt", "one\n")
	h.ExpectExit(h.Run("init"), 0)
	t.Cleanup(func() { h.Run("stop") })

	h.ExpectExit(h.Run("run", "--name", "demo"), 0)
	h.ExpectExit(h.Run("stop", "-i", "demo"), 0)
	h.ExpectExit(h.Run("read", "a.txt"), 0)
	if e, ok := liveEntry(t, h); !ok || e.Name != "demo" {
		t.Fatalf("auto-started entry %+v live=%v, want name demo", e, ok)
	}
	if r := h.Run("ps"); !strings.Contains(r.Stdout, "demo") {
		t.Errorf("ps does not show demo:\n%s", h.Transcript(r))
	}
	h.ExpectExit(h.Run("stop", "-i", "demo"), 0)

	h.ExpectExit(h.Run("run", "--name", "other"), 0)
	h.ExpectExit(h.Run("stop", "-i", "other"), 0)
	h.ExpectExit(h.Run("run"), 0)
	if e, ok := liveEntry(t, h); !ok || e.Name != "other" {
		t.Fatalf("plain run entry %+v live=%v, want name other", e, ok)
	}
	h.ExpectExit(h.Run("stop", "-i", "other"), 0)
}

func TestRunNamesAutoStarted(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	h.Write("a.txt", "one\n")
	h.ExpectExit(h.Run("init"), 0)
	t.Cleanup(func() { h.Run("stop") })

	h.ExpectExit(h.Run("read", "a.txt"), 0)
	e, ok := liveEntry(t, h)
	if !ok || e.Name != "" {
		t.Fatalf("auto-started entry %+v live=%v", e, ok)
	}
	r := h.Run("run", "--name", "demo")
	h.ExpectExit(r, 0)
	if !strings.Contains(r.Stderr+r.Stdout, "already running (named demo)") {
		t.Errorf("run --name output:\n%s", h.Transcript(r))
	}
	if e2, ok := liveEntry(t, h); !ok || e2.Name != "demo" || e2.PID != e.PID {
		t.Fatalf("entry after run --name %+v live=%v", e2, ok)
	}
	if r := h.Run("status", "-i", "demo"); !strings.Contains(r.Stdout, "(demo)") {
		t.Errorf("status does not show demo:\n%s", h.Transcript(r))
	}
	h.ExpectExit(h.Run("stop", "-i", "demo"), 0)
	if _, ok := liveEntry(t, h); ok {
		t.Fatal("process still live after stop -i demo")
	}
}
