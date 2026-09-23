package scenarios

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/astralyx/lino/bench/harness"
)

func init() {
	harness.Register(harness.Scenario{
		Name: "vs-coreutils",
		Doc:  "per-call latency of lino read / read --lines / read --anchors / edit / search vs cat, sed -n, sed -i, grep -rn and ripgrep",
		Run:  vsCoreutils,
	})
}

// coreutilsQuery is the literal the search comparison looks for.
const coreutilsQuery = "ReadFull"

// pairing is one operation timed with lino and with the tools it replaces.
type pairing struct {
	op    string
	lino  func() error
	tools []namedCmd
	// capped runs the tools at most baselineIters times (they scan the corpus).
	capped bool
}

type namedCmd struct {
	name string
	cmd  func(i int) *exec.Cmd
}

func vsCoreutils(ctx context.Context, e *harness.Env) ([]harness.Metric, error) {
	path, lines, err := pickFile(e.Root, 1000, 3)
	if err != nil {
		return nil, err
	}
	from := max(lines/2, 1)
	to := from + 49
	span := fmt.Sprintf("%d:%d", from, to)
	inRoot := func(name string, args ...string) func(int) *exec.Cmd {
		return func(int) *exec.Cmd {
			c := exec.CommandContext(ctx, name, args...)
			c.Dir = e.Root
			return c
		}
	}

	// sed -i edits a copy outside the root, so the live process's watcher
	// does not re-index behind lino's back.
	tmp, err := os.MkdirTemp("", "lino-bench-sed-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	b, err := os.ReadFile(filepath.Join(e.Root, filepath.FromSlash(path)))
	if err != nil {
		return nil, err
	}
	copyPath := filepath.Join(tmp, filepath.Base(path))
	if err := os.WriteFile(copyPath, b, 0o644); err != nil {
		return nil, err
	}
	editLine := func(i int) int { return 1 + (i*37)%lines }
	gnu := gnuSed()
	sedEdit := func(i int) *exec.Cmd {
		return exec.CommandContext(ctx, "sed", sedInPlace(gnu,
			fmt.Sprintf("%ds/.*/\\/\\/ sed bench edit %d/", editLine(i), i), copyPath)...)
	}

	v, err := version(ctx, e, path)
	if err != nil {
		return nil, err
	}
	ed := &editor{e: e, path: path, v: v}
	edits := 0
	search := []namedCmd{{"grep -rn", inRoot("grep", "-rn", "--exclude-dir=.lino", "-F", coreutilsQuery, ".")}}
	if rg := ripgrep(); rg != nil {
		search = append(search, namedCmd{"ripgrep", func(int) *exec.Cmd {
			c := rg()
			c.Args = append(c.Args, "-n", "-F", coreutilsQuery)
			c.Dir = e.Root
			return c
		}})
	} else {
		e.Logf("ripgrep not found (set LINO_BENCH_RG or RG); skipping it")
	}

	pairs := []pairing{
		{op: "read whole file", lino: func() error { return runOK(ctx, e, "", "read", path) },
			tools: []namedCmd{{"cat", inRoot("cat", path)}}},
		{op: "read 50 lines", lino: func() error { return runOK(ctx, e, "", "read", path, "--lines", span) },
			tools: []namedCmd{{"sed -n", inRoot("sed", "-n", fmt.Sprintf("%d,%dp", from, to), path)}}},
		{op: "read --anchors whole file", lino: func() error { return runOK(ctx, e, "", "read", path, "--anchors") },
			tools: []namedCmd{{"cat", inRoot("cat", path)}}},
		{op: "one-line edit", lino: func() error {
			edits++
			return ed.edit(ctx, editLine(edits), fmt.Sprintf("// lino bench edit %d", edits))
		}, tools: []namedCmd{{"sed -i", sedEdit}}},
		{op: "search " + strconv.Quote(coreutilsQuery), lino: func() error { return runOK(ctx, e, "", "search", coreutilsQuery) },
			tools: search, capped: true},
	}
	note := fmt.Sprintf("%s, %d lines", path, lines)
	var ms []harness.Metric
	for _, p := range pairs {
		ts, err := e.Time(p.lino)
		if err != nil {
			return ms, fmt.Errorf("%s: %w", p.op, err)
		}
		linoP50 := harness.Ms(ts.Percentile(50))
		ms = append(ms, pctMetric(p.op+": lino", ts, note))
		for _, t := range p.tools {
			warm, n := e.Warmup, e.Iters
			if p.capped {
				warm, n = 1, min(e.Iters, baselineIters)
			}
			tt, err := timeCmds(warm, n, t.cmd)
			if err != nil {
				return ms, fmt.Errorf("%s: %w", p.op, err)
			}
			ms = append(ms, pctMetric(p.op+": "+t.name, tt, note))
			if base := harness.Ms(tt.Percentile(50)); base > 0 {
				ms = append(ms, harness.Metric{Name: p.op + ": lino / " + t.name, Value: linoP50 / base, Unit: "x",
					Note: "p50 ratio; < 1 means lino is faster"})
			}
		}
	}
	return append(ms, ed.failures()), nil
}

// pctMetric reports p50 with p95 in the note.
func pctMetric(name string, ts harness.Timings, note string) harness.Metric {
	return harness.Metric{Name: name + " p50", Value: harness.Ms(ts.Percentile(50)), Unit: "ms",
		Note: fmt.Sprintf("p95 %.2f ms, n=%d; %s", harness.Ms(ts.Percentile(95)), len(ts), note)}
}

// timeCmds times a fresh command per iteration; exit 1 (no match) is fine.
func timeCmds(warm, n int, mk func(i int) *exec.Cmd) (harness.Timings, error) {
	i := 0
	return harness.Measure(warm, n, func() error {
		i++
		c := mk(i)
		out, err := c.CombinedOutput()
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %v: %.200s", c.String(), err, out)
		}
		return nil
	})
}

// gnuSed reports whether sed on PATH is GNU sed (BSD sed has no --version).
func gnuSed() bool {
	return exec.Command("sed", "--version").Run() == nil
}

// sedInPlace returns sed arguments editing file in place with expr, for GNU
// or BSD sed.
func sedInPlace(gnu bool, expr, file string) []string {
	if gnu {
		return []string{"-i", "-e", expr, file}
	}
	return []string{"-i", "", "-e", expr, file}
}
