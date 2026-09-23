package scenarios

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/astralyx/lino/bench/harness"
	"github.com/astralyx/lino/internal/statscmd"
)

// Resource targets from scope.md, Targets. First init (< 30 s) is the
// harness's setup metric.
const idleRSSTargetMB = 200

// History workload: edits spread evenly over files of at least 200 lines.
var historyEdits, historyFiles = 1000, 100

func init() {
	for _, s := range []harness.Scenario{
		{Name: "index-size", Doc: "index size on disk vs source size", Run: indexSize},
		{Name: "history-disk", Doc: "history disk use per 1,000 one-line edits across 100 files", Run: historyDisk},
		{Name: "idle-rss", Doc: "resident memory of the live process, idle after the other scenarios", Run: idleRSS},
	} {
		harness.Register(s)
	}
}

const mb = 1 << 20

// dbBytes sums a SQLite database with its -wal and -shm files (or only the
// files with the given suffixes).
func dbBytes(root, name string, suffixes ...string) int64 {
	if len(suffixes) == 0 {
		suffixes = []string{"", "-wal", "-shm"}
	}
	var n int64
	for _, suf := range suffixes {
		if fi, err := os.Stat(filepath.Join(root, ".lino", name+suf)); err == nil {
			n += fi.Size()
		}
	}
	return n
}

func indexSize(ctx context.Context, e *harness.Env) ([]harness.Metric, error) {
	idx, src := dbBytes(e.Root, "index.db"), e.Corpus.Bytes
	if idx == 0 || src == 0 {
		return nil, fmt.Errorf("index %d bytes, source %d bytes", idx, src)
	}
	return []harness.Metric{
		{Name: "source size", Value: float64(src) / mb, Unit: "MB",
			Note: fmt.Sprintf("%d files, %d lines", e.Corpus.Files, e.Corpus.Lines)},
		{Name: "index size on disk", Value: float64(idx) / mb, Unit: "MB", Note: "index.db with -wal and -shm"},
		{Name: "index / source", Value: float64(idx) / float64(src), Unit: "x"},
	}, nil
}

func stats(ctx context.Context, e *harness.Env) (statscmd.Data, error) {
	var st statscmd.Data
	_, err := e.JSON(ctx, &st, "stats")
	if err == nil && (!st.Live || st.Process == nil) {
		err = fmt.Errorf("stats: no live process")
	}
	return st, err
}

// rss is the resident set size of pid in bytes, from ps (Linux and macOS).
func rss(ctx context.Context, pid int) (int64, error) {
	out, err := exec.CommandContext(ctx, "ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, fmt.Errorf("ps %d: %w", pid, err)
	}
	kb, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return kb << 10, err
}

