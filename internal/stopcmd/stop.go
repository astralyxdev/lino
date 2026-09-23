// Package stopcmd implements `lino stop`: a shutdown request over the socket.
// Inside the live process the same command triggers the graceful stop.
package stopcmd

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/live"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/proto"
	"github.com/astralyx/lino/internal/registry"
)

func init() { cli.Register(Command(registry.Default)) }

// Wait bounds how long the client waits for the process to exit after
// accepting the request.
var Wait = 15 * time.Second

// Command returns `lino stop` using reg to find the registry.
func Command(reg func() (*registry.Registry, error)) *cli.Command {
	return &cli.Command{
		Name:    "stop",
		Summary: "stop the live process",
		Accept:  cli.AcceptID,
		Local:   true,
		Setup: func(fs *flag.FlagSet) cli.RunFunc {
			return func(ctx context.Context, c *cli.Call) (output.Result, error) {
				if p := live.FromContext(ctx); p != nil {
					return stopSelf(p), nil
				}
				r, err := reg()
				if err != nil {
					return output.Result{}, outcome.Wrap(outcome.Internal, err, "registry: "+err.Error())
				}
				env := ""
				if c.Env != nil {
					env = c.Env("LINO_ID")
				}
				return Stop(ctx, r, c.ID, env, c.Cwd)
			}
		},
	}
}

// Data describes the stopped process.
type Data struct {
	ID   string `json:"id"`
	Root string `json:"root"`
	PID  int    `json:"pid"`
}

// WriteText prints "stopped <id> <root>".
func (d Data) WriteText(w io.Writer) error {
	_, err := fmt.Fprintf(w, "stopped %s %s\n", d.ID, d.Root)
	return err
}

// stopSelf answers the request, then shuts the process down: in-flight
// mutations finish, the index closes, socket and registry entry go away.
func stopSelf(p *live.Process) output.Result {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), live.ShutdownGrace)
		defer cancel()
		p.Stop(ctx)
	}()
	return output.Result{Data: Data{ID: p.ID, Root: p.Root, PID: os.Getpid()}, Message: "stopping"}
}

// Stop resolves the target process (flag id, LINO_ID, then cwd), asks it to
// stop and waits until it has exited.
func Stop(ctx context.Context, r *registry.Registry, flagID, envID, cwd string) (output.Result, error) {
	t, err := r.Resolve(flagID, envID, cwd)
	if err != nil {
		return output.Result{}, err
	}
	if t.Entry == nil || registry.Check(*t.Entry) != registry.Live {
		return output.Result{}, outcome.New(outcome.NotRunning, "no live process for %s", t.Root)
	}
	e := *t.Entry
	resp, err := send(ctx, e.Socket, t.Root)
	if err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "stop "+e.ID+": "+err.Error())
	}
	if !resp.OK {
		return output.Result{Outcome: resp.Outcome, Message: resp.Message, Hint: resp.Hint}, nil
	}
	d := Data{ID: e.ID, Root: e.Root, PID: e.PID}
	if !waitGone(ctx, r, e, Wait) {
		return output.Result{Outcome: outcome.Internal, Data: d,
			Message: fmt.Sprintf("process %d accepted stop but did not exit within %s", e.PID, Wait)}, nil
	}
	return output.Result{Data: d}, nil
}

func send(ctx context.Context, sock, root string) (*proto.Response, error) {
	var dl net.Dialer
	dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c, err := dl.DialContext(dctx, "unix", sock)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(Wait))
	req := proto.NewRequest("stop")
	req.Cwd = root
	if err := proto.WriteRequest(c, req); err != nil {
		return nil, err
	}
	return proto.NewReader(c).ReadResponse()
}

// waitGone polls until the entry is removed or its process is gone.
func waitGone(ctx context.Context, r *registry.Registry, e registry.Entry, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		cur, err := r.Read(e.ID)
		if outcome.Is(err, outcome.NotFound) || (err == nil && cur.PID != e.PID) || !registry.PIDAlive(e.PID) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(20 * time.Millisecond):
		}
	}
}
