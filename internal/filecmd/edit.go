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

func init() { cli.Register(EditCommand) }

// EditCommand is `lino edit`.
var EditCommand = &cli.Command{
	Name:    "edit",
	Usage:   "<file> <start> <end> --v V",
	Summary: "replace a line range with lines from stdin",
	Accept:  cli.Mutating,
	Stdin:   true,
	MinArgs: 3,
	MaxArgs: 3,
	Setup: func(fs *flag.FlagSet) cli.RunFunc {
		v := fs.String("v", "", "version of the file you last read")
		return func(ctx context.Context, c *cli.Call) (output.Result, error) {
			req := EditRequest{Path: c.Arg(0), Start: c.Arg(1), End: c.Arg(2), V: *v, By: c.By}
			if err := mutate.ValidateV(req.Path, req.V); err != nil {
				return output.Result{}, err
			}
			in, err := c.ReadStdin()
			if err != nil {
				return output.Result{}, err
			}
			if req.Lines, err = mutate.Content(in); err != nil {
				return output.Result{}, err
			}
			return Edit(ctx, c.Cwd, req)
		}
	},
}

// EditRequest is one edit call. Start and End are anchors ("13:9c1") or
// plain line numbers ("13").
type EditRequest struct {
	Path       string
	Start, End string
	V          string
	By         string
	Lines      []string
}

// EditData is the result of a line mutation.
type EditData struct {
	mutate.Result
	Start int `json:"start"` // changed lines in the new file
	End   int `json:"end"`
}

// WriteText prints "updated <path> v=old→new lines A-B".
func (d EditData) WriteText(w io.Writer) error {
	if d.Unchanged {
		_, err := fmt.Fprintf(w, "unchanged %s v=%s\n", d.Path, d.NewV)
		return err
	}
	_, err := fmt.Fprintf(w, "updated %s v=%s→%s lines %d-%d\n", d.Path, d.OldV, d.NewV, d.Start, d.End)
	return err
}

// Edit replaces the anchored range with req.Lines.
func Edit(ctx context.Context, cwd string, req EditRequest) (output.Result, error) {
	start, err := anchor.Parse(req.Start)
	if err != nil {
		return output.Result{}, err
	}
	end, err := anchor.Parse(req.End)
	if err != nil {
		return output.Result{}, err
	}
	if len(req.Lines) == 0 {
		return output.Result{}, outcome.New(outcome.Usage, "no content on stdin").
			WithHint("use lino delete to remove lines")
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
		Op: &mutate.Splice{Start: start, End: end, Lines: req.Lines, OpName: "edit"},
	})
	if err != nil {
		return output.Result{}, err
	}
	d := EditData{Result: *res, Start: res.Changed.Start, End: res.Changed.End}
	if res.Unchanged {
		return output.Result{Outcome: outcome.OK, Data: d, Message: "content already matches; nothing written"}, nil
	}
	return output.Result{Outcome: outcome.Updated, Data: d}, nil
}
