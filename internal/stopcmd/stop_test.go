package stopcmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/cli"
	_ "github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/initcmd"
	"github.com/astralyx/lino/internal/live"
	"github.com/astralyx/lino/internal/registry"
)

// shortTemp keeps socket paths under the ~104-byte unix limit.
func shortTemp(t *testing.T, prefix string) string {
	t.Helper()
	d, err := os.MkdirTemp("", prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	d, err = filepath.EvalSymlinks(d)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func initRoot(t *testing.T) string {
	t.Helper()
	root := shortTemp(t, "sr")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := initcmd.Init(context.Background(), root, root); err != nil {
		t.Fatal(err)
	}
	return root
}

func run(t *testing.T, reg *registry.Registry, cwd string, env map[string]string, args ...string) (string, string, int) {
	t.Helper()
	cmds := cli.NewRegistry()
	cmds.Register(Command(func() (*registry.Registry, error) { return reg, nil }))
	var o, e bytes.Buffer
	code := cmds.Main(context.Background(), args, cli.Env{Cwd: cwd, Getenv: func(k string) string { return env[k] }}, &o, &e)
	return o.String(), e.String(), code
}

func TestStop(t *testing.T) {
	tests := []struct {
		name string
		cwd  string // relative to root
		env  map[string]string
		args []string
	}{
		{"from root", "", nil, []string{"stop"}},
		{"from subdir", "sub", nil, []string{"stop"}},
		{"by name", "", nil, []string{"stop", "-i", "proj"}},
		{"by LINO_ID", "", map[string]string{"LINO_ID": "proj"}, []string{"stop"}},
		{"json", "", nil, []string{"stop", "--json"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := initRoot(t)
			reg := &registry.Registry{Dir: filepath.Join(shortTemp(t, "sh"), "run")}
			p, err := live.Start(context.Background(), live.Options{Dir: root, Name: "proj", Registry: reg})
			if err != nil {
				t.Fatal(err)
			}
			cwd := filepath.Join(root, tt.cwd)
			if tt.env != nil || len(tt.args) > 1 && tt.args[1] == "-i" {
				cwd = shortTemp(t, "elsewhere")
			}
			out, errOut, code := run(t, reg, cwd, tt.env, tt.args...)
			if code != 0 {
				t.Fatalf("exit %d: %s%s", code, out, errOut)
			}
			if !strings.Contains(out, p.ID) {
				t.Errorf("stdout %q lacks id %s", out, p.ID)
			}
			select {
			case <-p.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("process not stopped")
			}
			if _, err := os.Stat(reg.EntryPath(p.ID)); !os.IsNotExist(err) {
				t.Errorf("registry entry left: %v", err)
			}
			if _, err := os.Stat(reg.SocketPath(p.ID)); !os.IsNotExist(err) {
				t.Errorf("socket left: %v", err)
			}
			if _, _, code := run(t, reg, root, nil, "stop"); code != 8 {
				t.Errorf("second stop exit %d, want 8", code)
			}
		})
	}
}

func TestStopNotRunning(t *testing.T) {
	reg := &registry.Registry{Dir: filepath.Join(shortTemp(t, "sh"), "run")}
	tests := []struct {
		name string
		cwd  string
		args []string
		code int
	}{
		{"initialised, no process", initRoot(t), []string{"stop"}, 8},
		{"uninitialised dir", shortTemp(t, "un"), []string{"stop"}, 8},
		{"unknown id", initRoot(t), []string{"stop", "-i", "zzzzzz"}, 3},
		{"extra arg", initRoot(t), []string{"stop", "x"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errOut, code := run(t, reg, tt.cwd, nil, tt.args...)
			if code != tt.code {
				t.Errorf("exit %d, want %d: %s%s", code, tt.code, out, errOut)
			}
		})
	}
}
