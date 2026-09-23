package server

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/proto"
)

// shortDir keeps socket paths under the 104-byte macOS limit.
func shortDir(t *testing.T) string {
	d, err := os.MkdirTemp("/tmp", "lino")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func start(t *testing.T, s *Server) string {
	t.Helper()
	path := filepath.Join(shortDir(t), "run", "x.sock")
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- s.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		if err := <-errc; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	return path
}

func call(t *testing.T, path string, req *proto.Request) *proto.Response {
	t.Helper()
	c, err := net.Dial("unix", path)
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

func TestSocketPerms(t *testing.T) {
	path := start(t, &Server{Handler: echo})
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSocket == 0 || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", st.Mode())
	}
	dst, _ := os.Stat(filepath.Dir(path))
	if dst.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v", dst.Mode())
	}
	if _, err := Listen(path); outcome.Of(err) != outcome.LiveExists {
		t.Fatalf("second listen: %v", err)
	}
}

func TestListenRemovesStaleSocket(t *testing.T) {
	path := filepath.Join(shortDir(t), "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	ln2, err := Listen(path)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	ln2.Close()
}

func echo(_ context.Context, req *proto.Request) (output.Result, error) {
	return output.Result{Data: req.Command}, nil
}

func TestConcurrentReads(t *testing.T) {
	const n = 8
	var inside atomic.Int32
	release := make(chan struct{})
	s := &Server{Handler: func(ctx context.Context, req *proto.Request) (output.Result, error) {
		if inside.Add(1) == n {
			close(release)
		}
		select {
		case <-release:
		case <-time.After(3 * time.Second):
			return output.Result{}, outcome.New(outcome.Internal, "reads not concurrent")
		}
		return output.Result{Data: "ok"}, nil
	}}
	path := start(t, s)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if resp := call(t, path, proto.NewRequest("read", "a")); resp.Outcome != outcome.OK {
				t.Errorf("resp = %+v", resp)
			}
		}()
	}
	wg.Wait()
}

func TestMutationsSerialised(t *testing.T) {
	var active, maxActive, total atomic.Int32
	s := &Server{Handler: func(ctx context.Context, req *proto.Request) (output.Result, error) {
		a := active.Add(1)
		for {
			m := maxActive.Load()
			if a <= m || maxActive.CompareAndSwap(m, a) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		total.Add(1)
		active.Add(-1)
		return output.Result{Outcome: outcome.Updated, Data: "done"}, nil
	}}
	path := start(t, s)
	var wg sync.WaitGroup
	for _, cmd := range []string{"edit", "insert", "delete", "replace", "write", "mv", "rm", "rollback", "edit", "write"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if resp := call(t, path, proto.NewRequest(cmd, "f")); resp.Outcome != outcome.Updated {
				t.Errorf("%s: %+v", cmd, resp)
			}
		}()
	}
	wg.Wait()
	if maxActive.Load() != 1 || total.Load() != 10 {
		t.Fatalf("max concurrent mutations = %d, total = %d", maxActive.Load(), total.Load())
	}
}

func TestBadInput(t *testing.T) {
	s := &Server{Handler: func(ctx context.Context, req *proto.Request) (output.Result, error) {
		if req.Command == "boom" {
			panic("kaboom")
		}
		if req.Command == "missing" {
			return output.Result{}, outcome.New(outcome.NotFound, "no such file")
		}
		return output.Result{Data: req.Command}, nil
	}}
	path := start(t, s)
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := proto.NewReader(c)
	tests := []struct {
		name string
		line string
		want outcome.Outcome
		exit int
	}{
		{"not json", "{nope\n", outcome.Internal, 1},
		{"no version", `{"command":"read"}` + "\n", outcome.Internal, 1},
		{"wrong version", `{"v":99,"command":"read"}` + "\n", outcome.Internal, 1},
		{"panic", `{"v":1,"command":"boom"}` + "\n", outcome.Internal, 1},
		{"handler error", `{"v":1,"command":"missing"}` + "\n", outcome.NotFound, 3},
		{"still alive", `{"v":1,"command":"read"}` + "\n", outcome.OK, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := c.Write([]byte(tt.line)); err != nil {
				t.Fatal(err)
			}
			resp, err := r.ReadResponse()
			if err != nil {
				t.Fatal(err)
			}
			if resp.Outcome != tt.want || resp.Exit != tt.exit || resp.OK != (tt.exit == 0) {
				t.Fatalf("resp = %+v", resp)
			}
		})
	}
}

func TestShutdownWaitsForInFlight(t *testing.T) {
	started := make(chan struct{})
	var finished, hook atomic.Bool
	var cancelled atomic.Bool
	s := &Server{
		Handler: func(ctx context.Context, req *proto.Request) (output.Result, error) {
			if req.Command == "changes" {
				<-ctx.Done()
				cancelled.Store(true)
				return output.Result{Outcome: outcome.Empty}, nil
			}
			close(started)
			time.Sleep(50 * time.Millisecond)
			if ctx.Err() != nil {
				t.Error("mutation context cancelled")
			}
			finished.Store(true)
			return output.Result{Outcome: outcome.Updated}, nil
		},
		OnShutdown: func() { hook.Store(true) },
	}
	path := filepath.Join(shortDir(t), "s.sock")
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve(ln)
	idle, err := net.Dial("unix", path) // idle connection must not block shutdown
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if resp := call(t, path, proto.NewRequest("edit")); resp.Outcome != outcome.Updated {
			t.Errorf("edit: %+v", resp)
		}
	}()
	<-started
	go func() {
		defer wg.Done()
		if resp := call(t, path, proto.NewRequest("changes")); resp.Outcome != outcome.Empty {
			t.Errorf("changes: %+v", resp)
		}
	}()
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if !finished.Load() || !hook.Load() || !cancelled.Load() {
		t.Fatalf("finished=%v hook=%v cancelled=%v", finished.Load(), hook.Load(), cancelled.Load())
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("Done not closed")
	}
	if _, err := net.Dial("unix", path); err == nil {
		t.Fatal("still accepting after shutdown")
	}
}
