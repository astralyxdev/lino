package changescmd

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/live"
	"github.com/astralyx/lino/internal/proto"
	"github.com/astralyx/lino/internal/registry"
)

func liveCall(t *testing.T, sock string, root string, flags map[string][]string) Data {
	t.Helper()
	c, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	req := proto.NewRequest("changes")
	req.Cwd = root
	req.Flags = flags
	req.JSON = true
	if err := proto.WriteRequest(c, req); err != nil {
		t.Fatal(err)
	}
	resp, err := proto.NewReader(c).ReadResponse()
	if err != nil {
		t.Fatal(err)
	}
	if resp.Exit != 0 {
		t.Fatalf("exit %d: %s %s", resp.Exit, resp.Message, resp.Stderr)
	}
	var env struct {
		Data Data `json:"data"`
	}
	if err := json.Unmarshal([]byte(resp.Stdout), &env); err != nil {
		t.Fatalf("%q: %v", resp.Stdout, err)
	}
	return env.Data
}

func seqs(d Data) []int64 {
	out := []int64{}
	for _, e := range d.Events {
		out = append(out, e.Seq)
	}
	return out
}

func TestChangesDefaultSinceLive(t *testing.T) {
	// Entries 1-3 exist before the process starts and must not be listed.
	root, err := filepath.EvalSymlinks(newRoot(t, "", filler(3)))
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.MkdirTemp("", "lh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	reg := &registry.Registry{Dir: filepath.Join(home, "run")}
	p, err := live.Start(context.Background(), live.Options{Dir: root, Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Stop(context.Background()) })
	sock := reg.SocketPath(p.ID)

	if start, ok := StartSeq(p); !ok || start != 3 {
		t.Fatalf("StartSeq = %d, %v", start, ok)
	}
	if d := liveCall(t, sock, root, nil); len(d.Events) != 0 || d.Next != 3 {
		t.Fatalf("before edits: %+v", d)
	}

	appendLater(t, root, 0, "a.txt", "b.txt")
	if !waitUntil(func() bool { return len(liveCall(t, sock, root, nil).Events) == 2 }) {
		t.Fatalf("after edits: %+v", liveCall(t, sock, root, nil))
	}
	d := liveCall(t, sock, root, nil)
	if got := seqs(d); got[0] != 4 || got[1] != 5 || d.Next != 5 {
		t.Errorf("events %v next %d", got, d.Next)
	}
	// An explicit --since still wins.
	if d := liveCall(t, sock, root, map[string][]string{"since": {"4"}}); len(d.Events) != 1 {
		t.Errorf("--since 4: %v", seqs(d))
	}

	t.Run("wait without since", func(t *testing.T) {
		// Two events after start already exist, so --wait answers at once.
		if d := liveCall(t, sock, root, map[string][]string{"wait": {"5s"}}); len(d.Events) != 2 {
			t.Errorf("got %v", seqs(d))
		}
		// With a path filter nothing matches yet; a later append wakes it.
		appendLater(t, root, 100*time.Millisecond, "later/c.txt")
		d := liveCall(t, sock, root, map[string][]string{"wait": {"5s"}, "path": {"later/**"}})
		if got := seqs(d); len(got) != 1 || got[0] != 6 {
			t.Errorf("got %v", got)
		}
	})
}

func waitUntil(cond func() bool) bool {
	for i := 0; i < 100; i++ {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
