package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/registry"
)

type psJSON struct {
	Outcome string `json:"outcome"`
	Data    struct {
		Processes []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Root string `json:"root"`
			PID  int    `json:"pid"`
		} `json:"processes"`
		Removed []struct {
			ID     string `json:"id"`
			Reason string `json:"reason"`
		} `json:"removed"`
	} `json:"data"`
}

func ps(t *testing.T, h *Harness) psJSON {
	t.Helper()
	r := h.Run("ps", "--json")
	h.ExpectExit(r, 0)
	var v psJSON
	if err := json.Unmarshal([]byte(r.Stdout), &v); err != nil {
		t.Fatalf("ps output %q: %v", r.Stdout, err)
	}
	return v
}

// psRoots maps id to root for every live process ps reports.
func psRoots(t *testing.T, h *Harness) map[string]string {
	m := map[string]string{}
	for _, p := range ps(t, h).Data.Processes {
		m[p.ID] = p.Root
	}
	return m
}

func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

// TestLiveTwoRoots runs two roots side by side under one HOME and addresses
// them by cwd, -i id, -i name and LINO_ID.
func TestLiveTwoRoots(t *testing.T) {
	a := New(t)
	a.Home = shortHome(t)
	b := New(t)
	b.Home = a.Home
	a.Write("a.txt", "alpha\n").Mkdir("sub/deep")
	b.Write("b.txt", "beta\n").Write("a.txt", "not alpha\n").Mkdir("sub")
	for _, h := range []*Harness{a, b} {
		h.ExpectExit(h.Run("init"), 0)
		stopOnCleanup(t, h)
	}
	idA, idB := registry.IDFor(a.Root), registry.IDFor(b.Root)
	if idA == idB {
		t.Fatalf("both roots got id %s", idA)
	}

	a.ExpectExit(a.Run("run", "--name", "alpha"), 0)
	b.ExpectExit(b.Run("run", "--name", "beta"), 0)
	got := psRoots(t, a)
	if len(got) != 2 || got[idA] != a.Root || got[idB] != b.Root {
		t.Fatalf("ps: %v", got)
	}
	r := a.Run("ps")
	a.ExpectExit(r, 0)
	for _, s := range []string{"ID", idA, "alpha", a.Root, idB, "beta", b.Root} {
		if !strings.Contains(r.Stdout, s) {
			t.Errorf("ps text lacks %q:\n%s", s, r.Stdout)
		}
	}

	// All run from root a (or its subdir); want is the file's first line.
	cases := []struct {
		name string
		args []string
		dir  string
		env  map[string]string
		want string
		exit int
	}{
		{name: "cwd root", args: []string{"read", "a.txt"}, want: "alpha"},
		{name: "cwd subdir", args: []string{"read", "../../a.txt"}, dir: "sub/deep", want: "alpha"},
		{name: "flag id", args: []string{"read", "b.txt", "-i", idB}, want: "beta"},
		{name: "flag long", args: []string{"read", "b.txt", "--id", idB}, want: "beta"},
		{name: "flag name", args: []string{"read", "b.txt", "-i", "beta"}, want: "beta"},
		{name: "flag id from subdir", args: []string{"read", "a.txt", "-i", "beta"}, dir: "sub", want: "not alpha"},
		{name: "env id", args: []string{"read", "b.txt"}, env: map[string]string{"LINO_ID": idB}, want: "beta"},
		{name: "env name", args: []string{"read", "a.txt"}, env: map[string]string{"LINO_ID": "beta"}, want: "not alpha"},
		{name: "flag beats env", args: []string{"read", "a.txt", "-i", "alpha"}, env: map[string]string{"LINO_ID": "beta"}, want: "alpha"},
		{name: "search by name", args: []string{"search", "beta", "-i", "beta"}, want: "b.txt"},
		{name: "status by name", args: []string{"status", "-i", "beta"}, want: "root      " + b.Root},
		{name: "unknown id", args: []string{"read", "a.txt", "-i", "zzzzzz"}, exit: 3},
		{name: "unknown env id", args: []string{"read", "a.txt"}, env: map[string]string{"LINO_ID": "nosuch"}, exit: 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := a.Exec(Cmd{Args: c.args, Dir: c.dir, Env: c.env})
			a.ExpectExit(r, c.exit)
			if c.exit != 0 {
				return
			}
			lines := strings.SplitN(r.Stdout, "\n", 3)
			if len(lines) < 2 || lines[1] != c.want && !strings.HasPrefix(lines[0], c.want) {
				t.Errorf("stdout %q, want %q", r.Stdout, c.want)
			}
		})
	}

	// A write addressed to b by id lands in b, not in the cwd's root.
	r = a.Exec(Cmd{Args: []string{"write", "new.txt", "-i", "beta"}, Stdin: "x\n"})
	a.ExpectExit(r, 0)
	if _, err := os.Stat(b.Path("new.txt")); err != nil {
		t.Errorf("write via -i beta: %v", err)
	}
	if _, err := os.Stat(a.Path("new.txt")); err == nil {
		t.Error("write via -i beta landed in root a")
	}

	// Stopping one leaves the other running.
	a.ExpectExit(a.Run("stop", "-i", "alpha"), 0)
	if got := psRoots(t, a); len(got) != 1 || got[idB] != b.Root {
		t.Fatalf("after stop alpha, ps: %v", got)
	}
	a.ExpectExit(a.Run("read", "b.txt", "-i", "beta"), 0)
	if r := a.Run("read", "a.txt", "-i", "alpha"); r.Exit != 3 {
		t.Errorf("stopped name still resolves: %s", a.Transcript(r))
	}

	// A killed process leaves a stale entry that ps reports and removes.
	e, ok := liveEntry(t, b)
	if !ok {
		t.Fatal("beta not live")
	}
	p, _ := os.FindProcess(e.PID)
	p.Kill()
	if !waitFor(5*time.Second, func() bool { return !registry.PIDAlive(e.PID) }) {
		t.Fatal("beta did not die")
	}
	v := ps(t, a)
	if v.Outcome != "empty" || len(v.Data.Processes) != 0 || len(v.Data.Removed) != 1 || v.Data.Removed[0].ID != idB {
		t.Errorf("ps after kill: %+v", v)
	}
	reg := &registry.Registry{Dir: filepath.Join(a.Home, ".lino", "run")}
	if _, err := os.Stat(reg.EntryPath(idB)); !os.IsNotExist(err) {
		t.Errorf("stale entry not removed: %v", err)
	}
	if v := ps(t, a); len(v.Data.Removed) != 0 {
		t.Errorf("second ps removed again: %+v", v)
	}
}

