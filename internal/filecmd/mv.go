package filecmd

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
)

func init() { cli.Register(MvCommand) }

// MvCommand is `lino mv`.
var MvCommand = &cli.Command{
	Name:    "mv",
	Usage:   "<from> <to>",
	Summary: "move or rename a file; the target must not exist",
	Accept:  cli.Mutating,
	MinArgs: 2,
	MaxArgs: 2,
	Setup: func(fs *flag.FlagSet) cli.RunFunc {
		return func(ctx context.Context, c *cli.Call) (output.Result, error) {
			return Mv(ctx, c.Cwd, MvRequest{From: c.Arg(0), To: c.Arg(1), By: c.By})
		}
	},
}

// MvRequest is one mv call.
type MvRequest struct {
	From, To, By string
}

// MvData is the result of a move.
type MvData struct {
	From    string `json:"from"`
	Path    string `json:"path"`
	Version string `json:"version,omitempty"`
	By      string `json:"by,omitempty"`
}

// WriteText prints "moved <from> → <to> v=V".
func (d MvData) WriteText(w io.Writer) error {
	var err error
	if d.Version == "" {
		_, err = fmt.Fprintf(w, "moved %s → %s\n", d.From, d.Path)
	} else {
		_, err = fmt.Fprintf(w, "moved %s → %s v=%s\n", d.From, d.Path, d.Version)
	}
	return err
}

// Mv moves a file in the workspace found from cwd.
func Mv(ctx context.Context, cwd string, req MvRequest) (output.Result, error) {
	ws, err := Open(cwd)
	if err != nil {
		return output.Result{}, err
	}
	if err := ws.Prepare(ctx, cwd, req.From, req.To); err != nil {
		return output.Result{}, err
	}
	p := ws.Pipeline()
	res, err := p.Move(ctx, mutate.MoveRequest{Cwd: cwd, From: req.From, To: req.To, By: req.By})
	if res == nil {
		return output.Result{}, err
	}
	d := MvData{From: res.From, Path: res.Path, Version: res.NewV, By: res.By}
	return output.Result{Outcome: outcome.Updated, Data: d}, err
}
