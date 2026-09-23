package pscmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/registry"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// newReg uses a short temp dir: unix socket paths are limited to ~104 bytes.
func newReg(t *testing.T) *registry.Registry {
	t.Helper()
	d, err := os.MkdirTemp("", "lp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return &registry.Registry{Dir: filepath.Join(d, "run")}
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
	dead      bool
	listening bool
	age       time.Duration
}

func add(t *testing.T, r *registry.Registry, f fake) {
	t.Helper()
	pid := os.Getpid()
	if f.dead {
		pid = deadPID(t)
	}
	e := registry.Entry{ID: f.id, Name: f.name, PID: pid, Root: "/r/" + f.id,
		Socket: r.SocketPath(f.id), Started: t0.Add(-f.age), Version: "test"}
	if err := r.Write(e); err != nil {
		t.Fatal(err)
	}
	if f.listening {
		listen(t, e.Socket)
	}
}

func run(t *testing.T, r *registry.Registry, args ...string) (int, string, string) {
	t.Helper()
	reg := cli.NewRegistry()
	reg.Register(Command(func() (*registry.Registry, error) { return r, nil }, func() time.Time { return t0 }))
	var out, errw bytes.Buffer
	code := reg.Main(context.Background(), append([]string{"ps"}, args...), cli.Env{}, &out, &errw)
	return code, out.String(), errw.String()
}

func TestPS(t *testing.T) {
	tests := []struct {
		name        string
		fakes       []fake
		wantCode    int
		wantOut     []string
		wantErr     []string
		wantLeft    []string
		wantRemoved []string
	}{
		{name: "no registry", wantCode: 0, wantErr: []string{"no running processes"}},
		{
			name: "live only",
			fakes: []fake{
				{id: "aaaaaa", name: "web", listening: true, age: 2*time.Hour + 5*time.Minute},
				{id: "bbbbbb", listening: true, age: 42 * time.Second},
			},
			wantOut:  []string{"ID  ", "aaaaaa  web", "/r/aaaaaa", "2h05m", "bbbbbb  -", "42s"},
			wantLeft: []string{"aaaaaa", "bbbbbb"},
		},
		{
			name: "live and stale",
			fakes: []fake{
				{id: "aaaaaa", listening: true, age: time.Minute},
				{id: "dddddd", dead: true},
				{id: "ssssss"},
			},
			wantOut:     []string{"aaaaaa", "1m00s"},
			wantErr:     []string{"removed stale dddddd (dead pid", "removed stale ssssss (dead socket"},
			wantLeft:    []string{"aaaaaa"},
			wantRemoved: []string{"dddddd", "ssssss"},
		},
		{
			name:        "stale only",
			fakes:       []fake{{id: "dddddd", dead: true}},
			wantErr:     []string{"removed stale dddddd", "no running processes"},
			wantRemoved: []string{"dddddd"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newReg(t)
			for _, f := range tt.fakes {
				add(t, r, f)
			}
			code, out, errs := run(t, r)
			if code != tt.wantCode {
				t.Errorf("exit = %d, want %d", code, tt.wantCode)
			}
			for _, w := range tt.wantOut {
				if !strings.Contains(out, w) {
					t.Errorf("stdout missing %q:\n%s", w, out)
				}
			}
			if len(tt.wantOut) == 0 && out != "" {
				t.Errorf("stdout = %q, want empty", out)
			}
			for _, w := range tt.wantErr {
				if !strings.Contains(errs, w) {
					t.Errorf("stderr missing %q:\n%s", w, errs)
				}
			}
			left, err := r.List()
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, e := range left {
				ids = append(ids, e.ID)
			}
			if strings.Join(ids, ",") != strings.Join(tt.wantLeft, ",") {
				t.Errorf("entries left = %v, want %v", ids, tt.wantLeft)
			}
			for _, id := range tt.wantRemoved {
				if _, err := os.Stat(r.EntryPath(id)); !os.IsNotExist(err) {
					t.Errorf("%s entry still on disk", id)
				}
			}
		})
	}
}

func TestPSJSON(t *testing.T) {
	r := newReg(t)
	add(t, r, fake{id: "aaaaaa", name: "web", listening: true, age: 90 * time.Second})
	add(t, r, fake{id: "dddddd", dead: true})

	code, out, _ := run(t, r, "--json")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var env struct {
		OK      bool   `json:"ok"`
		Outcome string `json:"outcome"`
		Data    Result `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !env.OK || env.Outcome != "ok" {
		t.Errorf("ok=%v outcome=%q", env.OK, env.Outcome)
	}
	if len(env.Data.Processes) != 1 || env.Data.Processes[0].ID != "aaaaaa" || env.Data.Processes[0].Uptime != 90 || env.Data.Processes[0].Name != "web" {
		t.Errorf("processes = %+v", env.Data.Processes)
	}
	if len(env.Data.Removed) != 1 || env.Data.Removed[0].ID != "dddddd" || env.Data.Removed[0].Reason != "dead pid" {
		t.Errorf("removed = %+v", env.Data.Removed)
	}

	code, out, _ = run(t, newReg(t), "--json")
	if code != 0 || !strings.Contains(out, `"outcome":"empty"`) {
		t.Errorf("empty registry: exit %d, %s", code, out)
	}
}

func TestPSRejectsArgsAndID(t *testing.T) {
	for _, args := range [][]string{{"extra"}, {"-i", "abc"}} {
		if code, _, _ := run(t, newReg(t), args...); code != 2 {
			t.Errorf("ps %v: exit %d, want 2", args, code)
		}
	}
}

func TestFormatUptime(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{59 * time.Second, "59s"},
		{time.Minute, "1m00s"},
		{3*time.Minute + 12*time.Second, "3m12s"},
		{2*time.Hour + 5*time.Minute, "2h05m"},
		{3*24*time.Hour + 4*time.Hour + 30*time.Minute, "3d04h"},
	}
	for _, tt := range tests {
		if got := FormatUptime(tt.d); got != tt.want {
			t.Errorf("FormatUptime(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
