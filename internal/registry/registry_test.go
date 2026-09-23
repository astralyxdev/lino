package registry

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/outcome"
)

// newReg uses a short temp dir: unix socket paths are limited to ~104 bytes.
func newReg(t *testing.T) *Registry {
	t.Helper()
	d, err := os.MkdirTemp("", "lr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return &Registry{Dir: filepath.Join(d, "run")}
}

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func listen(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
}

type fake struct {
	id, name  string
	pid       func(t *testing.T) int
	listening bool
}

func (f fake) add(t *testing.T, r *Registry) Entry {
	t.Helper()
	e := Entry{ID: f.id, Name: f.name, PID: f.pid(t), Root: "/r/" + f.id, Socket: r.SocketPath(f.id),
		Started: time.Unix(1700000000, 0).UTC(), Version: "test"}
	if f.listening {
		listen(t, e.Socket)
	}
	if err := r.Write(e); err != nil {
		t.Fatal(err)
	}
	return e
}

func self(*testing.T) int { return os.Getpid() }

func TestCheckAndClean(t *testing.T) {
	r := newReg(t)
	tests := []struct {
		f    fake
		want State
	}{
		{fake{id: "aaaaaa", pid: self, listening: true}, Live},
		{fake{id: "bbbbbb", pid: deadPID, listening: true}, DeadPID},
		{fake{id: "cccccc", pid: self}, DeadSocket},
		{fake{id: "dddddd", pid: func(*testing.T) int { return 0 }}, DeadPID},
	}
	for _, tt := range tests {
		e := tt.f.add(t, r)
		t.Run(tt.f.id, func(t *testing.T) {
			got, err := r.Read(e.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got != e {
				t.Fatalf("read %+v, want %+v", got, e)
			}
			if s := Check(got); s != tt.want {
				t.Fatalf("Check = %v, want %v", s, tt.want)
			}
		})
	}
	fi, err := os.Stat(r.EntryPath("aaaaaa"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("entry mode: %v %v", fi, err)
	}
	removed, err := r.Clean()
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 3 {
		t.Fatalf("removed %d, want 3", len(removed))
	}
	left, _ := r.List()
	if len(left) != 1 || left[0].ID != "aaaaaa" {
		t.Fatalf("left %+v", left)
	}
	for _, id := range []string{"bbbbbb", "cccccc"} {
		if _, err := os.Stat(r.SocketPath(id)); !os.IsNotExist(err) {
			t.Errorf("socket %s not removed: %v", id, err)
		}
	}
}

func TestLookup(t *testing.T) {
	r := newReg(t)
	fake{id: "aaaaaa", name: "web", pid: self, listening: true}.add(t, r)
	fake{id: "bbbbbb", name: "api", pid: deadPID}.add(t, r)
	tests := []struct {
		in     string
		wantID string
		want   outcome.Outcome
	}{
		{"aaaaaa", "aaaaaa", outcome.OK},
		{"web", "aaaaaa", outcome.OK},
		{"api", "bbbbbb", outcome.OK},
		{"zzzzzz", "", outcome.NotFound},
		{"nope", "", outcome.NotFound},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			e, err := r.Lookup(tt.in)
			if outcome.Of(err) != tt.want {
				t.Fatalf("err = %v, want %s", err, tt.want)
			}
			if e.ID != tt.wantID {
				t.Fatalf("id = %q, want %q", e.ID, tt.wantID)
			}
		})
	}
}

func TestNames(t *testing.T) {
	r := newReg(t)
	fake{id: "aaaaaa", name: "web", pid: self, listening: true}.add(t, r)
	fake{id: "bbbbbb", name: "old", pid: deadPID}.add(t, r)
	tests := []struct {
		name string
		e    Entry
		want outcome.Outcome
	}{
		{"duplicate live name", Entry{ID: "cccccc", Name: "web"}, outcome.Refused},
		{"name reused from stale", Entry{ID: "cccccc", Name: "old"}, outcome.OK},
		{"same id keeps its name", Entry{ID: "aaaaaa", Name: "web"}, outcome.OK},
		{"name equals other id", Entry{ID: "dddddd", Name: "aaaaaa"}, outcome.Refused},
		{"invalid name", Entry{ID: "dddddd", Name: "a b"}, outcome.Usage},
		{"invalid id", Entry{ID: "BAD"}, outcome.Usage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := outcome.Of(r.Write(tt.e)); got != tt.want {
				t.Fatalf("Write = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestEmptyRegistry(t *testing.T) {
	r := newReg(t)
	if l, err := r.List(); err != nil || l != nil {
		t.Fatalf("List = %v, %v", l, err)
	}
	if err := r.Remove("aaaaaa"); err != nil {
		t.Fatal(err)
	}
}

func TestDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	r, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if r.Dir != filepath.Join(home, ".lino", "run") {
		t.Fatalf("Dir = %s", r.Dir)
	}
}

func TestConcurrentNameClaim(t *testing.T) {
	r := newReg(t)
	ids := []string{"aaaaa1", "aaaaa2", "aaaaa3", "aaaaa4", "aaaaa5", "aaaaa6", "aaaaa7", "aaaaa8"}
	errs := make(chan error, len(ids))
	start := make(chan struct{})
	for _, id := range ids {
		go func() {
			<-start
			errs <- r.Write(Entry{ID: id, Name: "shared", PID: os.Getpid(), Root: "/r/" + id,
				Socket: r.SocketPath(id), Started: time.Now(), Version: "test"})
		}()
	}
	close(start)
	wins := 0
	for range ids {
		err := <-errs
		switch {
		case err == nil:
			wins++
		case !outcome.Is(err, outcome.Refused):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("wins = %d, want exactly 1", wins)
	}
}

func TestNameClaimAcrossProcesses(t *testing.T) {
	if dir := os.Getenv("LINO_REG_CHILD_DIR"); dir != "" {
		id := os.Getenv("LINO_REG_CHILD_ID")
		r := &Registry{Dir: dir}
		err := r.Write(Entry{ID: id, Name: "shared", PID: os.Getppid(), Root: "/r/" + id,
			Socket: r.SocketPath(id), Started: time.Now(), Version: "test"})
		if err != nil {
			os.Exit(7)
		}
		os.Exit(0)
	}
	r := newReg(t)
	ids := []string{"bbbbb1", "bbbbb2", "bbbbb3", "bbbbb4", "bbbbb5", "bbbbb6"}
	cmds := make([]*exec.Cmd, len(ids))
	for i, id := range ids {
		c := exec.Command(os.Args[0], "-test.run=^TestNameClaimAcrossProcesses$")
		c.Env = append(os.Environ(), "LINO_REG_CHILD_DIR="+r.Dir, "LINO_REG_CHILD_ID="+id)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		cmds[i] = c
	}
	wins := 0
	for _, c := range cmds {
		err := c.Wait()
		if err == nil {
			wins++
		} else if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 7 {
			t.Fatalf("child: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("wins = %d, want exactly 1", wins)
	}
}
