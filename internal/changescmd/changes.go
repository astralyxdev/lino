// Package changescmd implements `lino changes`: every change with a sequence
// number above the caller's position, optionally filtered by path globs, and
// the next position to pass.
package changescmd

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/astralyx/lino/internal/changelog"
	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/config"
	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/live"
	"github.com/astralyx/lino/internal/metrics"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/search"
)

func init() { cli.Register(Command) }

// Command is `lino changes`.
var Command = &cli.Command{
	Name:    "changes",
	Usage:   "[--since N] [--path P ...] [--wait DURATION]",
	Summary: "changes after position N, and the next position",
	Accept:  cli.File,
	MaxArgs: 0,
	Setup: func(fs *flag.FlagSet) cli.RunFunc {
		since := fs.Int64("since", -1, "list changes with id > N (default: since the live process started)")
		var paths cli.StringList
		fs.Var(&paths, "path", "only changes to paths matching this glob (repeatable)")
		wait := fs.String("wait", "", "hold the call until a matching change arrives or this long passes, e.g. 30s")
		return func(ctx context.Context, c *cli.Call) (output.Result, error) {
			req := Request{Since: *since, Paths: paths}
			if *wait != "" {
				d, err := config.ParseDuration(*wait)
				if err != nil {
					return output.Result{}, outcome.New(outcome.Usage, "--wait %q: %v", *wait, err).WithHint("--wait 30s")
				}
				req.Wait = d
			}
			return Run(ctx, c.Cwd, req)
		}
	},
}

// Request is one changes call. Since < 0 means "since the live process
// started"; without a live process it is a usage error.
type Request struct {
	Since int64
	Paths []string
	Wait  time.Duration // hold an empty result up to this long; 0 = answer now
}

// Data is the result: events oldest first and the position to pass next.
type Data struct {
	Events []changelog.Entry `json:"events"`
	Next   int64             `json:"next"`
}

// pageSize is how many log rows are read per query while filtering.
const pageSize = 1000

// Run lists changes after req.Since in the workspace found from cwd.
func Run(ctx context.Context, cwd string, req Request) (output.Result, error) {
	ws, err := filecmd.Open(cwd)
	if err != nil {
		return output.Result{}, err
	}
	if req.Since < 0 {
		p := live.FromContext(ctx)
		if p == nil {
			return output.Result{}, outcome.New(outcome.Usage, "--since is required without a live process").
				WithHint("lino changes --since N")
		}
		start, ok := StartSeq(p)
		if !ok {
			return output.Result{}, outcome.New(outcome.Internal, "live process start position unknown").
				WithHint("lino changes --since N")
		}
		req.Since = start
	}
	db, err := search.OpenIndex(ctx, ws)
	if err != nil {
		return output.Result{}, err
	}
	defer db.Close()
	log, err := changelog.Open(ctx, db)
	if err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
	}
	changed := log.Changed() // before reading, so no append is missed
	latest, err := log.Latest(ctx)
	if err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
	}
	var globs []string
	if len(req.Paths) > 0 {
		dir, _, err := ws.Root.Resolve(cwd, "")
		if err != nil {
			dir = "."
		}
		globs = search.NormalizeGlobs(req.Paths, dir)
	}
	evs, more, err := collect(ctx, log, req.Since, globs, ws.Config.ChangesEvents)
	if err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
	}
	if len(evs) == 0 && req.Wait > 0 {
		t0 := time.Now()
		evs, more, latest, err = wait(ctx, log, changed, max(latest, req.Since), globs, ws.Config.ChangesEvents, req.Wait)
		metrics.Waited(ctx, time.Since(t0))
		if err != nil {
			return output.Result{}, err
		}
	}
	d := Data{Events: evs, Next: max(latest, req.Since)}
	res := output.Result{Data: d}
	switch {
	case more:
		d.Next = evs[len(evs)-1].Seq
		res.Data = d
		res.Outcome = outcome.Truncated
		res.Message = fmt.Sprintf("truncated at %d events", len(evs))
		res.Hint = fmt.Sprintf("--since %d", d.Next)
	case len(evs) == 0:
		res.Outcome = outcome.Empty
		res.Message = "no changes"
	}
	return res, nil
}

// collect reads the log after since, keeps entries matching globs, and stops
// at limit. more reports that matching entries beyond limit exist.
func collect(ctx context.Context, log *changelog.Log, since int64, globs []string, limit int) (evs []changelog.Entry, more bool, err error) {
	evs = []changelog.Entry{}
	for pos := since; ; {
		page, err := log.Since(ctx, pos, pageSize)
		if err != nil {
			return nil, false, err
		}
		for _, e := range page {
			if !matches(globs, e) {
				continue
			}
			if limit > 0 && len(evs) >= limit {
				return evs, true, nil
			}
			evs = append(evs, e)
		}
		if len(page) < pageSize {
			return evs, false, nil
		}
		pos = page[len(page)-1].Seq
	}
}

func matches(globs []string, e changelog.Entry) bool {
	return search.MatchPath(globs, e.Path) || e.From != "" && search.MatchPath(globs, e.From)
}

// WriteText prints one line per event, then "next N".
func (d Data) WriteText(w io.Writer) error {
	seqW, pathW := 0, 0
	for _, e := range d.Events {
		seqW = max(seqW, len(strconv.FormatInt(e.Seq, 10)))
		pathW = max(pathW, len(e.Path))
	}
	for _, e := range d.Events {
		line := fmt.Sprintf("%-*d  %-8s  %-*s", seqW, e.Seq, e.Kind, pathW, e.Path)
		if tail := detail(e); tail != "" {
			line += "  " + tail
		}
		if _, err := fmt.Fprintln(w, strings.TrimRight(line, " ")); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "next %d\n", d.Next)
	return err
}

// detail is the event's version and line summary, e.g. "v=8c21e0→5f02aa  lines 12-18".
func detail(e changelog.Entry) string {
	var parts []string
	if e.Kind == changelog.Moved {
		parts = append(parts, "from "+e.From)
	}
	switch {
	case e.VBefore != "" && e.VAfter != "" && e.VBefore != e.VAfter:
		parts = append(parts, "v="+e.VBefore+"→"+e.VAfter)
	case e.VAfter != "":
		parts = append(parts, "v="+e.VAfter)
	case e.VBefore != "":
		parts = append(parts, "v="+e.VBefore)
	}
	if e.Kind == changelog.Modified && len(e.Ranges) > 0 {
		parts = append(parts, "lines "+FormatRanges(e.Ranges))
	}
	return strings.Join(parts, "  ")
}

// FormatRanges renders ranges as "12-18,30,41(del)": a single line as its
// number, and lines removed before line N as "N(del)".
func FormatRanges(rs []changelog.Range) string {
	parts := make([]string, len(rs))
	for i, r := range rs {
		switch {
		case r.End < r.Start:
			parts[i] = strconv.Itoa(r.Start) + "(del)"
		case r.End == r.Start:
			parts[i] = strconv.Itoa(r.Start)
		default:
			parts[i] = strconv.Itoa(r.Start) + "-" + strconv.Itoa(r.End)
		}
	}
	return strings.Join(parts, ",")
}
