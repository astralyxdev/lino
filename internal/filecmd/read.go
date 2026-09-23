package filecmd

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/astralyx/lino/internal/anchor"
	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/version"
)

func init() { cli.Register(ReadCommand) }

// ReadCommand is `lino read`.
var ReadCommand = &cli.Command{
	Name:    "read",
	Usage:   "<file> [--lines A:B] [--anchors]",
	Summary: "print a file or a line range",
	Accept:  cli.File,
	MinArgs: 1,
	MaxArgs: 1,
	Setup: func(fs *flag.FlagSet) cli.RunFunc {
		lines := fs.String("lines", "", "line range A:B (1-based, inclusive)")
		anchors := fs.Bool("anchors", false, "prefix each line with its anchor N:hash")
		return func(ctx context.Context, c *cli.Call) (output.Result, error) {
			return Read(c.Cwd, ReadRequest{Path: c.Arg(0), Lines: *lines, Anchors: *anchors})
		}
	},
}

// ReadRequest is one read call.
type ReadRequest struct {
	Path    string
	Lines   string // "A:B", "A:", "A" or "" for the whole file
	Anchors bool
}

// ReadData is the result of a read. Start and End are 1-based and inclusive;
// both are 0 when no lines are returned.
type ReadData struct {
	Path    string   `json:"path"`
	Version string   `json:"version"`
	Start   int      `json:"start"`
	End     int      `json:"end"`
	Total   int      `json:"total"`
	Lines   []string `json:"lines"`
	Anchors []string `json:"anchors,omitempty"` // hash per line, parallel to Lines
}

// WriteText prints the header and the lines as they are.
func (d ReadData) WriteText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "%s v=%s lines %d-%d of %d\n", d.Path, d.Version, d.Start, d.End, d.Total); err != nil {
		return err
	}
	for i, l := range d.Lines {
		if d.Anchors != nil {
			l = strconv.Itoa(d.Start+i) + ":" + d.Anchors[i] + "| " + l
		}
		if _, err := io.WriteString(w, l+"\n"); err != nil {
			return err
		}
	}
	return nil
}

// Read runs a read in the workspace found from cwd.
func Read(cwd string, req ReadRequest) (output.Result, error) {
	a, b, err := ParseRange(req.Lines)
	if err != nil {
		return output.Result{}, err
	}
	ws, err := Open(cwd)
	if err != nil {
		return output.Result{}, err
	}
	f, err := ws.Load(cwd, req.Path)
	if err != nil {
		return output.Result{}, err
	}
	all := f.Lines()
	d := ReadData{Path: f.Path, Version: version.Of(f.Data), Total: len(all), Lines: []string{}}
	switch {
	case len(all) == 0:
		return output.Result{Outcome: outcome.Empty, Data: d, Message: "empty file"}, nil
	case a > len(all):
		return output.Result{Outcome: outcome.Empty, Data: d,
			Message: fmt.Sprintf("line %d is past the end (%d lines)", a, len(all))}, nil
	}
	if b == 0 || b > len(all) {
		b = len(all)
	}
	var res output.Result
	if limit := ws.Config.ReadLines; limit > 0 && b-a+1 > limit {
		next := b
		b = a + limit - 1
		if next > b+limit {
			next = b + limit
		}
		res.Outcome = outcome.Truncated
		res.Message = fmt.Sprintf("truncated at %d lines", limit)
		res.Hint = fmt.Sprintf("--lines %d:%d", b+1, next)
	}
	d.Start, d.End = a, b
	d.Lines = all[a-1 : b]
	if req.Anchors {
		d.Anchors = make([]string, len(d.Lines))
		for i, l := range d.Lines {
			d.Anchors[i] = anchor.Hash(l)
		}
	}
	res.Data = d
	return res, nil
}

// ParseRange parses --lines: "A:B", "A:" (to the end) or "A" (one line).
// An empty spec is the whole file (1, 0). B == 0 means the last line.
func ParseRange(s string) (a, b int, err error) {
	if s == "" {
		return 1, 0, nil
	}
	bad := func(why string) (int, int, error) {
		return 0, 0, outcome.New(outcome.Usage, "--lines %q: %s", s, why).WithHint("--lines A:B with 1 <= A <= B")
	}
	from, to, hasColon := strings.Cut(s, ":")
	a, err = strconv.Atoi(from)
	if err != nil {
		return bad("start is not a number")
	}
	if a < 1 {
		return bad("start must be >= 1")
	}
	switch {
	case !hasColon:
		return a, a, nil
	case to == "":
		return a, 0, nil
	}
	b, err = strconv.Atoi(to)
	if err != nil {
		return bad("end is not a number")
	}
	if b < a {
		return bad("end must be >= start")
	}
	return a, b, nil
}
