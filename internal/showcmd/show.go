// Package showcmd implements `lino show <id>`: one recorded change as a
// header and a line diff.
package showcmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/histrec"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
)

func init() { cli.Register(Command) }

// Command is `lino show`.
var Command = &cli.Command{
	Name:    "show",
	Usage:   "<id>",
	Summary: "one change as a diff",
	Accept:  cli.File,
	MinArgs: 1,
	MaxArgs: 1,
	Setup: func(*flag.FlagSet) cli.RunFunc {
		return func(ctx context.Context, c *cli.Call) (output.Result, error) {
			id, err := strconv.ParseInt(c.Arg(0), 10, 64)
			if err != nil || id < 1 {
				return output.Result{}, outcome.New(outcome.Usage, "invalid change id %q", c.Arg(0)).WithHint("lino history")
			}
			return Show(ctx, c.Cwd, id)
		}
	},
}

// TimeFormat is how the header prints the change time (local time).
const TimeFormat = "2006-01-02 15:04:05"

// Hunk is one changed range. OldStart is the first line in the version
// before the change, NewStart the first line in the version after it.
type Hunk struct {
	OldStart int      `json:"old_start"`
	Old      []string `json:"old"`
	NewStart int      `json:"new_start"`
	New      []string `json:"new"`
}

// Data is one change.
type Data struct {
	ID        int64     `json:"id"`
	Time      time.Time `json:"time"`
	Source    string    `json:"source"`
	Author    string    `json:"author,omitempty"`
	Op        string    `json:"op"`
	Path      string    `json:"path"`
	VBefore   string    `json:"v_before,omitempty"`
	VAfter    string    `json:"v_after,omitempty"`
	From      string    `json:"from,omitempty"`
	Undid     int64     `json:"undid,omitempty"`
	Created   bool      `json:"created,omitempty"`
	Removed   bool      `json:"removed,omitempty"`
	Binary    bool      `json:"binary,omitempty"`
	Symlink   bool      `json:"symlink,omitempty"`
	Hunks     []Hunk    `json:"hunks"`
	DiffLines int       `json:"diff_lines"`          // total -/+ lines in the change
	Shown     int       `json:"shown_lines"`         // -/+ lines included in Hunks
	Truncated bool      `json:"truncated,omitempty"` // Hunks cut at the output limit
}

// WriteText prints the header line, notes and the diff.
func (d Data) WriteText(w io.Writer) error {
	author := d.Author
	if author == "" {
		author = "-"
	}
	var state string
	switch {
	case d.Op == history.OpMv || d.From != "":
		state = "moved from " + d.From
	case d.Created:
		state = "created v=" + d.VAfter
	case d.Removed:
		state = "removed v=" + d.VBefore
	default:
		state = "v=" + d.VBefore + "→" + d.VAfter
	}
	if d.Undid != 0 {
		state += fmt.Sprintf("  undid %d", d.Undid)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d  %s  %s  %s  %s  %s  %s\n", d.ID, d.Time.Local().Format(TimeFormat), d.Source, author, d.Op, d.Path, state)
	switch {
	case d.Symlink:
		b.WriteString("symlink removed: recorded by path only, cannot be rolled back\n")
	case d.Binary:
		b.WriteString("binary: recorded by version only, cannot be rolled back\n")
	}
	for _, h := range d.Hunks {
		for i, l := range h.Old {
			fmt.Fprintf(&b, "- %d: %s\n", h.OldStart+i, l)
		}
		for i, l := range h.New {
			fmt.Fprintf(&b, "+ %d: %s\n", h.NewStart+i, l)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Show loads change id from the history of the workspace found from cwd.
func Show(ctx context.Context, cwd string, id int64) (output.Result, error) {
	ws, err := filecmd.Open(cwd)
	if err != nil {
		return output.Result{}, err
	}
	st, err := histrec.Store(ctx, ws.Root.Path())
	if err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
	}
	c, err := st.Get(ctx, id)
	if errors.Is(err, history.ErrNotFound) {
		return output.Result{}, outcome.New(outcome.NotFound, "no change %d in history (unknown or pruned)", id).WithHint("lino history")
	}
	if err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
	}
	d := Build(c, ws.Config.ReadLines)
	res := output.Result{Data: d}
	if d.Truncated {
		res.Outcome = outcome.Truncated
		res.Message = fmt.Sprintf("diff cut at %d of %d lines", d.Shown, d.DiffLines)
	}
	return res, nil
}

// Build converts a change into Data, keeping at most limit diff lines
// (limit <= 0 = no limit).
func Build(c history.Change, limit int) Data {
	d := Data{
		ID: c.ID, Time: c.Time, Source: c.Source, Author: c.Author, Op: c.Op, Path: c.Path,
		VBefore: c.VBefore, VAfter: c.VAfter,
		From: c.Extra.From, Undid: c.Extra.Undid, Created: c.Extra.Created, Removed: c.Extra.Removed,
		Binary: c.Extra.Binary, Symlink: c.Extra.Symlink,
		Hunks: []Hunk{},
	}
	delta := 0 // lines added minus removed by earlier fragments
	for _, f := range c.Fragments {
		h := Hunk{OldStart: f.Pos + 1, NewStart: f.Pos + delta + 1, Old: f.Old, New: f.New}
		delta += len(f.New) - len(f.Old)
		if h.Old == nil {
			h.Old = []string{}
		}
		if h.New == nil {
			h.New = []string{}
		}
		n := len(h.Old) + len(h.New)
		d.DiffLines += n
		if d.Truncated {
			continue
		}
		if limit > 0 && d.Shown+n > limit {
			room := limit - d.Shown
			h.Old = h.Old[:min(len(h.Old), room)]
			h.New = h.New[:min(len(h.New), room-len(h.Old))]
			d.Truncated = true
			n = len(h.Old) + len(h.New)
			if n == 0 {
				continue
			}
		}
		d.Shown += n
		d.Hunks = append(d.Hunks, h)
	}
	return d
}
