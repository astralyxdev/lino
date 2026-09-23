package filecmd

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/astralyx/lino/internal/anchor"
	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
)

func init() { cli.Register(DeleteCommand) }

// DeleteCommand is `lino delete`. It never reads stdin.
var DeleteCommand = &cli.Command{
	Name:    "delete",
	Usage:   "<file> <start> <end> --v V",
	Summary: "delete a line range",
	Accept:  cli.Mutating,
	MinArgs: 3,
	MaxArgs: 3,
	Setup: func(fs *flag.FlagSet) cli.RunFunc {
		v := fs.String("v", "", "file version from the last read")
		return func(ctx context.Context, c *cli.Call) (output.Result, error) {
			return Delete(ctx, c.Cwd, DeleteRequest{
				Path: c.Arg(0), Start: c.Arg(1), End: c.Arg(2), V: *v, By: c.By,
			})
		}
	},
}

// DeleteRequest is one delete call. Start and End are anchors ("13:9c1") or
// plain line numbers.
type DeleteRequest struct {
	Path, Start, End, V, By string
}

// DeleteData is the result of a delete. Start and End are the removed lines
// as numbered in the old file.
type DeleteData struct {
	Path    string `json:"path"`
	Op      string `json:"op"`
	OldV    string `json:"old_version"`
	Version string `json:"version"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Total   int    `json:"total"`
	By      string `json:"by,omitempty"`
}

// WriteText prints "updated <path> v=old→new deleted lines A-B".
func (d DeleteData) WriteText(w io.Writer) error {
	_, err := fmt.Fprintf(w, "updated %s v=%s→%s deleted lines %d-%d\n", d.Path, d.OldV, d.Version, d.Start, d.End)
	return err
}

// Delete removes an anchored line range in the workspace found from cwd.
func Delete(ctx context.Context, cwd string, req DeleteRequest) (output.Result, error) {
	start, err := anchor.Parse(req.Start)
	if err != nil {
		return output.Result{}, err
	}
	end, err := anchor.Parse(req.End)
	if err != nil {
		return output.Result{}, err
	}
	ws, err := Open(cwd)
	if err != nil {
		return output.Result{}, err
	}
	if err := ws.Prepare(ctx, cwd, req.Path); err != nil {
		return output.Result{}, err
	}
	p := ws.Pipeline()
	res, err := p.Run(ctx, mutate.Request{
		Cwd: cwd, Path: req.Path, V: req.V, By: req.By,
		Op: &mutate.Splice{Start: start, End: end},
	})
	if res == nil {
		return output.Result{}, err
	}
	d := DeleteData{
		Path: res.Path, Op: res.Op, OldV: res.OldV, Version: res.NewV,
		Start: res.Target.Start, End: res.Target.End, Total: res.Total, By: res.By,
	}
	return output.Result{Outcome: outcome.Updated, Data: d}, err
}
