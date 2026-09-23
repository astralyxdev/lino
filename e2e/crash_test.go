package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/registry"
	"github.com/astralyx/lino/internal/version"
)

// The index skips fsync; a live process killed mid-edit must still leave an
// index that, after a restart, matches the files exactly.
func TestKillDuringEdits(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	const files = 8
	for i := range files {
		h.Write(fmt.Sprintf("f%d.txt", i), "start\n")
	}
	h.must(0, Cmd{Args: []string{"init"}})
	stopOnCleanup(t, h)
	t.Cleanup(func() { killServers(h.Root) })

	for round := range 3 {
		h.must(0, Cmd{Args: []string{"run"}})
		e, ok := liveEntry(t, h)
		if !ok {
			t.Fatal("no live process")
		}
		var done atomic.Int64
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for w := range files {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; ; i++ {
					select {
					case <-stop:
						return
					default:
					}
					rel := fmt.Sprintf("f%d.txt", w)
					body := fmt.Sprintf("round%d writer%d edit%d\n", round, w, i)
					cur, err := os.ReadFile(h.Path(rel))
					if err != nil {
						continue
					}
					if h.Exec(Cmd{Args: []string{"write", rel, "--v", version.Of(cur)}, Stdin: body}).Exit == 0 {
						done.Add(1)
					}
				}
			}()
		}
		deadline := time.Now().Add(30 * time.Second)
		for done.Load() < 40 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		syscall.Kill(e.PID, syscall.SIGKILL)
		close(stop)
		wg.Wait()
		if done.Load() < 40 {
			t.Fatalf("round %d: only %d edits succeeded", round, done.Load())
		}
		// Writers may have autostarted a new process after the kill.
		h.Run("stop")
		for i := 0; i < 100 && registry.PIDAlive(e.PID); i++ {
			time.Sleep(10 * time.Millisecond)
		}
		// An autostart racing the kill can leave a process stop never saw.
		if n := killServers(h.Root); n > 0 {
			t.Logf("round %d: killed %d stray lino-core", round, n)
		}
	}

	// Restart reconciles, then check the index against the files.
	h.must(0, Cmd{Args: []string{"run"}})
	h.must(0, Cmd{Args: []string{"stop"}})
	if n := killServers(h.Root); n > 0 {
		t.Errorf("%d lino-core still running for %s after stop", n, h.Root)
	}
	ctx := context.Background()
	db, err := index.Open(ctx, h.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := range files {
		rel := fmt.Sprintf("f%d.txt", i)
		want, err := os.ReadFile(filepath.Join(h.Root, rel))
		if err != nil {
			t.Fatal(err)
		}
		var got string
		if err := db.SQL.QueryRowContext(ctx, `SELECT content FROM files WHERE path = ?`, rel).Scan(&got); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if got != string(want) {
			t.Errorf("%s: index %q, file %q", rel, got, want)
		}
		r := h.Run("search", string(want[:len(want)-1]), "--direct")
		if r.Exit != 0 {
			t.Errorf("search %s:\n%s", rel, h.Transcript(r))
		}
	}
	var ok string
	if err := db.SQL.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
		t.Errorf("integrity_check = %q, %v", ok, err)
	}
	if _, err := db.SQL.ExecContext(ctx, `INSERT INTO tri(tri) VALUES ('integrity-check')`); err != nil {
		t.Errorf("fts integrity: %v", err)
	}
}

// serverPIDs lists the `lino-core run <root> --foreground` processes.
func serverPIDs(root string) []int {
	out, _ := exec.Command("ps", "-Ao", "pid=,args=").Output()
	var pids []int
	for _, l := range strings.Split(string(out), "\n") {
		pid, args, _ := strings.Cut(strings.TrimSpace(l), " ")
		if !strings.Contains(args, coreBin()+" run "+root+" --foreground") {
			continue
		}
		if n, err := strconv.Atoi(pid); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}

// killServers SIGKILLs every lino-core serving root, waits for them to exit
// and returns how many there were.
func killServers(root string) int {
	pids := serverPIDs(root)
	for _, pid := range pids {
		syscall.Kill(pid, syscall.SIGKILL)
	}
	for _, pid := range pids {
		for i := 0; i < 200 && registry.PIDAlive(pid); i++ {
			time.Sleep(10 * time.Millisecond)
		}
	}
	return len(pids)
}
