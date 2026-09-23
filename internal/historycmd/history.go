// Package historycmd implements `lino history`: recorded changes, newest
// first, filtered by path, position and author.
package historycmd

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/astralyx/lino/internal/changelog"
	"github.com/astralyx/lino/internal/changescmd"
	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/histrec"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/paths"
)

func init() { cli.Register(Command) }

// DefaultK and MaxK bound -k.
const (
	DefaultK = 20
	MaxK     = 500
)

// Command is `lino history`.
var Command = &cli.Command{
	Name:    "history",
	Usage:   "[path] [--since N] [--by A] [-k N]",
	Summary: "list recorded changes, newest first",
	Accept:  cli.File,
	MaxArgs: 1,
	Setup: func(fs *flag.FlagSet) cli.RunFunc {
		since := fs.Int64("since", 0, "only changes with id > N")
		by := fs.String("by", "", "only changes by this author")
		k := fs.Int("k", DefaultK, "at most N changes (max "+strconv.Itoa(MaxK)+")")
		return func(ctx context.Context, c *cli.Call) (output.Result, error) {
			if *k < 1 || *k > MaxK {
				return output.Result{}, outcome.New(outcome.Usage, "-k %d: want 1..%d", *k, MaxK)
			}
			if *since < 0 {
				return output.Result{}, outcome.New(outcome.Usage, "--since %d: want N >= 0", *since)
			}
			return Run(ctx, c.Cwd, Request{Path: c.Arg(0), Since: *since, By: *by, K: *k})
		}
	},
}

// Request is one history call. Path is relative to cwd, like other file
// arguments; a directory matches everything below it.
type Request struct {
	Path  string
	Since int64
	By    string
	K     int
}

// Entry is one listed change.
type Entry struct {
	ID      int64             `json:"id"`
	Time    time.Time         `json:"time"`
	Source  string            `json:"source"`
	Author  string            `json:"author,omitempty"`
	Op      string            `json:"op"`
	Path    string            `json:"path"`
	From    string            `json:"from,omitempty"`
	Undid   int64             `json:"undid,omitempty"`
	Created bool              `json:"created,omitempty"`
	Removed bool              `json:"removed,omitempty"`
	Binary  bool              `json:"binary,omitempty"`
	VBefore string            `json:"v_before,omitempty"`
	VAfter  string            `json:"v_after,omitempty"`
	Ranges  []changelog.Range `json:"ranges,omitempty"`
	Summary string            `json:"summary"`
}

// Data is the result, newest first.
type Data struct {
	Changes []Entry `json:"changes"`
	now     time.Time
}

// Run lists changes in the workspace found from cwd.
func Run(ctx context.Context, cwd string, req Request) (output.Result, error) {
	if req.K <= 0 {
		req.K = DefaultK
	}
	ws, err := filecmd.Open(cwd)
	if err != nil {
		return output.Result{}, err
	}
	st, err := histrec.Store(ctx, ws.Root.Path())
	if err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "history: "+err.Error())
	}
	f := history.Filter{Since: req.Since, By: req.By, Limit: req.K + 1}
	exact, prefix, err := pathFilter(ws.Root, cwd, req.Path)
	if err != nil {
		return output.Result{}, err
	}
	// A path is tried exactly first, then as a directory prefix.
	var cs []history.Change
	for _, p := range []string{exact, prefix} {
		if p == "" && (exact != "" || prefix != "") {
			continue
		}
		f.Path = p
		if cs, err = st.List(ctx, f); err != nil {
			return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
		}
		if len(cs) > 0 || p == "" {
			break
		}
	}
	more := len(cs) > req.K
	if more {
		cs = cs[:req.K]
	}
	shapes, err := fragmentShapes(ctx, st, cs)
	if err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
	}
	d := Data{Changes: make([]Entry, len(cs)), now: time.Now()}
	for i, c := range cs {
		d.Changes[i] = entry(c, shapes[c.ID])
	}
	res := output.Result{Data: d}
	switch {
	case more:
		res.Outcome = outcome.Truncated
		res.Message = fmt.Sprintf("showing the newest %d; older changes exist", req.K)
		res.Hint = "-k " + strconv.Itoa(min(req.K*2, MaxK))
	case len(cs) == 0:
		res.Outcome = outcome.Empty
		res.Message = "no changes"
	}
	return res, nil
}