func TestLiveIdleExit(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	h.Write("a.txt", "one\n")
	h.ExpectExit(h.Run("init"), 0)
	stopOnCleanup(t, h)

	v := parseRun(t, h.Run("run", "--idle", "1s", "--json"))
	pid := v.Data.PID
	if !registry.PIDAlive(pid) {
		t.Fatalf("pid %d not alive", pid)
	}

	// Calls keep it alive past the idle limit.
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(300 * time.Millisecond) {
		h.ExpectExit(h.Run("read", "a.txt"), 0)
		if !registry.PIDAlive(pid) {
			t.Fatal("process exited while in use")
		}
	}

	start := time.Now()
	if !waitFor(5*time.Second, func() bool { return !registry.PIDAlive(pid) }) {
		t.Fatal("process did not exit when idle")
	}
	if d := time.Since(start); d < 700*time.Millisecond {
		t.Errorf("exited %v after last call, before the 1s idle limit", d)
	}
	if _, ok := liveEntry(t, h); ok {
		t.Error("registry still lists the idle-exited process")
	}
	id := registry.IDFor(h.Root)
	if b, _ := os.ReadFile(filepath.Join(h.Home, ".lino", "run", id+".log")); !strings.Contains(string(b), "idle") {
		t.Errorf("log does not mention the idle exit:\n%s", b)
	}

	// The next call brings it back.
	r := h.Run("read", "a.txt")
	h.ExpectExit(r, 0)
	if !strings.HasPrefix(r.Stderr, "auto-started") {
		t.Errorf("no auto-start after idle exit: %q", r.Stderr)
	}
	if e, ok := liveEntry(t, h); !ok || e.PID == pid {
		t.Errorf("no fresh process after idle exit: %+v", e)
	}
}

func TestLiveIdleInvalid(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	h.ExpectExit(h.Run("init"), 0)
	stopOnCleanup(t, h)
	h.ExpectExit(h.Run("run", "--idle", "soon"), 2)
	if _, ok := liveEntry(t, h); ok {
		t.Error("process started with an invalid --idle")
	}
}

// TestLiveWatcherFreshness checks that external edits reach search within 1s.
func TestLiveWatcherFreshness(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	h.Write("keep.txt", "keep\n").Write("dir/old.txt", "oldtoken\n")
	h.ExpectExit(h.Run("init"), 0)
	startLive(t, h)

	hits := func(q string) string {
		r := h.Run("search", q)
		h.ExpectExit(r, 0)
		return r.Stdout
	}
	steps := []struct {
		name   string
		change func()
		query  string
		want   string // substring of search stdout; "" = no hits
	}{
		{"create", func() { h.Write("new.txt", "freshtoken1\n") }, "freshtoken1", "new.txt"},
		{"create in new dir", func() { h.Write("n1/n2/deep.txt", "deeptoken\n") }, "deeptoken", "n1/n2/deep.txt"},
		{"modify", func() { h.Write("dir/old.txt", "newtoken\n") }, "newtoken", "dir/old.txt"},
		{"modified away", func() {}, "oldtoken", ""},
		{"delete", func() { os.Remove(h.Path("new.txt")) }, "freshtoken1", ""},
		{"rename", func() { os.Rename(h.Path("dir/old.txt"), h.Path("dir/moved.txt")) }, "newtoken", "dir/moved.txt"},
	}
	for _, s := range steps {
		start := time.Now()
		s.change()
		var out string
		ok := waitFor(5*time.Second, func() bool {
			out = hits(s.query)
			if s.want == "" {
				return !strings.Contains(out, "\n  ")
			}
			return strings.Contains(out, s.want+"\n") && (s.name != "rename" || !strings.Contains(out, "dir/old.txt"))
		})
		d := time.Since(start)
		if !ok {
			t.Errorf("%s: not visible after %v:\n%s", s.name, d, out)
			continue
		}
		if d > time.Second {
			t.Errorf("%s: visible after %v, want < 1s", s.name, d)
		}
		t.Logf("%s: visible after %v", s.name, d)
	}
}
