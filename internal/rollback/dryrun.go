package rollback

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/astralyx/lino/internal/changelog"
	"github.com/astralyx/lino/internal/changescmd"
	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/histrec"
	"github.com/astralyx/lino/internal/linediff"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/showcmd"
	"github.com/astralyx/lino/internal/textfile"
	"github.com/astralyx/lino/internal/version"
)

// Dry-run file actions.
const (
	DryEdit    = "edit"    // lines change
	DryRestore = "restore" // a missing file is written back
	DryRemove  = "remove"  // an existing file is removed
	DryMove    = "move"    // the file moves back to To
)

// DryChange is the change a single-change dry run would undo.
type DryChange struct {
	ID      int64  `json:"id"`
	Op      string `json:"op"`
	Path    string `json:"path"`
	Author  string `json:"author,omitempty"`
	Summary string `json:"summary,omitempty"` // "lines 13-15", "created", ...
}

// DryFile is what a rollback would do to one file. Hunks number old lines in
// the current file and new lines in the file after the rollback.
type DryFile struct {
	Path   string         `json:"path"`
	Action string         `json:"action"`
	To     string         `json:"to,omitempty"` // DryMove: destination
	OldV   string         `json:"old_version,omitempty"`
	NewV   string         `json:"version,omitempty"`
	Hunks  []showcmd.Hunk `json:"hunks"`
}

// DryConflict is why the rollback would fail, with the current lines.
type DryConflict struct {
	Message string         `json:"message"`
	Path    string         `json:"path,omitempty"`
	Lines   []outcome.Line `json:"lines,omitempty"`
}

// DryData is the result of --dry-run: nothing is written.
type DryData struct {
	DryRun    bool         `json:"dry_run"`
	Undo      *DryChange   `json:"undo,omitempty"` // rollback [<id>]
	To        int64        `json:"to,omitempty"`   // rollback --to
	Files     []DryFile    `json:"files"`
	Conflict  *DryConflict `json:"conflict,omitempty"`
	DiffLines int          `json:"diff_lines"`
	Shown     int          `json:"shown_lines"`
	Truncated bool         `json:"truncated,omitempty"`
}

