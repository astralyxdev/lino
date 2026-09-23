package live

import (
	"bytes"
	"context"
	"flag"
	"path/filepath"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/proto"
	"github.com/astralyx/lino/internal/registry"
)

func probeCommands(seen *bool) *cli.Registry {
	cmds := cli.NewRegistry()
	cmds.Register(&cli.Command{
		Name: "probe", Summary: "report ctx watched", MaxArgs: 0,
		Setup: func(*flag.FlagSet) cli.RunFunc {
			return func(ctx context.Context, _ *cli.Call) (output.Result, error) {
				*seen = index.Watched(ctx)
				return output.Result{}, nil
			}
		},
	})
	return cmds
}

func TestRequestsMarkedWatched(t *testing.T) {
	var seen bool
	cmds := probeCommands(&seen)
	root := initRoot(t, map[string]string{"a.txt": "a\n"})
	reg := &registry.Registry{Dir: filepath.Join(shortTemp(t, "lh"), "run")}
	p, err := Start(context.Background(), Options{Dir: root, Registry: reg, Commands: cmds})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p.Stop(ctx)
	})

	tests := []struct {
		name     string
		watching bool
	}{
		{"watcher not running", false},
		{"watcher running", true},
		{"watcher stopped", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p.SetWatching(tt.watching)
			seen = !tt.watching
			resp := call(t, reg.SocketPath(p.ID), &proto.Request{Command: "probe", Cwd: root})
			if !resp.OK {
				t.Fatalf("probe: %s %s", resp.Outcome, resp.Message)
			}
			if seen != tt.watching {
				t.Fatalf("Watched(ctx) = %v, want %v", seen, tt.watching)
			}
		})
	}

	seen = true
	var out, errb bytes.Buffer
	if code := cmds.Main(context.Background(), []string{"probe"}, cli.Env{Cwd: root}, &out, &errb); code != 0 {
		t.Fatalf("direct run exit %d: %s", code, errb.String())
	}
	if seen {
		t.Fatal("direct CLI run saw Watched(ctx) = true")
	}
}
