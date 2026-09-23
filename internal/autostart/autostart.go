// Package autostart wires the lifecycle rules around client forwarding: a
// command in an initialised root with no live process starts one, and
// --direct is refused while a live process serves the root.
package autostart

import (
	"context"
	"fmt"
	"io"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/client"
	"github.com/astralyx/lino/internal/live"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/registry"
)

type stderrKey struct{}

// NoStart lists forwarded commands that report not_running instead of
// starting a process: asking about the process must not create one.
var NoStart = map[string]bool{"status": true}

// Install makes r forward calls through client.Forward, starting a process
// when none runs, and refusing --direct when one does.
func Install(r *cli.Registry) {
	notLive := client.NotLive
	client.NotLive = func(ctx context.Context, c *cli.Call, t registry.Target) (registry.Entry, error) {
		if NoStart[c.Command.Name] {
			return notLive(ctx, c, t)
		}
		return Start(ctx, c, t)
	}
	r.Forward = func(ctx context.Context, c *cli.Call, stdout, stderr io.Writer) (int, bool) {
		if c.Direct {
			if err := CheckDirect(c); err != nil {
				p := &output.Printer{Stdout: stdout, Stderr: stderr, JSON: c.JSON}
				return p.Error(err), true
			}
			return 0, false
		}
		return client.Forward(context.WithValue(ctx, stderrKey{}, stderr), c, stdout, stderr)
	}
}

// Start is the client.NotLive hook: it starts a detached process for the
// target root (as `lino run` would), notes the startup reconcile on stderr and
// returns the new entry. Failure is reported, never answered in direct mode.
func Start(ctx context.Context, c *cli.Call, t registry.Target) (registry.Entry, error) {
	reg, err := client.Registry()
	if err != nil {
		return registry.Entry{}, outcome.Wrap(outcome.Internal, err, "registry: "+err.Error())
	}
	res, err := live.Run(ctx, live.RunRequest{Dir: t.Root, Registry: reg})
	if err != nil {
		return registry.Entry{}, err
	}
	d, _ := res.Data.(live.RunData)
	if w, ok := ctx.Value(stderrKey{}).(io.Writer); ok && !d.Existing && res.Message != "" {
		fmt.Fprintf(w, "auto-started: %s\n", res.Message)
	}
	id := d.ID
	if id == "" {
		id = t.ID
	}
	e, err := reg.Read(id)
	if err != nil {
		return registry.Entry{}, outcome.Wrap(outcome.Internal, err, "started process "+id+" is not registered")
	}
	return e, nil
}

// CheckDirect returns live_exists when a live process serves the root c
// targets. Resolution failures are left to the command itself.
func CheckDirect(c *cli.Call) error {
	reg, err := client.Registry()
	if err != nil {
		return nil
	}
	envID := ""
	if c.Env != nil {
		envID = c.Env("LINO_ID")
	}
	t, err := reg.Resolve(c.ID, envID, c.Cwd)
	if err != nil || t.Entry == nil || registry.Check(*t.Entry) != registry.Live {
		return nil
	}
	return outcome.New(outcome.LiveExists, "live process %s (pid %d) serves %s", t.Entry.ID, t.Entry.PID, t.Root).
		WithHint("run lino " + c.Command.Name + " without --direct, or lino stop first")
}
