// Package scenarios holds the benchmark scenarios; each file registers its
// own with harness.Register from init, in the order they run.
package scenarios

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/astralyx/lino/bench/harness"
	"github.com/astralyx/lino/internal/statscmd"
)

// Latency targets from scope.md, Targets.
const (
	searchTargetMs      = 10
	reindexTargetMs     = 20
	reconstructTargetMs = 50
	externalTargetMs    = 1000
	directTargetMs      = 15
)

func init() {
	for _, s := range []harness.Scenario{
		{Name: "search", Doc: "live search p95 end-to-end (client process included) vs grep -rn and ripgrep", Run: searchLatency},
		{Name: "edit-reindex", Doc: "one-line edit in live mode: write, re-index and history, server side and end-to-end", Run: editReindex},
		{Name: "reconstruct", Doc: "reconstruct a file version 100 changes back (rollback --to --dry-run)", Run: reconstruct},
		{Name: "external-edit", Doc: "an edit made outside lino becomes visible to search and changes", Run: externalEdit},
		{Name: "direct-cold", Doc: "cold --direct calls with no live process (stops and restarts it)", Run: directCold},
	} {
		harness.Register(s)
	}
}

// query is one search, with the equivalent grep -E / ripgrep pattern ("" when
// there is none, e.g. BM25 word search).
type query struct {
	q, mode string // mode: "" literal, "--regex", "--words"
	ere     string
}

var queries = []query{
	{q: "ErrUnexpectedEOF"},
	{q: "ReadFull"},
	{q: "return err"},
	{q: "if err != nil"},
	{q: `func \w+Reader\(`, mode: "--regex", ere: `func [[:alnum:]_]+Reader\(`},
	{q: `ErrUnexpected\w+`, mode: "--regex", ere: `ErrUnexpected[[:alnum:]_]+`},
	{q: "buffer reader", mode: "--words"},
}

// baselineIters caps grep and ripgrep runs: they scan the whole corpus.
const baselineIters = 10

func searchLatency(ctx context.Context, e *harness.Env) ([]harness.Metric, error) {
	rg := ripgrep()
	rgVersion := ""
	if rg == nil {
		e.Logf("ripgrep not found (set LINO_BENCH_RG); skipping it")
	} else {
		c := rg()
		c.Args = append(c.Args, "--version")
		out, err := c.Output()
		if err != nil {
			return nil, fmt.Errorf("rg --version: %w", err)
		}
		rgVersion, _, _ = strings.Cut(string(out), "\n")
	}
	var ms []harness.Metric
	for _, q := range queries {
		args := []string{"search", q.q}
		if q.mode != "" {
			args = append(args, q.mode)
		}
		ts, err := e.Time(func() error { return runOK(ctx, e, "", args...) })
		if err != nil {
			return ms, err
		}
		label := strconv.Quote(q.q)
		if q.mode != "" {
			label += " " + q.mode
		}
		ms = append(ms, p95Metric("lino p95", ts, searchTargetMs, label))
		if q.mode == "--words" {
			continue
		}
		grepArgs := []string{"-rn", "--exclude-dir=.lino", "-F", q.q, "."}
		rgArgs := []string{"-n", "-F", q.q}
		if q.mode == "--regex" {
			grepArgs = []string{"-rn", "--exclude-dir=.lino", "-E", q.ere, "."}
			rgArgs = []string{"-n", q.ere}
		}
		ts, err = baseline(ctx, e, exec.Command("grep", grepArgs...))
		if err != nil {
			return ms, err
		}
		ms = append(ms, p95Metric("grep -rn p95", ts, 0, label))
		if rg != nil {
			c := rg()
			c.Args = append(c.Args, rgArgs...)
			if ts, err = baseline(ctx, e, c); err != nil {
				return ms, err
			}
			ms = append(ms, p95Metric("ripgrep p95", ts, 0, label+", "+rgVersion))
		}
	}
	return ms, nil
}

