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

func init() { cli.Register(InsertCommand) }

// InsertCommand is `lino insert`.
var InsertCommand = &cli.Command{
	Name:    "insert",
	Usage:   "<file> (--before A | --after A | --at-start | --at-end) --v V",
	Summary: "insert stdin lines before or after an anchor, or at the start or end",
	Accept:  cli.Mutating,
	Stdin:   true,
	MinArgs: 1,
	MaxArgs: 1,
	Setup: func(fs *flag.FlagSet) cli.RunFunc {
		before := fs.String("before", "", "insert before anchor A")
		after := fs.String("after", "", "insert after anchor A")
		atStart := fs.Bool("at-start", false, "insert at the start of the file")
		atEnd := fs.Bool("at-end", false, "insert at the end of the file")
		v := fs.String("v", "", "file version from the last read")
		return func(ctx context.Context, c *cli.Call) (output.Result, error) {
			req := InsertRequest{Path: c.Arg(0), V: *v, By: c.By}
			n := 0
			if _, ok := c.Flags["before"]; ok {
				req.Where, req.At, n = mutate.Before, *before, n+1
			}
			if _, ok := c.Flags["after"]; ok {
				req.Where, req.At, n = mutate.After, *after, n+1
			}
			if *atStart {
				req.Where, n = mutate.AtStart, n+1
			}
			if *atEnd {
				req.Where, n = mutate.AtEnd, n+1
			}
			if n != 1 {
				return output.Result{}, outcome.New(outcome.Usage, "insert: give exactly one of --before, --after, --at-start, --at-end").
					WithHint(c.Command.Synopsis())
			}
			stdin, err := c.ReadStdin()
			if err != nil {
				return output.Result{}, err
			}
			req.Stdin = stdin
			return Insert(ctx, c.Cwd, req)
		}
	},
}

// InsertRequest is one insert call.
type InsertRequest struct {
	Path  string
	Where mutate.Where
	At    string // anchor for Before and After
	V     string
	By    string
	Stdin []byte
}

// InsertData is the result of an insert.
type InsertData struct {
	*mutate.Result
	Start int `json:"start"` // inserted lines, 1-based inclusive
	End   int `json:"end"`
}

// WriteText prints "updated <path> v=old→new lines A-B".
func (d InsertData) WriteText(w io.Writer) error {
	_, err := fmt.Fprintf(w, "updated %s v=%s→%s lines %d-%d\n", d.Path, d.OldV, d.NewV, d.Start, d.End)
	return err
}

// Insert runs an insert in the workspace found from cwd.
func Insert(ctx context.Context, cwd string, req InsertRequest) (output.Result, error) {
	op := &mutate.Insert{Where: req.Where}
	if req.Where == mutate.Before || req.Where == mutate.After {
		a, err := anchor.Parse(req.At)
		if err != nil {
			return output.Result{}, err
		}
		op.At = a
	}
	if err := mutate.ValidateV(req.Path, req.V); err != nil {
		return output.Result{}, err
	}
	lines, err := mutate.Content(req.Stdin)
	if err != nil {
		return output.Result{}, err
	}
	op.Lines = lines
	ws, err := Open(cwd)
	if err != nil {
		return output.Result{}, err
	}
	if err := ws.Prepare(ctx, cwd, req.Path); err != nil {
		return output.Result{}, err
	}
	p := ws.Pipeline()
	res, err := p.Run(ctx, mutate.Request{Cwd: cwd, Path: req.Path, V: req.V, By: req.By, Op: op})
	if res == nil {
		return output.Result{}, err
	}
	d := InsertData{Result: res, Start: res.Changed.Start, End: res.Changed.End}
	if err != nil {
		return output.Result{Outcome: outcome.Updated, Data: d, Message: err.Error()}, nil
	}
	return output.Result{Outcome: outcome.Updated, Data: d}, nil
}
