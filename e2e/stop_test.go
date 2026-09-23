package e2e

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/proto"
	"github.com/astralyx/lino/internal/registry"
)

// startLive runs `lino run --foreground` in h and waits until it serves.
// The returned channel yields the process's exit error.
func startLive(t *testing.T, h *Harness) (*exec.Cmd, <-chan error) {
	t.Helper()
	cmd := exec.Command(linoBin, "run", "--foreground")
	cmd.Dir = h.Root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + h.Home, "LANG=C"}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stderr)
		once := sync.Once{}
		for sc.Scan() {
			if strings.Contains(sc.Text(), " serving ") {
				once.Do(func() { close(ready) })
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(15 * time.Second):
		t.Fatal("process did not become ready")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return cmd, done
}

func expectGone(t *testing.T, h *Harness, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("process exit: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("process did not exit")
	}
	reg := &registry.Registry{Dir: filepath.Join(h.Home, ".lino", "run")}
	id := registry.IDFor(h.Root)
	if _, err := os.Stat(reg.EntryPath(id)); !os.IsNotExist(err) {
		t.Errorf("registry entry left behind: %v", err)
	}
	if _, err := os.Stat(reg.SocketPath(id)); !os.IsNotExist(err) {
		t.Errorf("socket left behind: %v", err)
	}
}

func TestStopCommand(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	h.Write("a.txt", "one\n").Mkdir("sub")
	h.ExpectExit(h.Run("init"), 0)

	r := h.Run("stop")
	h.ExpectExit(r, 8)

	_, done := startLive(t, h)
	r = h.Exec(Cmd{Args: []string{"stop"}, Dir: "sub"})
	h.ExpectExit(r, 0)
	if !strings.HasPrefix(r.Stdout, "stopped "+registry.IDFor(h.Root)+" ") {
		t.Errorf("stdout %q", r.Stdout)
	}
	expectGone(t, h, done)
	h.ExpectExit(h.Run("stop"), 8)
}

// TestSIGTERMMidMutation signals the process while large writes are in
// flight: it must exit cleanly with the file holding one complete version.
func TestSIGTERMMidMutation(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	a := strings.Repeat("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n", 100_000)
	b := strings.Repeat("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n", 100_000)
	h.Write("big.txt", a)
	h.ExpectExit(h.Run("init"), 0)
	cmd, done := startLive(t, h)
	sock := (&registry.Registry{Dir: filepath.Join(h.Home, ".lino", "run")}).SocketPath(registry.IDFor(h.Root))

	var wg sync.WaitGroup
	var written atomic.Int32
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; ; n++ {
				c, err := net.DialTimeout("unix", sock, time.Second)
				if err != nil {
					return
				}
				req := proto.NewRequest("write", "big.txt")
				req.Cwd = h.Root
				req.Flags = map[string][]string{"force": {"true"}}
				req.Stdin = []byte(a)
				if (i+n)%2 == 1 {
					req.Stdin = []byte(b)
				}
				err = proto.WriteRequest(c, req)
				if err == nil {
					var resp *proto.Response
					if resp, err = proto.NewReader(c).ReadResponse(); err == nil && resp.Exit == 0 {
						written.Add(1)
					}
				}
				c.Close()
				if err != nil {
					return
				}
			}
		}(i)
	}
	time.Sleep(300 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	expectGone(t, h, done)
	wg.Wait()

	if written.Load() == 0 {
		t.Fatal("no write completed before the signal")
	}
	if got := h.Read("big.txt"); got != a && got != b {
		t.Errorf("big.txt is torn: %d bytes", len(got))
	}
	if tree := h.Tree(); len(tree) != 1 || tree[0] != "big.txt" {
		t.Errorf("leftover files: %v", tree)
	}
}