// ripgrep returns a command factory for rg, from LINO_BENCH_RG or PATH. A
// multi-call binary is started with argv[0] "rg".
func ripgrep() func() *exec.Cmd {
	p := os.Getenv("LINO_BENCH_RG")
	if p == "" {
		var err error
		if p, err = exec.LookPath("rg"); err != nil {
			return nil
		}
	}
	return func() *exec.Cmd {
		c := exec.Command(p)
		c.Args[0] = "rg"
		return c
	}
}

// baseline times a grep-like command in the root (exit 1, no match, is fine).
func baseline(ctx context.Context, e *harness.Env, proto *exec.Cmd) (harness.Timings, error) {
	return harness.Measure(1, min(e.Iters, baselineIters), func() error {
		c := exec.CommandContext(ctx, proto.Path, proto.Args[1:]...)
		c.Args[0] = proto.Args[0]
		c.Dir = e.Root
		var errb bytes.Buffer
		c.Stderr = &errb
		err := c.Run()
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %v: %s", strings.Join(c.Args, " "), err, errb.Bytes())
		}
		return nil
	})
}

func editReindex(ctx context.Context, e *harness.Env) ([]harness.Metric, error) {
	path, lines, err := pickFile(e.Root, 1000, 0)
	if err != nil {
		return nil, err
	}
	v, err := version(ctx, e, path)
	if err != nil {
		return nil, err
	}
	ed := &editor{e: e, path: path, v: v}
	i := 0
	ts, err := e.Time(func() error {
		i++
		return ed.edit(ctx, 1+(i*37)%lines, fmt.Sprintf("// lino bench edit %d", i))
	})
	if err != nil {
		return nil, err
	}
	note := fmt.Sprintf("%s, %d lines, n=%d", path, lines, len(ts))
	ms := []harness.Metric{p95Metric("edit end-to-end p95", ts, 0, note), ed.failures()}
	var st statscmd.Data
	if _, err := e.JSON(ctx, &st, "stats"); err != nil {
		return ms, err
	}
	for _, c := range st.Commands {
		if c.Name == "edit" {
			ms = append(ms, harness.Metric{Name: "edit server-side p95 (write + re-index + history)", Value: float64(c.P95) / 1e3,
				Unit: "ms", Target: harness.Under(reindexTargetMs), Note: fmt.Sprintf("lino stats, n=%d", c.Count)})
		}
	}
	ms = append(ms, harness.Metric{Name: "re-index hook p95", Value: float64(st.Reindex.P95) / 1e3, Unit: "ms",
		Note: fmt.Sprintf("lino stats, n=%d", st.Reindex.Count)})
	return ms, nil
}

func reconstruct(ctx context.Context, e *harness.Env) ([]harness.Metric, error) {
	const back = 100
	path, lines, err := pickFile(e.Root, 1000, 1)
	if err != nil {
		return nil, err
	}
	v, err := version(ctx, e, path)
	if err != nil {
		return nil, err
	}
	ed := &editor{e: e, path: path, v: v}
	var target, targetV string
	for i := 0; i <= back; i++ {
		if err := ed.edit(ctx, 1+(i*53)%lines, fmt.Sprintf("// lino bench reconstruct %d", i)); err != nil {
			return nil, err
		}
		if i == 0 {
			var h struct{ Changes []struct{ ID int64 } }
			if _, err := e.JSON(ctx, &h, "history", path, "-k", "1"); err != nil {
				return nil, err
			}
			if len(h.Changes) == 0 {
				return nil, fmt.Errorf("history %s: empty after an edit", path)
			}
			target, targetV = strconv.FormatInt(h.Changes[0].ID, 10), ed.v
		}
	}
	args := []string{"rollback", "--to", target, "--path", path, "--dry-run"}
	var d struct {
		Files []struct{ Path, Version string }
	}
	if _, err := e.JSON(ctx, &d, args...); err != nil {
		return nil, err
	}
	if len(d.Files) != 1 || d.Files[0].Version != targetV {
		return nil, fmt.Errorf("rollback --to %s: got %+v, want version %s", target, d.Files, targetV)
	}
	ts, err := e.Time(func() error { return runOK(ctx, e, "", args...) })
	if err != nil {
		return nil, err
	}
	return []harness.Metric{p95Metric("reconstruct p95", ts, reconstructTargetMs,
		fmt.Sprintf("%d changes back, %s, %d lines, end-to-end", back, path, lines)), ed.failures()}, nil
}