// WriteText prints e.g.
//
//	would undo 1047 (edit wallet/service.go lines 13-15 by agent-2)
//	- 13:     if amt <= 0 || amt > MaxWithdraw {
//	+ 13:     if amt <= 0 {
//	no conflicts
func (d DryData) WriteText(w io.Writer) error {
	var b strings.Builder
	switch {
	case d.Undo != nil:
		u := d.Undo
		fmt.Fprintf(&b, "would undo %d (%s %s", u.ID, u.Op, u.Path)
		if u.Summary != "" {
			b.WriteString(" " + u.Summary)
		}
		if u.Author != "" {
			b.WriteString(" by " + u.Author)
		}
		b.WriteString(")\n")
	case len(d.Files) == 1:
		fmt.Fprintf(&b, "would restore 1 file to its state right after %d\n", d.To)
	default:
		fmt.Fprintf(&b, "would restore %d files to their state right after %d\n", len(d.Files), d.To)
	}
	for _, f := range d.Files {
		if line := f.header(d.Undo); line != "" {
			b.WriteString(line + "\n")
		}
		for _, h := range f.Hunks {
			for i, l := range h.Old {
				fmt.Fprintf(&b, "- %d: %s\n", h.OldStart+i, l)
			}
			for i, l := range h.New {
				fmt.Fprintf(&b, "+ %d: %s\n", h.NewStart+i, l)
			}
		}
	}
	if d.Conflict != nil {
		b.WriteString("conflict: " + d.Conflict.Message + "\n")
		for _, l := range d.Conflict.Lines {
			b.WriteString(output.FormatLine(l) + "\n")
		}
	} else {
		b.WriteString("no conflicts\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// header is the per-file line; single-change edits of a file that did not
// move need none (the path is in the first line).
func (f DryFile) header(u *DryChange) string {
	switch f.Action {
	case DryMove:
		return fmt.Sprintf("move %s back to %s", f.Path, f.To)
	case DryRestore:
		return fmt.Sprintf("restore %s v=%s", f.Path, f.NewV)
	case DryRemove:
		return fmt.Sprintf("remove %s v=%s", f.Path, f.OldV)
	}
	if u != nil && u.Path == f.Path {
		return ""
	}
	return fmt.Sprintf("%s  v=%s→%s", f.Path, dash(f.OldV), dash(f.NewV))
}

// dryUndo is the dry run of undoing change id; pl and perr are what Prepare
// returned. A conflict is reported as data with outcome conflict.
func dryUndo(ctx context.Context, ws *filecmd.Workspace, id int64, pl *Plan, perr error) (output.Result, error) {
	e, isErr := outcome.As(perr)
	if perr != nil && (!isErr || e.Outcome != outcome.Conflict) {
		return output.Result{}, perr
	}
	c := history.Change{ID: id}
	if pl != nil {
		c = pl.Change
	} else {
		st, err := histrec.Store(ctx, ws.Root.Path())
		if err != nil {
			return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
		}
		if c, err = st.Get(ctx, id); err != nil {
			return output.Result{}, perr
		}
	}
	d := DryData{DryRun: true, Files: []DryFile{}, Undo: &DryChange{
		ID: c.ID, Op: c.Op, Path: c.Path, Author: c.Author, Summary: summarize(c),
	}}
	if perr != nil {
		d.Conflict = &DryConflict{Message: e.Message, Path: e.Path, Lines: e.Lines}
		return output.Result{Outcome: outcome.Conflict, Data: d, Hint: e.Hint}, nil
	}
	f := DryFile{Path: pl.Path}
	var frags []linediff.Fragment
	switch pl.Action {
	case ActLines:
		f.Action = DryEdit
		doc := &textfile.Doc{Lines: slices.Clone(pl.File.Lines()), Format: pl.File.Format()}
		for i := len(pl.Hunks) - 1; i >= 0; i-- {
			h := pl.Hunks[i]
			doc.Replace(h.Start-1, len(h.Cur), h.Restore)
			frags = append(frags, linediff.Fragment{Pos: h.Start - 1, Old: h.Cur, New: h.Restore})
		}
		slices.Reverse(frags)
		f.OldV, f.NewV = version.Of(pl.File.Data), version.Of(doc.Bytes())
	case ActRestore:
		f.Action, f.NewV = DryRestore, version.Of(pl.Doc.Bytes())
		frags = []linediff.Fragment{{Pos: 0, New: pl.Doc.Lines}}
	case ActRemove:
		f.Action, f.OldV = DryRemove, version.Of(pl.File.Data)
		frags = []linediff.Fragment{{Pos: 0, Old: pl.File.Lines()}}
	case ActMoveBack:
		f.Action, f.To = DryMove, pl.To
	}
	d.Files = append(d.Files, f)
	d.fill(0, frags, ws.Config.ReadLines)
	return d.result(), nil
}

// dryTo is the dry run of a return to a point.
func dryTo(ws *filecmd.Workspace, pl *ToPlan) (output.Result, error) {
	d := DryData{DryRun: true, To: pl.To, Files: []DryFile{}}
	if len(pl.Restores) == 0 {
		return output.Result{Outcome: outcome.Empty, Data: ToData{To: pl.To, Files: []ToFile{}}}, nil
	}
	for i, r := range pl.Restores {
		f := DryFile{Path: r.Path, Action: DryEdit}
		var before, after []string
		if r.Exists() {
			f.OldV = version.Of(r.Before)
			before = textfile.Parse(r.Before).Lines
		} else {
			f.Action = DryRestore
		}
		if r.Keeps() {
			f.NewV = version.Of(r.After)
			after = r.Doc.Lines
		} else {
			f.Action = DryRemove
		}
		d.Files = append(d.Files, f)
		d.fill(i, linediff.Diff(before, after), ws.Config.ReadLines)
	}
	return d.result(), nil
}

// fill sets the hunks of file i from frags, keeping the total diff within
// limit lines (<= 0: no limit).
func (d *DryData) fill(i int, frags []linediff.Fragment, limit int) {
	c := history.Change{Fragments: frags}
	room := 0
	if limit > 0 {
		room = limit - d.Shown
	}
	full := showcmd.Build(c, 0)
	d.DiffLines += full.DiffLines
	if limit > 0 && room <= 0 {
		d.Truncated = d.Truncated || full.DiffLines > 0
		d.Files[i].Hunks = []showcmd.Hunk{}
		return
	}
	s := full
	if limit > 0 {
		s = showcmd.Build(c, room)
	}
	d.Files[i].Hunks = s.Hunks
	d.Shown += s.Shown
	d.Truncated = d.Truncated || s.Truncated
}

func (d DryData) result() output.Result {
	r := output.Result{Outcome: outcome.OK, Data: d}
	if d.Truncated {
		r.Outcome = outcome.Truncated
		r.Message = fmt.Sprintf("diff cut at %d of %d lines", d.Shown, d.DiffLines)
	}
	return r
}

// summarize describes c as history does: "lines 13-15", "created", ...
func summarize(c history.Change) string {
	switch {
	case c.Extra.From != "":
		return "moved from " + c.Extra.From
	case c.Extra.Created:
		return "created"
	case c.Extra.Removed:
		return "removed"
	case c.Extra.Binary:
		return "binary"
	}
	var rs []changelog.Range
	shift := 0
	for _, f := range c.Fragments {
		start := f.Pos + shift + 1
		rs = append(rs, changelog.Range{Start: start, End: start + len(f.New) - 1})
		shift += len(f.New) - len(f.Old)
	}
	if len(rs) == 0 {
		return ""
	}
	return "lines " + changescmd.FormatRanges(rs)
}
