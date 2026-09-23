package tokens

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Report is a token benchmark run.
type Report struct {
	Agent   string      `json:"agent"`
	Model   string      `json:"model,omitempty"`
	Pin     string      `json:"pin"`
	Dry     bool        `json:"dry,omitempty"` // scripted agent: validates the harness, tokens are meaningless
	Arms    []string    `json:"arms"`
	Results []RunResult `json:"results"`
}

// ArmSummary aggregates one arm over all tasks and repetitions.
type ArmSummary struct {
	Arm          string  `json:"arm"`
	Runs         int     `json:"runs"`
	Succeeded    int     `json:"succeeded"`
	Tokens       int64   `json:"tokens"` // total over all runs
	Output       int64   `json:"output_tokens"`
	Commands     int     `json:"commands"`
	Retries      int     `json:"retries"`
	Ops          Ops     `json:"ops"`
	FallbackRate float64 `json:"fallback_rate"`
	CostUSD      float64 `json:"cost_usd"`
}

// Summaries aggregates the results per arm, in arm order.
func (r *Report) Summaries() []ArmSummary {
	var out []ArmSummary
	for _, arm := range r.Arms {
		s := ArmSummary{Arm: arm}
		for _, res := range r.Results {
			if res.Arm != arm {
				continue
			}
			s.Runs++
			if res.OK {
				s.Succeeded++
			}
			t := res.Transcript
			s.Tokens += t.Usage.Total()
			s.Output += t.Usage.Output
			s.Commands += t.Ops.Commands
			s.Retries += t.Ops.Retries
			s.Ops.add(t.Ops)
			s.CostUSD += t.CostUSD
		}
		s.FallbackRate = s.Ops.FallbackRate()
		out = append(out, s)
	}
	return out
}

// cell is the median run of one task in one arm.
type cell struct {
	tokens  int64
	ok, n   int
	retries int
	ops     Ops
}

func (r *Report) cells() (tasks []string, cells map[string]map[string]*cell) {
	cells = map[string]map[string]*cell{}
	toks := map[[2]string][]int64{}
	for _, res := range r.Results {
		if cells[res.Task] == nil {
			cells[res.Task] = map[string]*cell{}
			tasks = append(tasks, res.Task)
		}
		c := cells[res.Task][res.Arm]
		if c == nil {
			c = &cell{}
			cells[res.Task][res.Arm] = c
		}
		c.n++
		if res.OK {
			c.ok++
		}
		c.retries += res.Transcript.Ops.Retries
		c.ops.add(res.Transcript.Ops)
		k := [2]string{res.Task, res.Arm}
		toks[k] = append(toks[k], res.Transcript.Usage.Total())
	}
	for k, ts := range toks {
		sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
		cells[k[0]][k[1]].tokens = ts[len(ts)/2]
	}
	sort.Strings(tasks)
	return tasks, cells
}

// Markdown writes the per-task table and the per-arm summary.
func (r *Report) Markdown(w io.Writer) error {
	var b strings.Builder
	b.WriteString("# lino token benchmark\n\n")
	fmt.Fprintf(&b, "- Agent: %s", r.Agent)
	if r.Model != "" {
		fmt.Fprintf(&b, ", model %s", r.Model)
	}
	fmt.Fprintf(&b, "\n- Repository: %s (src/), task set bench/tokens\n", r.Pin)
	b.WriteString("- Tokens: median per task over repetitions; input + output + cache writes + cache reads\n")
	if r.Dry {
		b.WriteString("- **Dry run**: a scripted agent applied the reference solutions; token numbers are meaningless\n")
	}
	b.WriteString("\n## Per task\n\n| task |")
	for _, a := range r.Arms {
		fmt.Fprintf(&b, " %s tokens | %s ok |", a, a)
	}
	b.WriteString(" lino fallback | lino retries |\n|---|")
	b.WriteString(strings.Repeat("---:|---|", len(r.Arms)))
	b.WriteString("---:|---:|\n")
	tasks, cells := r.cells()
	for _, id := range tasks {
		title := id
		if t, ok := Lookup(id); ok {
			title += " " + t.Title
		}
		fmt.Fprintf(&b, "| %s |", title)
		for _, a := range r.Arms {
			if c := cells[id][a]; c != nil {
				fmt.Fprintf(&b, " %d | %d/%d |", c.tokens, c.ok, c.n)
			} else {
				b.WriteString(" | |")
			}
		}
		if c := cells[id]["lino"]; c != nil {
			fmt.Fprintf(&b, " %.0f%% | %d |\n", 100*c.ops.FallbackRate(), c.retries)
		} else {
			b.WriteString(" | |\n")
		}
	}
	b.WriteString("\n## Per arm\n\n| arm | runs | succeeded | tokens | output tokens | bash calls | lino ops | fallback ops | fallback rate | retries | cost USD |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, s := range r.Summaries() {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %d | %.1f%% | %d | %.2f |\n", s.Arm, s.Runs, s.Succeeded,
			s.Tokens, s.Output, s.Commands, s.Ops.Lino, s.Ops.Fallback, 100*s.FallbackRate, s.Retries, s.CostUSD)
	}
	b.WriteString("\nFallback rate: share of file operations (cat, head, tail, grep, rg, find, ls, sed, awk, ...) " +
		"done without lino; meaningful for the lino arm. Retries: lino calls answered conflict or anchor_mismatch.\n")
	var errs []string
	for _, res := range r.Results {
		if res.Error != "" {
			errs = append(errs, fmt.Sprintf("- %s %s #%d: %s", res.Task, res.Arm, res.Rep, res.Error))
		}
	}
	if len(errs) > 0 {
		b.WriteString("\n## Run errors\n\n" + strings.Join(errs, "\n") + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Write stores results.json and results.md in dir.
func (r *Report) Write(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(struct {
		*Report
		Summary []ArmSummary `json:"summary"`
	}{r, r.Summaries()}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "results.json"), b, 0o644); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, "results.md"))
	if err != nil {
		return err
	}
	defer f.Close()
	return r.Markdown(f)
}