func externalEdit(ctx context.Context, e *harness.Env) ([]harness.Metric, error) {
	path, _, err := pickFile(e.Root, 1000, 2)
	if err != nil {
		return nil, err
	}
	var cur struct{ Next int64 }
	if _, err := e.JSON(ctx, &cur, "changes", "--since", "0", "--path", path); err != nil {
		return nil, err
	}
	abs := filepath.Join(e.Root, filepath.FromSlash(path))
	n := min(e.Iters, 20)
	var toSearch, toChanges harness.Timings
	for i := range n {
		b, err := os.ReadFile(abs)
		if err != nil {
			return nil, err
		}
		token := fmt.Sprintf("linoBenchExternal%dx%d", i, time.Now().UnixNano())
		b = append(b, []byte("// "+token+"\n")...)
		start := time.Now()
		if err := os.WriteFile(abs, b, 0o644); err != nil {
			return nil, err
		}
		var dSearch, dChanges time.Duration
		for dSearch == 0 || dChanges == 0 {
			if time.Since(start) > 10*time.Second {
				return nil, fmt.Errorf("external edit %d of %s not visible after 10 s (search %v, changes %v)", i, path, dSearch, dChanges)
			}
			if dSearch == 0 {
				var s struct{ Hits []struct{} }
				if _, err := e.JSON(ctx, &s, "search", token, "-k", "1"); err != nil {
					return nil, err
				}
				if len(s.Hits) > 0 {
					dSearch = time.Since(start)
				}
			}
			if dChanges == 0 {
				var c struct {
					Events []struct{}
					Next   int64
				}
				if _, err := e.JSON(ctx, &c, "changes", "--since", strconv.FormatInt(cur.Next, 10), "--path", path); err != nil {
					return nil, err
				}
				if len(c.Events) > 0 {
					dChanges = time.Since(start)
					cur.Next = c.Next
				}
			}
		}
		toSearch, toChanges = append(toSearch, dSearch), append(toChanges, dChanges)
	}
	note := fmt.Sprintf("%s, n=%d, polled by separate client calls", path, n)
	return []harness.Metric{
		p95Metric("visible to search p95", toSearch, externalTargetMs, note),
		{Name: "visible to search max", Value: harness.Ms(toSearch.Max()), Unit: "ms"},
		p95Metric("visible to changes p95", toChanges, externalTargetMs, note),
		{Name: "visible to changes max", Value: harness.Ms(toChanges.Max()), Unit: "ms"},
	}, nil
}

func directCold(ctx context.Context, e *harness.Env) (ms []harness.Metric, err error) {
	if err := runOK(ctx, e, "", "stop"); err != nil {
		return nil, err
	}
	defer func() {
		if _, rerr := e.JSON(ctx, nil, "run"); rerr != nil && err == nil {
			err = fmt.Errorf("restart live process: %w", rerr)
		}
	}()
	path, _, err := pickFile(e.Root, 1000, 3)
	if err != nil {
		return nil, err
	}
	if hello, done, err := emptyGoBinary(ctx); err != nil {
		e.Logf("no empty Go binary baseline: %v", err)
	} else {
		defer done()
		ts, err := e.Time(func() error { return exec.CommandContext(ctx, hello).Run() })
		if err != nil {
			return nil, err
		}
		ms = append(ms, p95Metric("baseline: empty Go program", ts, 0, fmt.Sprintf("n=%d", len(ts))))
	}
	for _, c := range []struct {
		name   string
		target float64
		args   []string
	}{
		{"process start (lino --version)", 0, []string{"--version"}},
		{"read --direct p95", directTargetMs, []string{"read", path, "--lines", "1:50", "--direct"}},
		{"search --direct p95", directTargetMs, []string{"search", "ErrUnexpectedEOF", "--direct"}},
		{"ls --direct p95", 0, []string{"ls", "--depth", "1", "--direct"}},
	} {
		ts, err := e.Time(func() error { return runOK(ctx, e, "", c.args...) })
		if err != nil {
			return ms, err
		}
		ms = append(ms, p95Metric(c.name, ts, c.target, fmt.Sprintf("n=%d", len(ts))))
	}
	return ms, nil
}