// pathFilter turns the path argument into an exact root-relative path and a
// directory prefix ("dir/"). A path naming an existing directory, or ending
// in "/", is only a prefix; any other path is tried exactly, then as a prefix
// (a directory that no longer exists).
func pathFilter(root *paths.Root, cwd, p string) (exact, prefix string, err error) {
	if p == "" {
		return "", "", nil
	}
	rel, abs, err := root.Resolve(cwd, p)
	if err != nil {
		return "", "", outcome.New(outcome.Refused, "%s: outside the root", p)
	}
	if rel == "." {
		return "", "", nil
	}
	if fi, err := os.Stat(abs); err == nil && fi.IsDir() || strings.HasSuffix(p, "/") {
		return "", rel + "/", nil
	}
	return rel, rel + "/", nil
}

type shape struct{ pos, oldN, newN int }

// fragmentShapes reads fragment positions and sizes, without their lines.
func fragmentShapes(ctx context.Context, st *history.Store, cs []history.Change) (map[int64][]shape, error) {
	out := map[int64][]shape{}
	if len(cs) == 0 {
		return out, nil
	}
	lo, hi := cs[len(cs)-1].ID, cs[0].ID
	rows, err := st.SQL.QueryContext(ctx,
		`SELECT change_id, pos, old_n, new_n FROM fragments WHERE change_id BETWEEN ? AND ? ORDER BY change_id, seq`, lo, hi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	want := make(map[int64]bool, len(cs))
	for _, c := range cs {
		want[c.ID] = true
	}
	for rows.Next() {
		var id int64
		var s shape
		if err := rows.Scan(&id, &s.pos, &s.oldN, &s.newN); err != nil {
			return nil, err
		}
		if want[id] {
			out[id] = append(out[id], s)
		}
	}
	return out, rows.Err()
}

// ranges converts fragments (positioned in the version before) into line
// ranges in the version after, as changes reports them.
func ranges(fs []shape) []changelog.Range {
	var out []changelog.Range
	shift := 0
	for _, f := range fs {
		start := f.pos + shift + 1
		out = append(out, changelog.Range{Start: start, End: start + f.newN - 1})
		shift += f.newN - f.oldN
	}
	return out
}

func entry(c history.Change, fs []shape) Entry {
	e := Entry{
		ID: c.ID, Time: c.Time, Source: c.Source, Author: c.Author, Op: c.Op, Path: c.Path,
		From: c.Extra.From, Undid: c.Extra.Undid, Created: c.Extra.Created, Removed: c.Extra.Removed,
		Binary: c.Extra.Binary, VBefore: c.VBefore, VAfter: c.VAfter,
	}
	if !e.Created && !e.Removed && e.From == "" && !e.Binary {
		e.Ranges = ranges(fs)
	}
	e.Summary = summary(e)
	return e
}

func summary(e Entry) string {
	var parts []string
	if e.Undid != 0 {
		parts = append(parts, "undid "+strconv.FormatInt(e.Undid, 10))
	}
	switch {
	case e.From != "":
		parts = append(parts, "moved from "+e.From)
	case e.Created:
		parts = append(parts, "created")
	case e.Removed:
		parts = append(parts, "removed")
	case len(e.Ranges) > 0:
		parts = append(parts, "lines "+changescmd.FormatRanges(e.Ranges))
	}
	if e.Binary {
		parts = append(parts, "binary")
	}
	return strings.Join(parts, ", ")
}

// WriteText prints one aligned line per change:
// id, time, source, author ("-" if none), op, path, summary.
func (d Data) WriteText(w io.Writer) error {
	now := d.now
	if now.IsZero() {
		now = time.Now()
	}
	rows := make([][]string, len(d.Changes))
	for i, e := range d.Changes {
		author := e.Author
		if author == "" {
			author = "-"
		}
		rows[i] = []string{strconv.FormatInt(e.ID, 10), stamp(e.Time, now), e.Source, author, e.Op, e.Path, e.Summary}
	}
	return writeColumns(w, rows)
}

// stamp is the time of day for today's changes, else date and time.
func stamp(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return t.Format("15:04:05")
	}
	return t.Format("2006-01-02 15:04:05")
}

func writeColumns(w io.Writer, rows [][]string) error {
	if len(rows) == 0 {
		return nil
	}
	width := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			width[i] = max(width[i], len([]rune(c)))
		}
	}
	for _, r := range rows {
		var b strings.Builder
		for i, c := range r {
			b.WriteString(c)
			if i < len(r)-1 {
				b.WriteString(strings.Repeat(" ", width[i]-len([]rune(c))+2))
			}
		}
		if _, err := fmt.Fprintln(w, strings.TrimRight(b.String(), " ")); err != nil {
			return err
		}
	}
	return nil
}
