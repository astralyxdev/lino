package e2e

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/proto"
	"github.com/astralyx/lino/internal/registry"
)

// shortHome keeps socket paths under the ~104-byte unix limit.
func shortHome(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "lh")
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

func TestRunForeground(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	h.Write("a.txt", "one\ntwo\n")
	h.ExpectExit(h.Run("init"), 0)

	cmd := exec.Command(linoBin, "run", "--foreground", "--name", "e2e")
	cmd.Dir = h.Root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + h.Home, "LANG=C"}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })

	ready := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if strings.Contains(sc.Text(), " serving ") {
				ready <- sc.Text()
			}
		}
	}()
	select {
	case line := <-ready:
		if !strings.Contains(line, h.Root) {
			t.Fatalf("ready line %q", line)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("process did not become ready")
	}

	id := registry.IDFor(h.Root)
	reg := &registry.Registry{Dir: filepath.Join(h.Home, ".lino", "run")}
	e, err := reg.Lookup("e2e")
	if err != nil || e.ID != id || e.PID != cmd.Process.Pid {
		t.Fatalf("registry entry %+v: %v", e, err)
	}

	c, err := net.DialTimeout("unix", reg.SocketPath(id), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	req := proto.NewRequest("read", "a.txt")
	req.Cwd = h.Root
	req.Flags = map[string][]string{"lines": {"2"}}
	if err := proto.WriteRequest(c, req); err != nil {
		t.Fatal(err)
	}
	resp, err := proto.NewReader(c).ReadResponse()
	c.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.Exit != 0 || !strings.HasPrefix(resp.Stdout, "a.txt v=") || !strings.HasSuffix(resp.Stdout, "lines 2-2 of 2\ntwo\n") {
		t.Fatalf("response %+v", resp)
	}

	// A second foreground run finds the live one and exits 0 with its id.
	again := h.Run("run", "--foreground")
	h.ExpectExit(again, 0)
	if strings.TrimSpace(again.Stdout) != id {
		t.Fatalf("second run stdout %q", again.Stdout)
	}

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if errors.As(err, &ee) || err != nil {
			t.Fatalf("exit after SIGINT: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("process did not exit after SIGINT")
	}
	if strings.TrimSpace(stdout.String()) != id {
		t.Fatalf("stdout %q", stdout.String())
	}
	if _, err := os.Stat(reg.EntryPath(id)); !os.IsNotExist(err) {
		t.Fatalf("registry entry left behind: %v", err)
	}
	if _, err := os.Stat(reg.SocketPath(id)); !os.IsNotExist(err) {
		t.Fatalf("socket left behind: %v", err)
	}
}
