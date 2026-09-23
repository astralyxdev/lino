package live

import (
	"context"
	"flag"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/proto"
	"github.com/astralyx/lino/internal/registry"
)

// slowCommands has one command, "hold", that blocks for the given duration,
// standing in for a waiting read such as changes --wait.
func slowCommands() *cli.Registry {
	r := cli.NewRegistry()
	r.Register(&cli.Command{
		Name: "hold", MinArgs: 1, MaxArgs: 1,
		Setup: func(*flag.FlagSet) cli.RunFunc {
			return func(ctx context.Context, c *cli.Call) (output.Result, error) {
				d, _ := time.ParseDuration(c.Arg(0))
				select {
				case <-time.After(d):
				case <-ctx.Done():
				}
				return output.Result{Data: "held"}, nil
			}
		},
	})
	return r
}

func startIdle(t *testing.T) (*Process, *registry.Registry) {
	t.Helper()
	root := initRoot(t, map[string]string{"a.txt": "a\n"})
	reg := &registry.Registry{Dir: filepath.Join(shortTemp(t, "lh"), "run")}
	p, err := Start(context.Background(), Options{Dir: root, Registry: reg, Commands: slowCommands()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Stop(context.Background()) })
	return p, reg
}

func stoppedWithin(p *Process, d time.Duration) bool {
	select {
	case <-p.Done():
		return true
	case <-time.After(d):
		return false
	}
}

func TestIdleExit(t *testing.T) {
	tests := []struct {
		name     string
		idle     time.Duration
		hold     time.Duration // request in flight when the idle period passes
		stayFor  time.Duration // must still be running after this long
		stopWith time.Duration // must have stopped within this long after stayFor
	}{
		{"exits", 150 * time.Millisecond, 0, 0, 2 * time.Second},
		{"never", 0, 0, 600 * time.Millisecond, 0},
		{"waiting_request_keeps_alive", 150 * time.Millisecond, 700 * time.Millisecond, 600 * time.Millisecond, 2 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, reg := startIdle(t)
			sock := reg.SocketPath(p.ID)
			started := time.Now()
			p.WatchIdle(tt.idle)
			held := make(chan *proto.Response, 1)
			if tt.hold > 0 {
				go func() {
					held <- call(t, sock, &proto.Request{Version: proto.Version, Command: "hold", Args: []string{tt.hold.String()}})
				}()
			}
			if tt.stayFor > 0 && stoppedWithin(p, tt.stayFor) {
				t.Fatalf("stopped after %s, want running", time.Since(started))
			}
			if tt.stopWith == 0 {
				if p.IdleExited() {
					t.Fatal("IdleExited with idle 0")
				}
				return
			}
			if !stoppedWithin(p, tt.stopWith) {
				t.Fatalf("still running after %s", time.Since(started))
			}
			if !p.IdleExited() {
				t.Fatal("IdleExited = false")
			}
			if tt.hold > 0 {
				if r := <-held; !r.OK {
					t.Fatalf("held request: %+v", r)
				}
				if el := time.Since(started); el < tt.hold+tt.idle {
					t.Fatalf("stopped after %s, before hold+idle", el)
				}
			}
			if _, err := reg.Read(p.ID); err == nil {
				t.Fatal("registry entry left after idle exit")
			}
		})
	}
}

func TestIdleResetByRequests(t *testing.T) {
	p, reg := startIdle(t)
	sock := reg.SocketPath(p.ID)
	p.WatchIdle(300 * time.Millisecond)
	for range 6 {
		time.Sleep(100 * time.Millisecond)
		call(t, sock, &proto.Request{Version: proto.Version, Command: "hold", Args: []string{"1ms"}})
	}
	if stoppedWithin(p, 0) {
		t.Fatal("stopped while requests kept arriving")
	}
	if !stoppedWithin(p, 2*time.Second) {
		t.Fatal("did not stop once requests ceased")
	}
}

func TestRunForegroundIdle(t *testing.T) {
	root := initRoot(t, map[string]string{"a.txt": "a\n"})
	reg := &registry.Registry{Dir: filepath.Join(shortTemp(t, "lh"), "run")}
	var errOut strings.Builder
	old := Stderr
	Stderr = &errOut
	defer func() { Stderr = old }()
	done := make(chan output.Result, 1)
	go func() {
		res, err := Run(context.Background(), RunRequest{Dir: root, Idle: "200ms", Foreground: true, Registry: reg})
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	select {
	case res := <-done:
		if res.Message != "stopped after 200ms idle" {
			t.Fatalf("message %q", res.Message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run --idle 200ms did not exit")
	}
}
