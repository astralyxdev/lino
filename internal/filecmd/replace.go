package filecmd

import (
	"context"
	"flag"
	"fmt"
	"io"
	"slices"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/textfile"
)

func init() { cli.Register(ReplaceCommand) }

// DefaultSep separates the old and new blocks on replace's stdin.
const DefaultSep = "<<<lino>>>"

// ReplaceCommand is `lino replace`.
var ReplaceCommand = &cli.Command{
	Name:    "replace",
	Usage:   "<file> --v V [--sep S]",
	Summary: "replace an exact unique block; old and new from stdin",
	Accept:  cli.Mutating,
	Stdin:   true,
	MinArgs: 1,
	MaxArgs: 1,
	Setup: func(fs *flag.FlagSet) cli.RunFunc {
		v := fs.String("v", "", "version the file was read at")
		sep := fs.String("sep", DefaultSep, "separator line between old and new text")
		return func(ctx context.Context, c *cli.Call) (output.Result, error) {
			in, err := c.ReadStdin()
			if err != nil {
				return output.Result{}, err
			}
			return Replace(ctx, c.Cwd, ReplaceRequest{Path: c.Arg(0), V: *v, By: c.By, Sep: *sep, Stdin: in})
		}
	},
}

// ReplaceRequest is one replace call.
type ReplaceRequest struct {
	Path  string
	V     string
	By    string
	Sep   string // "" = DefaultSep
	Stdin []byte
}

// ReplaceData is the result of a replace.
type ReplaceData struct {
	mutate.Result
	Start int `json:"start"` // first changed line in the new file
	End   int `json:"end"`   // last changed line; End < Start when lines were only removed
}

// WriteText prints "updated <path> v=old→new lines A-B".
func (d ReplaceData) WriteText(w io.Writer) error {
	if d.Unchanged {
		_, err := fmt.Fprintf(w, "unchanged %s v=%s\n", d.Path, d.NewV)
		return err
	}
	where := fmt.Sprintf("lines %d-%d", d.Start, d.End)
	if d.End < d.Start {
		where = fmt.Sprintf("removed lines %d-%d", d.Target.Start, d.Target.End)
	}
	_, err := fmt.Fprintf(w, "updated %s v=%s→%s %s\n", d.Path, d.OldV, d.NewV, where)
	return err
}

// Replace runs a replace in the workspace found from cwd.
func Replace(ctx context.Context, cwd string, req ReplaceRequest) (output.Result, error) {
	if err := mutate.ValidateV(req.Path, req.V); err != nil {
		return output.Result{}, err
	}
	op, err := ParseReplace(req.Stdin, req.Sep)
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
	res, err := p.Run(ctx, mutate.Request{Cwd: cwd, Path: req.Path, V: req.V, By: req.By, Op: op})
	if err != nil {
		return output.Result{}, err
	}
	d := ReplaceData{Result: *res, Start: res.Changed.Start, End: res.Changed.End}
	if res.Unchanged {
		return output.Result{Outcome: outcome.OK, Data: d}, nil
	}
	return output.Result{Outcome: outcome.Updated, Data: d}, nil
}

// ParseReplace splits replace's stdin into the old and new blocks around the
// separator line sep ("" = DefaultSep). The separator must appear exactly
// once and the old block must not be empty; the new block may be.
func ParseReplace(stdin []byte, sep string) (*ReplaceOp, error) {
	if sep == "" {
		sep = DefaultSep
	}
	lines := textfile.SplitInput(stdin)
	hint := "old lines, then a line " + sep + ", then new lines"
	var at []int
	for i, l := range lines {
		if l == sep {
			at = append(at, i)
		}
	}
	switch {
	case len(at) == 0:
		return nil, outcome.New(outcome.Usage, "no separator line %q on stdin", sep).WithHint(hint)
	case len(at) > 1:
		return nil, outcome.New(outcome.Usage, "separator line %q appears %d times", sep, len(at)).
			WithHint("choose another separator with --sep")
	case at[0] == 0:
		return nil, outcome.New(outcome.Usage, "old text is empty").WithHint(hint)
	}
	old := lines[:at[0]]
	repl := lines[at[0]+1:]
	if repl == nil {
		repl = []string{}
	}
	return &ReplaceOp{Old: old, New: repl}, nil
}

// ReplaceOp replaces the one exact whole-line occurrence of Old with New.
type ReplaceOp struct {
	Old, New []string
}

func (r *ReplaceOp) Name() string { return "replace" }

// Target finds Old in lines: none is not_found, several is ambiguous with
// the candidate ranges.
func (r *ReplaceOp) Target(lines []string) (mutate.Range, error) {
	var found []int
	for i := 0; i+len(r.Old) <= len(lines); i++ {
		if slices.Equal(lines[i:i+len(r.Old)], r.Old) {
			found = append(found, i+1)
		}
	}
	switch len(found) {
	case 0:
		return mutate.Range{}, outcome.New(outcome.NotFound, "old text (%d lines) not found", len(r.Old)).
			WithHint("the match is exact and whole-line; re-read the file")
	case 1:
		return mutate.Range{Start: found[0], End: found[0] + len(r.Old) - 1}, nil
	}
	cands := make([]string, len(found))
	for i, s := range found {
		cands[i] = fmt.Sprintf("lines %d-%d", s, s+len(r.Old)-1)
	}
	return mutate.Range{}, outcome.New(outcome.Ambiguous, "old text matches %d times", len(found)).
		WithCandidates(cands...).
		WithHint("add surrounding lines to the old text, or use lino edit with anchors")
}

func (r *ReplaceOp) Apply(doc *textfile.Doc, t mutate.Range) (mutate.Range, error) {
	doc.Replace(t.Start-1, t.Len(), r.New)
	return mutate.Range{Start: t.Start, End: t.Start + len(r.New) - 1}, nil
}
