package metrics

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/initcmd"
	"github.com/astralyx/lino/internal/live"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/proto"
	"github.com/astralyx/lino/internal/registry"
	_ "github.com/astralyx/lino/internal/reindex"
	"github.com/astralyx/lino/internal/version"
)

func shortTemp(t *testing.T, prefix string) string {
	t.Helper()
	d, err := os.MkdirTemp("", prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	d, _ = filepath.EvalSymlinks(d)
	return d
}

func call(t *testing.T, sock string, req *proto.Request) *proto.Response {
	t.Helper()
	c, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := proto.WriteRequest(c, req); err != nil {
		t.Fatal(err)
	}
	resp, err := proto.NewReader(c).ReadResponse()
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestLiveCountersSurviveRestart(t *testing.T) {
	ctx := context.Background()
	root := shortTemp(t, "lr")
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\ntwo\n"), 0o644)
	if _, err := initcmd.Init(ctx, root, root); err != nil {
		t.Fatal(err)
	}
	reg := &registry.Registry{Dir: filepath.Join(shortTemp(t, "lh"), "run")}

	startAndCall := func(reqs ...*proto.Request) {
		p, err := live.Start(ctx, live.Options{Dir: root, Registry: reg})
		if err != nil {
			t.Fatal(err)
		}
		sock := reg.SocketPath(p.ID)
		for _, req := range reqs {
			req.Cwd = root
			call(t, sock, req)
		}
		if err := p.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		if For(p) != nil {
			t.Fatal("collector kept after stop")
		}
	}

	read := proto.NewRequest("read", "a.txt")
	missing := proto.NewRequest("read", "nope.txt")
	edit := proto.NewRequest("edit", "a.txt", "1", "1")
	edit.Flags = map[string][]string{"v": {version.Of([]byte("one\ntwo\n"))}}
	edit.Stdin = []byte("ONE\n")
	startAndCall(read, missing, edit)
	startAndCall(proto.NewRequest("read", "a.txt"))

	s := Load(Path(root)).Snapshot()
	r := s.Commands["read"]
	if s.Starts != 2 || r == nil || r.Count != 3 || r.Outcomes[outcome.NotFound] != 1 || r.Latency.Count != 3 {
		t.Fatalf("read stats %+v (starts %d)", r, s.Starts)
	}
	if e := s.Commands["edit"]; e == nil || e.Outcomes[outcome.Updated] != 1 {
		t.Fatalf("edit stats %+v", e)
	}
	if s.Reindex.Count != 1 {
		t.Fatalf("reindex count %d", s.Reindex.Count)
	}
}
