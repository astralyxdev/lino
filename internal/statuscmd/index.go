package statuscmd

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/config"
	"github.com/astralyx/lino/internal/ignore"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/live"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/registry"
)

// IndexCommand is `lino index`.
var IndexCommand = &cli.Command{
	Name:    "index",
	Summary: "force a full reconcile",
	Accept:  cli.AcceptID | cli.AcceptDirect,
	MaxArgs: 0,
	Setup: func(fs *flag.FlagSet) cli.RunFunc {
		return func(ctx context.Context, c *cli.Call) (output.Result, error) {
			return Index(ctx, c.ID, c.Env("LINO_ID"), c.Cwd)
		}
	},
}

// IndexData is the reconcile summary.
type IndexData struct {
	Root       string `json:"root"`
	Files      int    `json:"files"`
	Added      int    `json:"added"`
	Modified   int    `json:"modified"`
	Removed    int    `json:"removed"`
	Moved      int    `json:"moved"`
	Unchanged  int    `json:"unchanged"`
	DurationMS int64  `json:"duration_ms"`
}

// WriteText prints the summary on one line.
func (d IndexData) WriteText(w io.Writer) error {
	_, err := fmt.Fprintf(w, "reconciled in %dms: %s, %d unchanged\n",
		d.DurationMS, summary(d.Files, d.Added, d.Modified, d.Removed, d.Moved), d.Unchanged)
	return err
}

// Index runs a full reconcile of the root chosen by -i, LINO_ID or cwd. Inside
// the live process it uses the process index; outside it refuses when a live
// process serves the root, since there must never be two writers.
func Index(ctx context.Context, flagID, envID, cwd string) (output.Result, error) {
	if p := live.FromContext(ctx); p != nil {
		rules, err := ignore.NewRules(p.Root)
		if err != nil {
			return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
		}
		p.Rules = rules
		return reconcile(ctx, p.Root, p.DB, rules, p.Config.MaxFileSize, true)
	}

	reg, err := registry.Default()
	if err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
	}
	t, err := reg.Resolve(flagID, envID, cwd)
	if err != nil {
		return output.Result{}, err
	}
	if e := t.Entry; e != nil && registry.Check(*e) == registry.Live {
		return output.Result{}, outcome.New(outcome.LiveExists, "live process %s (pid %d) serves %s", e.ID, e.PID, t.Root).
			WithHint("run lino index without --direct, or lino stop first")
	}
	cfg, err := config.Load(t.Root)
	if err != nil {
		return output.Result{}, err
	}
	rules, err := ignore.NewRules(t.Root)
	if err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
	}
	db, err := index.Open(ctx, t.Root)
	if err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
	}
	defer db.Close()
	return reconcile(ctx, t.Root, db, rules, cfg.MaxFileSize, !db.Rebuilt)
}

// reconcile runs a full reconcile; with logExternal the changes it finds are
// reported as external edits (not for a freshly built index).
func reconcile(ctx context.Context, root string, db *index.DB, rules *ignore.Rules, maxSize int64, logExternal bool) (output.Result, error) {
	s, err := db.Reconcile(ctx, rules, maxSize)
	if err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "reconcile: "+err.Error())
	}
	if logExternal {
		db.ReportExternal(ctx, s.Changes)
	}
	d := IndexData{
		Root: root, Files: s.Files, Added: s.Added, Modified: s.Modified, Removed: s.Removed,
		Moved: s.Moved, Unchanged: s.Unchanged, DurationMS: s.Duration.Milliseconds(),
	}
	res := output.Result{Data: d}
	if s.Added+s.Modified+s.Removed+s.Moved > 0 {
		res.Outcome = outcome.Updated
	}
	return res, nil
}
