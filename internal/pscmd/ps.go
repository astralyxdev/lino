// Package pscmd implements "lino ps": list live processes from the registry
// and remove stale entries.
package pscmd

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/registry"
)

func init() { cli.Register(Command(registry.Default, time.Now)) }

// Process is one live entry as printed by ps.
type Process struct {
	ID      string    `json:"id"`
	Name    string    `json:"name,omitempty"`
	Root    string    `json:"root"`
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
	Uptime  int64     `json:"uptime_s"`
	Version string    `json:"version"`
}

// Stale is an entry that ps removed.
type Stale struct {
	ID     string `json:"id"`
	Root   string `json:"root"`
	PID    int    `json:"pid"`
	Reason string `json:"reason"`
}

// Result is the data of a ps call.
type Result struct {
	Processes []Process `json:"processes"`
	Removed   []Stale   `json:"removed,omitempty"`
}

// WriteText prints one row per live process.
func (r Result) WriteText(w io.Writer) error {
	if len(r.Processes) == 0 {
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tROOT\tPID\tUPTIME")
	for _, p := range r.Processes {
		name := p.Name
		if name == "" {
			name = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", p.ID, name, p.Root, p.PID, FormatUptime(time.Duration(p.Uptime)*time.Second))
	}
	return tw.Flush()
}

// Command returns the ps command reading the registry from reg.
func Command(reg func() (*registry.Registry, error), now func() time.Time) *cli.Command {
	return &cli.Command{
		Name:    "ps",
		Summary: "running processes: id, name, root, pid, uptime",
		MaxArgs: 0,
		Setup: func(*flag.FlagSet) cli.RunFunc {
			return func(ctx context.Context, _ *cli.Call) (output.Result, error) {
				r, err := reg()
				if err != nil {
					return output.Result{}, err
				}
				return List(r, now())
			}
		},
	}
}

// List checks every registry entry, removes stale ones and returns the live
// processes. The outcome is empty when none is live.
func List(r *registry.Registry, now time.Time) (output.Result, error) {
	entries, err := r.List()
	if err != nil {
		return output.Result{}, err
	}
	res := Result{Processes: []Process{}}
	var notes []string
	for _, e := range entries {
		if st := registry.Check(e); st != registry.Live {
			if err := r.Remove(e.ID); err != nil {
				return output.Result{}, err
			}
			res.Removed = append(res.Removed, Stale{ID: e.ID, Root: e.Root, PID: e.PID, Reason: st.String()})
			notes = append(notes, fmt.Sprintf("removed stale %s (%s, pid %d) %s", e.ID, st, e.PID, e.Root))
			continue
		}
		up := max(now.Sub(e.Started), 0)
		res.Processes = append(res.Processes, Process{
			ID: e.ID, Name: e.Name, Root: e.Root, PID: e.PID,
			Started: e.Started, Uptime: int64(up / time.Second), Version: e.Version,
		})
	}
	out := output.Result{Data: res}
	if len(res.Processes) == 0 {
		out.Outcome = outcome.Empty
		notes = append(notes, "no running processes")
	}
	out.Message = strings.Join(notes, "\n")
	return out, nil
}

// FormatUptime renders d compactly with its two largest units: 45s, 3m12s,
// 2h05m, 3d04h.
func FormatUptime(d time.Duration) string {
	s := int64(d / time.Second)
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	case s < 86400:
		return fmt.Sprintf("%dh%02dm", s/3600, s%3600/60)
	}
	return fmt.Sprintf("%dd%02dh", s/86400, s%86400/3600)
}
