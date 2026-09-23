// Package statuscmd implements `lino status` and `lino index`.
package statuscmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/live"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/registry"
)

func init() {
	cli.Register(StatusCommand)
	cli.Register(IndexCommand)
}

// WatchInfo reports the watcher state and the number of paths waiting to be
// re-indexed for a live process. The watcher sets it; nil means no watcher.
var WatchInfo = func(p *live.Process) (state string, pending int) { return p.WatchState() }

// StatusCommand is `lino status`.
var StatusCommand = &cli.Command{
	Name:    "status",
	Summary: "index and watcher state",
	Accept:  cli.AcceptID,
	MaxArgs: 0,
	Setup: func(fs *flag.FlagSet) cli.RunFunc {
		return func(ctx context.Context, c *cli.Call) (output.Result, error) {
			return Status(ctx, c.ID, c.Env("LINO_ID"), c.Cwd)
		}
	},
}

// StatusData is what status reports. Live fields are zero when no live
// process serves the root; Watcher state and Pending are only known inside it.
type StatusData struct {
	Root          string               `json:"root"`
	ID            string               `json:"id"`
	Name          string               `json:"name,omitempty"`
	Live          bool                 `json:"live"`
	PID           int                  `json:"pid"`
	Started       *time.Time           `json:"started,omitempty"`
	UptimeSec     int64                `json:"uptime_s"`
	Indexed       bool                 `json:"indexed"`
	Totals        index.Totals         `json:"totals"`
	Watcher       string               `json:"watcher"`
	Pending       *int                 `json:"pending,omitempty"`
	LastReconcile *index.ReconcileInfo `json:"last_reconcile,omitempty"`

	now time.Time
}

// WriteText prints one fact per line.
func (d StatusData) WriteText(w io.Writer) error {
	p := func(k, format string, a ...any) {
		fmt.Fprintf(w, "%-10s%s\n", k, fmt.Sprintf(format, a...))
	}
	p("root", "%s", d.Root)
	if d.Name != "" {
		p("id", "%s (%s)", d.ID, d.Name)
	} else {
		p("id", "%s", d.ID)
	}
	if d.Live {
		p("process", "live, pid %d, up %s", d.PID, time.Duration(d.UptimeSec)*time.Second)
	} else {
		p("process", "not running")
	}
	if d.Indexed {
		p("index", "%d files, %d lines, %d binary", d.Totals.Files, d.Totals.Lines, d.Totals.Binary)
	} else {
		p("index", "not built")
	}
	p("watcher", "%s", d.Watcher)
	if d.Pending != nil {
		p("pending", "%d", *d.Pending)
	}
	if r := d.LastReconcile; r != nil {
		p("reconcile", "%s (%s ago, %dms): %s", r.At.Local().Format("2006-01-02 15:04:05"),
			d.now.Sub(r.At).Round(time.Second), r.DurationMS, summary(r.Files, r.Added, r.Modified, r.Removed, r.Moved))
	}
	return nil
}

func summary(files, added, modified, removed, moved int) string {
	return fmt.Sprintf("%d files, %d added, %d modified, %d removed, %d moved", files, added, modified, removed, moved)
}

// Status reports on the root chosen by -i, LINO_ID or cwd.
func Status(ctx context.Context, flagID, envID, cwd string) (output.Result, error) {
	d := StatusData{now: time.Now(), Watcher: "none (no live process)"}
	if p := live.FromContext(ctx); p != nil {
		d.Root, d.ID, d.Name, d.Live, d.PID = p.Root, p.ID, p.CurrentName(), true, os.Getpid()
		started := p.Started
		d.Started = &started
		d.Watcher = "not started"
		if WatchInfo != nil {
			state, pending := WatchInfo(p)
			d.Watcher, d.Pending = state, &pending
		}
		if err := fill(ctx, &d, p.DB); err != nil {
			return output.Result{}, err
		}
		return finish(d), nil
	}

	reg, err := registry.Default()
	if err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
	}
	t, err := reg.Resolve(flagID, envID, cwd)
	if err != nil {
		return output.Result{}, err
	}
	d.Root, d.ID = t.Root, t.ID
	if e := t.Entry; e != nil {
		d.Name = e.Name
		if registry.Check(*e) == registry.Live {
			d.Live, d.PID = true, e.PID
			started := e.Started
			d.Started = &started
			d.Watcher = "unknown (not asked through the live process)"
		}
	}
	if _, err := os.Stat(index.Path(t.Root)); errors.Is(err, fs.ErrNotExist) {
		return finish(d), nil
	}
	db, err := index.Open(ctx, t.Root)
	if err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
	}
	defer db.Close()
	if err := fill(ctx, &d, db); err != nil {
		return output.Result{}, err
	}
	return finish(d), nil
}

func fill(ctx context.Context, d *StatusData, db *index.DB) error {
	t, err := db.Totals(ctx)
	if err != nil {
		return outcome.Wrap(outcome.Internal, err, "")
	}
	d.Indexed, d.Totals = true, t
	ri, ok, err := db.LastReconcile(ctx)
	if err != nil {
		return outcome.Wrap(outcome.Internal, err, "")
	}
	if ok {
		d.LastReconcile = &ri
	}
	return nil
}

func finish(d StatusData) output.Result {
	if d.Started != nil {
		d.UptimeSec = int64(d.now.Sub(*d.Started) / time.Second)
	}
	return output.Result{Data: d}
}
