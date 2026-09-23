// Package statscmd implements `lino stats`: index and history size, process
// memory, and the per-command counters the live process collects.
package statscmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/astralyx/lino/internal/autostart"
	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/histrec"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/live"
	"github.com/astralyx/lino/internal/lscmd"
	"github.com/astralyx/lino/internal/metrics"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/registry"
)

func init() {
	cli.Register(Command)
	// Asking for numbers must not start a process (and count a start).
	autostart.NoStart["stats"] = true
}

// Command is `lino stats`.
var Command = &cli.Command{
	Name:    "stats",
	Summary: "index and history size, memory, per-command latency and outcome rates",
	Accept:  cli.File,
	MaxArgs: 0,
	Setup: func(*flag.FlagSet) cli.RunFunc {
		return func(ctx context.Context, c *cli.Call) (output.Result, error) {
			return Stats(ctx, c.ID, c.Env("LINO_ID"), c.Cwd)
		}
	},
}

// Mutating are the commands whose conflict and anchor_mismatch rates are reported.
var Mutating = []string{"edit", "insert", "delete", "replace", "write", "mv", "rm", "rollback"}

// Data is what stats reports. Latencies are in microseconds.
type Data struct {
	Root     string     `json:"root"`
	ID       string     `json:"id"`
	Live     bool       `json:"live"`
	Process  *Process   `json:"process,omitempty"`
	Index    IndexStats `json:"index"`
	History  HistStats  `json:"history"`
	Since    *time.Time `json:"since,omitempty"`
	Starts   uint64     `json:"starts"`
	External uint64     `json:"external"`
	Calls    uint64     `json:"calls"`
	Rates    []Rate     `json:"rates"`
	Reindex  Latency    `json:"reindex"`
	Commands []CmdStats `json:"commands"`
}

// Process is the live process's memory.
type Process struct {
	PID       int   `json:"pid"`
	RSSBytes  int64 `json:"rss_bytes,omitempty"` // current; 0 where unknown (macOS)
	PeakBytes int64 `json:"peak_rss_bytes"`
	HeapBytes int64 `json:"heap_bytes"`
}

// IndexStats sizes the index.
type IndexStats struct {
	index.Totals
	DBBytes int64 `json:"db_bytes"`
}

// HistStats sizes the history.
type HistStats struct {
	Changes       int64 `json:"changes"`
	FragmentBytes int64 `json:"fragment_bytes"`
	DBBytes       int64 `json:"db_bytes"`
	// Per1000 projects fragment bytes to 1,000 changes; nil below MinProjection
	// changes, where fixed costs and one-off edits would dominate.
	Per1000 *int64 `json:"bytes_per_1000_changes,omitempty"`
}

// MinProjection is the fewest changes a per-1,000 projection is made from.
const MinProjection = 100

// Rate is the share of mutating calls that ended with Outcome.
type Rate struct {
	Outcome outcome.Outcome `json:"outcome"`
	Count   uint64          `json:"count"`
	Of      uint64          `json:"of"`
	Rate    float64         `json:"rate"`
}

// Latency summarises a histogram.
type Latency struct {
	Count uint64 `json:"count"`
	P50   int64  `json:"p50_us"`
	P95   int64  `json:"p95_us"`
	Max   int64  `json:"max_us"`
}

// CmdStats is one command's counters.
type CmdStats struct {
	Name string `json:"name"`
	Latency
	Outcomes map[outcome.Outcome]uint64 `json:"outcomes"`
}

func latency(h *metrics.Histogram) Latency {
	if h == nil {
		return Latency{}
	}
	us := func(d time.Duration) int64 { return int64(d / time.Microsecond) }
	return Latency{Count: h.Count, P50: us(h.Quantile(0.5)), P95: us(h.Quantile(0.95)), Max: us(h.Max)}
}

// Stats reports on the root chosen by -i, LINO_ID or cwd. Inside the live
// process the counters are the running ones; in direct mode they come from
// the last saved stats file.
func Stats(ctx context.Context, flagID, envID, cwd string) (output.Result, error) {
	var d Data
	var col *metrics.Collector
	var db *index.DB
	if p := live.FromContext(ctx); p != nil {
		d.Root, d.ID, d.Live, db = p.Root, p.ID, true, p.DB
		d.Process = processStats()
		col = metrics.For(p)
	} else {
		reg, err := registry.Default()
		if err != nil {
			return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
		}
		t, err := reg.Resolve(flagID, envID, cwd)
		if err != nil {
			return output.Result{}, err
		}
		d.Root, d.ID = t.Root, t.ID
		if _, err := os.Stat(index.Path(t.Root)); err == nil {
			if db, err = index.Open(ctx, t.Root); err != nil {
				return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
			}
			defer db.Close()
		}
	}
	if col == nil {
		col = metrics.Load(metrics.Path(d.Root))
	}
	if db != nil {
		t, err := db.Totals(ctx)
		if err != nil {
			return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
		}
		d.Index.Totals = t
	}
	d.Index.DBBytes = dbSize(index.Path(d.Root))
	if err := histStats(ctx, d.Root, &d.History); err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "history: "+err.Error())
	}
	fillMetrics(&d, col.Snapshot())
	return output.Result{Data: d}, nil
}

func processStats() *Process {
	cur, peak := rss()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return &Process{PID: os.Getpid(), RSSBytes: cur, PeakBytes: peak, HeapBytes: int64(ms.HeapAlloc)}
}

// dbSize is the size of a SQLite file plus its WAL.
func dbSize(path string) int64 {
	var n int64
	for _, p := range []string{path, path + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			n += fi.Size()
		}
	}
	return n
}