// editor makes one-line edits to one file, tracking its version. A call
// that fails with outcome internal (seen: SQLITE_BUSY under rapid edits) is
// counted and retried with a fresh version, so latency is still measured.
type editor struct {
	e        *harness.Env
	path, v  string
	calls    int
	internal []string
}

const editRetries = 3

func (ed *editor) edit(ctx context.Context, line int, text string) error {
	l := strconv.Itoa(line)
	for try := 0; ; try++ {
		ed.calls++
		var d struct{ Version string }
		env, err := ed.e.JSONIn(ctx, &d, text+"\n", "edit", ed.path, l, l, "--v", ed.v)
		if err == nil {
			ed.v = d.Version
			return nil
		}
		if env.Outcome != "internal" || try == editRetries {
			return err
		}
		ed.internal = append(ed.internal, env.Message)
		if ed.v, err = version(ctx, ed.e, ed.path); err != nil {
			return err
		}
	}
}

// failures is the count of internal errors as a metric that must be zero.
func (ed *editor) failures() harness.Metric {
	m := harness.Metric{Name: "edit internal errors", Value: float64(len(ed.internal)), Unit: "calls",
		Target: harness.Under(1), Note: fmt.Sprintf("of %d edit calls", ed.calls)}
	if len(ed.internal) > 0 {
		m.Note += "; first: " + ed.internal[0]
	}
	return m
}

// emptyGoBinary builds `func main() {}` so process start cost can be told
// apart from lino's own.
func emptyGoBinary(ctx context.Context) (string, func(), error) {
	dir, err := os.MkdirTemp("", "lb-hello-")
	if err != nil {
		return "", nil, err
	}
	done := func() { os.RemoveAll(dir) }
	files := map[string]string{"go.mod": "module hello\n\ngo 1.24\n", "main.go": "package main\n\nfunc main() {}\n"}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			done()
			return "", nil, err
		}
	}
	c := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", "hello", ".")
	c.Dir = dir
	c.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=")
	if out, err := c.CombinedOutput(); err != nil {
		done()
		return "", nil, fmt.Errorf("%v: %s", err, out)
	}
	return filepath.Join(dir, "hello"), done, nil
}

// runOK runs lino and fails on a non-zero exit.
func runOK(ctx context.Context, e *harness.Env, stdin string, args ...string) error {
	r, err := e.Run(ctx, stdin, args...)
	if err != nil {
		return err
	}
	if r.Exit != 0 {
		return fmt.Errorf("lino %s: exit %d: %s", strings.Join(args, " "), r.Exit, r.Stderr)
	}
	return nil
}

func version(ctx context.Context, e *harness.Env, path string) (string, error) {
	var d struct{ Version string }
	_, err := e.JSON(ctx, &d, "read", path, "--lines", "1:1")
	return d.Version, err
}

func p95Metric(name string, ts harness.Timings, target float64, note string) harness.Metric {
	m := harness.Metric{Name: name, Value: harness.Ms(ts.Percentile(95)), Unit: "ms",
		Note: fmt.Sprintf("%s; p50 %.2f ms", note, harness.Ms(ts.Percentile(50)))}
	if target > 0 {
		m.Target = harness.Under(target)
	}
	return m
}

// pickFile returns the nth (0-based) .go file, in lexical order, with at
// least minLines lines, so each scenario edits its own file.
func pickFile(root string, minLines, nth int) (string, int, error) {
	var found string
	var n int
	errFound := fmt.Errorf("found")
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
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
		if c := bytes.Count(b, []byte{'\n'}); c >= minLines {
			if nth > 0 {
				nth--
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			found, n = filepath.ToSlash(rel), c
			return errFound
		}
		return nil
	})
	if err == errFound {
		return found, n, nil
	}
	if err == nil {
		err = fmt.Errorf("no .go file with %d+ lines in the corpus", minLines)
	}
	return "", 0, err
}
