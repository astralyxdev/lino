// Package client forwards CLI calls to the live process over its socket.
package client

import (
	"context"
	"io"
	"net"
	"os"
	"time"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/proto"
	"github.com/astralyx/lino/internal/registry"
)

// Registry returns the process registry; tests may replace it.
var Registry = registry.Default

// DialTimeout bounds connecting to the socket.
var DialTimeout = 2 * time.Second

// NotLive is called when the target root has no live process. It returns the
// entry of a process to forward to, or an error to print. Auto-start (L16)
// replaces it; the default never falls back to direct mode.
var NotLive = func(ctx context.Context, c *cli.Call, t registry.Target) (registry.Entry, error) {
	return registry.Entry{}, outcome.New(outcome.NotRunning, "no live lino process for %s", t.Root).
		WithHint("lino run " + t.Root)
}

// Install makes r forward calls to the live process.
func Install(r *cli.Registry) { r.Forward = Forward }

// Forwarded reports whether c goes to the live process: commands that take
// -i, are not Local, and were not called with --direct.
func Forwarded(c *cli.Call) bool {
	return c.Command.Accept&cli.AcceptID != 0 && !c.Command.Local && !c.Direct
}

// Forward sends c to the live process of its root and prints the reply
// exactly as the command would print it locally. It returns handled=false
// for calls that run in this process.
func Forward(ctx context.Context, c *cli.Call, stdout, stderr io.Writer) (int, bool) {
	if !Forwarded(c) {
		return 0, false
	}
	p := &output.Printer{Stdout: stdout, Stderr: stderr, JSON: c.JSON}
	resp, err := Call(ctx, c)
	if err != nil {
		return p.Error(err), true
	}
	return resp.Print(stdout, stderr), true
}

// Call resolves c's target process and returns its response.
func Call(ctx context.Context, c *cli.Call) (*proto.Response, error) {
	req, err := Request(c)
	if err != nil {
		return nil, err
	}
	reg, err := Registry()
	if err != nil {
		return nil, outcome.Wrap(outcome.Internal, err, "registry: "+err.Error())
	}
	envID := ""
	if c.Env != nil {
		envID = c.Env("LINO_ID")
	}
	t, err := reg.Resolve(c.ID, envID, c.Cwd)
	if err != nil {
		return nil, err
	}
	if t.Entry != nil {
		conn, err := dial(ctx, t.Entry.Socket)
		if err == nil {
			return roundTrip(ctx, conn, t.Entry.ID, req)
		}
	}
	e, err := NotLive(ctx, c, t)
	if err != nil {
		return nil, err
	}
	conn, err := dial(ctx, e.Socket)
	if err != nil {
		return nil, outcome.Wrap(outcome.Internal, err, "cannot reach lino process "+e.ID+": "+err.Error())
	}
	return roundTrip(ctx, conn, e.ID, req)
}

// Request builds the socket request for c. Stdin is read only for commands
// marked Stdin, so a forwarded read or search never blocks on an open pipe.
func Request(c *cli.Call) (*proto.Request, error) {
	req := proto.NewRequest(c.Command.Name, c.Args...)
	req.Flags = c.Flags
	req.Cwd = c.Cwd
	if req.Cwd == "" {
		req.Cwd, _ = os.Getwd()
	}
	req.By = c.By
	req.JSON = c.JSON
	if c.Command.Stdin {
		in, err := c.ReadStdin()
		if err != nil {
			return nil, err
		}
		req.Stdin = in
	}
	return req, nil
}

func dial(ctx context.Context, sock string) (net.Conn, error) {
	var d net.Dialer
	dctx, cancel := context.WithTimeout(ctx, DialTimeout)
	defer cancel()
	return d.DialContext(dctx, "unix", sock)
}

func roundTrip(ctx context.Context, conn net.Conn, id string, req *proto.Request) (*proto.Response, error) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	defer stop()
	if err := proto.WriteRequest(conn, req); err != nil {
		return nil, reachErr(id, err)
	}
	resp, err := proto.NewReader(conn).ReadResponse()
	if err != nil {
		if _, ok := outcome.As(err); ok {
			return nil, err
		}
		return nil, reachErr(id, err)
	}
	return resp, nil
}

func reachErr(id string, err error) error {
	return outcome.Wrap(outcome.Internal, err, "lino process "+id+": "+err.Error())
}
