package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/registry"
)

type runJSON struct {
	OK      bool   `json:"ok"`
	Outcome string `json:"outcome"`
	Data    struct {
		ID       string `json:"id"`
		PID      int    `json:"pid"`
		Root     string `json:"root"`
		Existing bool   `json:"existing"`
	} `json:"data"`
}

func parseRun(t *testing.T, r Result) runJSON {
	t.Helper()
	var v runJSON
	if err := json.Unmarshal([]byte(r.Stdout), &v); err != nil {
		t.Fatalf("run output %q (stderr %q): %v", r.Stdout, r.Stderr, err)
	}
	return v
}

// stopOnCleanup terminates the live process of h's root when the test ends.
func stopOnCleanup(t *testing.T, h *Harness) {
	t.Cleanup(func() {
		reg := &registry.Registry{Dir: filepath.Join(h.Home, ".lino", "run")}
		e, err := reg.Read(registry.IDFor(h.Root))
		if err != nil {
			return
		}
		syscall.Kill(e.PID, syscall.SIGTERM)
		for i := 0; i < 100 && registry.PIDAlive(e.PID); i++ {
			time.Sleep(50 * time.Millisecond)
		}
		if registry.PIDAlive(e.PID) {
			syscall.Kill(e.PID, syscall.SIGKILL)
		}
	})
}

// servers counts running `lino-core run <root> --foreground` processes.
func servers(t *testing.T, root string) int {
	t.Helper()
	out, _ := exec.Command("ps", "-Ao", "args").Output()
	n := 0
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, coreBin()+" run "+root+" --foreground") {
			n++
		}
	}
	return n
}

func TestRunDetachedIdempotent(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	h.Write("a.txt", "one\ntwo\n")
	h.ExpectExit(h.Run("init"), 0)
	stopOnCleanup(t, h)

	first := h.Run("run")
	h.ExpectExit(first, 0)
	id := registry.IDFor(h.Root)
	if strings.TrimSpace(first.Stdout) != id {
		t.Fatalf("first run stdout %q, want %q (stderr %q)", first.Stdout, id, first.Stderr)
	}
	if !strings.Contains(first.Stderr, "reconciled") {
		t.Errorf("first run stderr %q lacks the reconcile note", first.Stderr)
	}

	second := parseRun(t, h.Run("run", "--json"))
	if second.Data.ID != id || !second.Data.Existing {
		t.Fatalf("second run %+v", second)
	}
	third := parseRun(t, h.Run("run", "--json"))
	if third.Data.PID != second.Data.PID {
		t.Fatalf("pid changed: %d then %d", second.Data.PID, third.Data.PID)
	}
	if !registry.PIDAlive(second.Data.PID) {
		t.Fatalf("pid %d not alive", second.Data.PID)
	}
	if n := servers(t, h.Root); n != 1 {
		t.Fatalf("%d server processes, want 1", n)
	}
	logPath := filepath.Join(h.Home, ".lino", "run", id+".log")
	if b, err := os.ReadFile(logPath); err != nil || !strings.Contains(string(b), "serving") {
		t.Errorf("log %s: %q %v", logPath, b, err)
	}

	// The detached process serves requests.
	r := h.Run("read", "a.txt", "--lines", "2")
	h.ExpectExit(r, 0)
}

func TestRunDetachedConcurrent(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	for i := 0; i < 50; i++ {
		h.Write(filepath.Join("d", string(rune('a'+i%26)), "f"+string(rune('a'+i/26))+".txt"), "x\ny\n")
	}
	h.ExpectExit(h.Run("init"), 0)
	stopOnCleanup(t, h)

	const n = 10
	results := make([]Result, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = h.Run("run", "--json")
		}()
	}
	wg.Wait()

	pids := map[int]bool{}
	started := 0
	for _, r := range results {
		h.ExpectExit(r, 0)
		v := parseRun(t, r)
		if v.Data.ID != registry.IDFor(h.Root) {
			t.Fatalf("id %q", v.Data.ID)
		}
		pids[v.Data.PID] = true
		if !v.Data.Existing {
			started++
		}
	}
	if len(pids) != 1 || started != 1 {
		t.Fatalf("pids %v, %d reported a fresh start; want one process", pids, started)
	}
	if c := servers(t, h.Root); c != 1 {
		t.Fatalf("%d server processes, want 1", c)
	}
}

func TestRunDetachedNotInitialised(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	r := h.Run("run")
	h.ExpectExit(r, 8)
	if !strings.Contains(r.Stderr, "lino init") {
		t.Errorf("stderr %q lacks the init hint", r.Stderr)
	}
}