// idleRSS lets the live process settle, then samples its RSS for a few
// seconds and reports the highest sample. It runs last, so the process has
// served every scenario before it (1,000 edits in a full run).
func idleRSS(ctx context.Context, e *harness.Env) ([]harness.Metric, error) {
	st, err := stats(ctx, e)
	if err != nil {
		return nil, err
	}
	pid := st.Process.PID
	if err := sleep(ctx, 2*time.Second); err != nil {
		return nil, err
	}
	var hi int64
	for range 12 {
		n, err := rss(ctx, pid)
		if err != nil {
			return nil, err
		}
		hi = max(hi, n)
		if err := sleep(ctx, 250*time.Millisecond); err != nil {
			return nil, err
		}
	}
	if st, err = stats(ctx, e); err != nil {
		return nil, err
	}
	return []harness.Metric{
		{Name: "idle RSS", Value: float64(hi) / mb, Unit: "MB", Target: harness.Under(idleRSSTargetMB),
			Note: fmt.Sprintf("max of 12 samples over 3 s, after %d calls", st.Calls)},
		{Name: "peak RSS", Value: float64(st.Process.PeakBytes) / mb, Unit: "MB", Note: "since start, from lino stats"},
		{Name: "Go heap", Value: float64(st.Process.HeapBytes) / mb, Unit: "MB", Note: "from lino stats"},
	}, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// historyDisk makes 1,000 one-line edits in live mode, 10 in each of 100
// files spread over the corpus, each appending a comment to an existing line,
// and reports how much the history database grew.
func historyDisk(ctx context.Context, e *harness.Env) ([]harness.Metric, error) {
	files, err := spreadFiles(e.Root, historyFiles, 200)
	if err != nil {
		return nil, err
	}
	perFile := historyEdits / len(files)
	before, err := stats(ctx, e)
	if err != nil {
		return nil, err
	}
	bytesBefore, mainBefore := dbBytes(e.Root, "history.db"), dbBytes(e.Root, "history.db", "")
	var eds []*editor
	start := time.Now()
	for i, f := range files {
		lines, err := fileLines(filepath.Join(e.Root, filepath.FromSlash(f)))
		if err != nil {
			return nil, err
		}
		v, err := version(ctx, e, f)
		if err != nil {
			return nil, err
		}
		ed := &editor{e: e, path: f, v: v}
		eds = append(eds, ed)
		for j := range perFile {
			n := 1 + (j*len(lines)/perFile+i)%len(lines)
			if err := ed.edit(ctx, n, lines[n-1]+" // lino bench "+strconv.Itoa(j)); err != nil {
				return nil, fmt.Errorf("%s:%d: %w", f, n, err)
			}
		}
	}
	took := time.Since(start)
	after, err := stats(ctx, e)
	if err != nil {
		return nil, err
	}
	edits := perFile * len(files)
	changes := after.History.Changes - before.History.Changes
	grew := dbBytes(e.Root, "history.db") - bytesBefore
	mainGrew := dbBytes(e.Root, "history.db", "") - mainBefore
	fails := harness.Metric{Name: "edit internal errors", Unit: "calls", Target: harness.Under(1)}
	for _, ed := range eds {
		fails.Value += float64(len(ed.internal))
		if fails.Note == "" && len(ed.internal) > 0 {
			fails.Note = "first: " + ed.internal[0]
		}
	}
	if changes < int64(edits) {
		return []harness.Metric{fails}, fmt.Errorf("history recorded %d changes for %d edits", changes, edits)
	}
	return []harness.Metric{
		{Name: "history growth per 1,000 edits", Value: float64(grew) / float64(edits) * 1000 / mb, Unit: "MB",
			Note: fmt.Sprintf("%d edits in %d files, %d changes recorded; history.db with -wal and -shm, %.1f -> %.1f MB",
				edits, len(files), changes, float64(bytesBefore)/mb, float64(bytesBefore+grew)/mb)},
		{Name: "history.db main file growth per 1,000 edits", Value: float64(mainGrew) / float64(edits) * 1000 / mb, Unit: "MB",
			Note: "without -wal and -shm, before any checkpoint of the rest"},
		{Name: "history fragments per 1,000 edits", Value: float64(after.History.FragmentBytes-before.History.FragmentBytes) / float64(edits) * 1000 / mb,
			Unit: "MB", Note: "stored change fragments, from lino stats"},
		{Name: "history total per 1,000 changes", Value: float64(after.History.DBBytes) / float64(after.History.Changes) * 1000 / mb, Unit: "MB",
			Note: fmt.Sprintf("lino stats: whole db over all %d changes, fixed overhead included", after.History.Changes)},
		{Name: "edit wall time, mean", Value: harness.Ms(took) / float64(edits), Unit: "ms", Note: "end-to-end, client process included"},
		fails,
	}, nil
}

// spreadFiles picks n .go files with at least minLines lines, evenly spaced
// over the lexical order of all such files.
func spreadFiles(root string, n, minLines int) ([]string, error) {
	var all []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".lino" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if bytes.Count(b, []byte{'\n'}) >= minLines {
			rel, _ := filepath.Rel(root, p)
			all = append(all, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(all) < n {
		return nil, fmt.Errorf("only %d .go files with %d+ lines", len(all), minLines)
	}
	sort.Strings(all)
	out := make([]string, n)
	for i := range out {
		out[i] = all[i*len(all)/n]
	}
	return out, nil
}

func fileLines(p string) ([]string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n"), nil
}