func histStats(ctx context.Context, root string, h *HistStats) error {
	if _, err := os.Stat(history.Path(root)); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	st, err := histrec.Store(ctx, root)
	if err != nil {
		return err
	}
	if err := st.SQL.QueryRowContext(ctx, `SELECT count(*) FROM changes`).Scan(&h.Changes); err != nil {
		return err
	}
	err = st.SQL.QueryRowContext(ctx,
		`SELECT coalesce(sum(coalesce(length(old_lines),0)+coalesce(length(new_lines),0)),0) FROM fragments`).Scan(&h.FragmentBytes)
	if err != nil {
		return err
	}
	h.DBBytes = dbSize(history.Path(root))
	h.Per1000 = per1000(h.FragmentBytes, h.Changes)
	return nil
}

func per1000(fragBytes, changes int64) *int64 {
	if changes < MinProjection {
		return nil
	}
	n := fragBytes * 1000 / changes
	return &n
}

func fillMetrics(d *Data, s *metrics.Stats) {
	if s.Total() > 0 || s.Starts > 0 {
		since := s.Since
		d.Since = &since
	}
	d.Starts, d.External, d.Calls = s.Starts, s.External, s.Total()
	d.Reindex = latency(s.Reindex)
	var of uint64
	for _, n := range Mutating {
		if c := s.Commands[n]; c != nil {
			of += c.Count
		}
	}
	for _, o := range []outcome.Outcome{outcome.Conflict, outcome.AnchorMismatch} {
		r := Rate{Outcome: o, Of: of}
		for _, n := range Mutating {
			if c := s.Commands[n]; c != nil {
				r.Count += c.Outcomes[o]
			}
		}
		if of > 0 {
			r.Rate = float64(r.Count) / float64(of)
		}
		d.Rates = append(d.Rates, r)
	}
	d.Commands = []CmdStats{}
	for _, n := range s.Names() {
		c := s.Commands[n]
		d.Commands = append(d.Commands, CmdStats{Name: n, Latency: latency(c.Latency), Outcomes: c.Outcomes})
	}
}

// WriteText prints one fact per line, then a per-command table.
func (d Data) WriteText(w io.Writer) error {
	p := func(k, format string, a ...any) {
		fmt.Fprintf(w, "%-10s%s\n", k, fmt.Sprintf(format, a...))
	}
	size := lscmd.FormatSize
	p("root", "%s", d.Root)
	p("id", "%s", d.ID)
	if pr := d.Process; pr != nil {
		mem := "peak rss " + size(pr.PeakBytes)
		if pr.RSSBytes > 0 {
			mem = "rss " + size(pr.RSSBytes) + ", " + mem
		}
		p("process", "live, pid %d, %s, heap %s", pr.PID, mem, size(pr.HeapBytes))
	} else {
		p("process", "not running")
	}
	p("index", "%d files, %d lines, %d binary, index.db %s", d.Index.Files, d.Index.Lines, d.Index.Binary, size(d.Index.DBBytes))
	h := d.History
	switch {
	case h.Per1000 != nil:
		p("history", "%d changes, history.db %s, fragments %s (~%s per 1,000 changes)", h.Changes, size(h.DBBytes), size(h.FragmentBytes), size(*h.Per1000))
	case h.Changes > 0:
		p("history", "%d changes, history.db %s, fragments %s", h.Changes, size(h.DBBytes), size(h.FragmentBytes))
	default:
		p("history", "0 changes, history.db %s", size(h.DBBytes))
	}
	if d.Since == nil {
		p("metrics", "none collected yet")
		return nil
	}
	p("since", "%s, %d starts, %d external changes", d.Since.Local().Format("2006-01-02 15:04:05"), d.Starts, d.External)
	var rates []string
	for _, r := range d.Rates {
		rates = append(rates, fmt.Sprintf("%s %d/%d (%.1f%%)", r.Outcome, r.Count, r.Of, 100*r.Rate))
	}
	p("calls", "%d; mutations: %s", d.Calls, strings.Join(rates, ", "))
	p("reindex", "%d, p50 %s, p95 %s, max %s", d.Reindex.Count, us(d.Reindex.P50), us(d.Reindex.P95), us(d.Reindex.Max))
	if len(d.Commands) == 0 {
		return nil
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "COMMAND\tCALLS\tP50\tP95\tOUTCOMES")
	for _, c := range d.Commands {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n", c.Name, c.Count, us(c.P50), us(c.P95), outcomes(c.Outcomes))
	}
	return tw.Flush()
}

// outcomes renders counts, most frequent first: "ok 12, conflict 1".
func outcomes(m map[outcome.Outcome]uint64) string {
	type kv struct {
		o outcome.Outcome
		n uint64
	}
	var xs []kv
	for o, n := range m {
		if n > 0 {
			xs = append(xs, kv{o, n})
		}
	}
	sort.Slice(xs, func(i, j int) bool {
		if xs[i].n != xs[j].n {
			return xs[i].n > xs[j].n
		}
		return xs[i].o < xs[j].o
	})
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprintf("%s %d", x.o, x.n)
	}
	return strings.Join(parts, ", ")
}

// us renders microseconds compactly: 850µs, 1.2ms, 35ms, 2.1s.
func us(n int64) string {
	d := time.Duration(n) * time.Microsecond
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", n)
	case d < 10*time.Millisecond:
		return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond))
	case d < time.Second:
		return fmt.Sprintf("%dms", d/time.Millisecond)
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}
