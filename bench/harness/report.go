package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/astralyx/lino/internal/statscmd"
)

// Report is everything one harness run produced.
type Report struct {
	Started  time.Time       `json:"started"`
	Finished time.Time       `json:"finished"`
	Machine  Machine         `json:"machine"`
	Lino     Lino            `json:"lino"`
	Corpus   Corpus          `json:"corpus"`
	Metrics  []Metric        `json:"metrics"`
	Errors   []ScenarioError `json:"errors,omitempty"`
	Stats    *statscmd.Data  `json:"stats,omitempty"` // lino stats --json after the last scenario
}

// Machine is the host the run happened on.
type Machine struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	CPU       string `json:"cpu,omitempty"`
	CPUs      int    `json:"cpus"`
	GoVersion string `json:"go_version"`
}

// Lino identifies the binary under test.
type Lino struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
}

// ScenarioError is a scenario that did not finish.
type ScenarioError struct {
	Scenario string `json:"scenario"`
	Error    string `json:"error"`
}

func (r *Report) add(m Metric) {
	m.Judge()
	r.Metrics = append(r.Metrics, m)
}

// Write stores results.json and results.md in dir.
func (r *Report) Write(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "results.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, "results.md"))
	if err != nil {
		return err
	}
	if err := r.Markdown(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Markdown writes the run header and the results table.
func (r *Report) Markdown(w io.Writer) error {
	var b strings.Builder
	b.WriteString("# lino benchmark results\n\n")
	fmt.Fprintf(&b, "- Date: %s\n", r.Started.Format(time.RFC3339))
	cpu := r.Machine.CPU
	if cpu == "" {
		cpu = "unknown CPU"
	}
	fmt.Fprintf(&b, "- Machine: %s, %d CPUs, %s/%s, %s\n", cpu, r.Machine.CPUs, r.Machine.OS, r.Machine.Arch, r.Machine.GoVersion)
	lino := r.Lino.Version
	if r.Lino.Commit != "" {
		lino += ", tree " + r.Lino.Commit
	}
	fmt.Fprintf(&b, "- lino: %s\n", lino)
	src := r.Corpus.Pin
	if src == "" {
		src = r.Corpus.Source
	}
	fmt.Fprintf(&b, "- Corpus: %s: %d files, %d lines, %.1f MB, sha256 %.12s\n\n",
		src, r.Corpus.Files, r.Corpus.Lines, float64(r.Corpus.Bytes)/1e6, r.Corpus.Digest)

	b.WriteString("| scenario | metric | value | target | result |\n|---|---|---|---|---|\n")
	for _, m := range r.Metrics {
		target, result := "", ""
		if m.Target != nil {
			target = m.Target.Op + " " + num(m.Target.Value) + " " + m.Unit
			result = "FAIL"
			if m.Pass != nil && *m.Pass {
				result = "pass"
			}
		}
		metric := m.Name
		if m.Note != "" {
			metric += " (" + m.Note + ")"
		}
		fmt.Fprintf(&b, "| %s | %s | %s %s | %s | %s |\n", m.Scenario, cell(metric), num(m.Value), m.Unit, target, result)
	}
	if len(r.Errors) > 0 {
		b.WriteString("\n## Errors\n\n")
		for _, e := range r.Errors {
			fmt.Fprintf(&b, "- %s: %s\n", e.Scenario, cell(e.Error))
		}
	}
	if s := r.Stats; s != nil {
		b.WriteString("\n## lino stats after the run\n\n")
		fmt.Fprintf(&b, "- Index: %d files, %d lines, index.db %.1f MB\n", s.Index.Files, s.Index.Lines, float64(s.Index.DBBytes)/1e6)
		fmt.Fprintf(&b, "- History: %d changes, history.db %.1f MB\n", s.History.Changes, float64(s.History.DBBytes)/1e6)
		if p := s.Process; p != nil {
			fmt.Fprintf(&b, "- Live process: peak RSS %.1f MB, heap %.1f MB\n", float64(p.PeakBytes)/1e6, float64(p.HeapBytes)/1e6)
		}
		if len(s.Commands) > 0 {
			b.WriteString("\n| command | calls | p50 | p95 | max |\n|---|---|---|---|---|\n")
			for _, c := range s.Commands {
				fmt.Fprintf(&b, "| %s | %d | %s ms | %s ms | %s ms |\n", c.Name, c.Count,
					num(float64(c.P50)/1e3), num(float64(c.P95)/1e3), num(float64(c.Max)/1e3))
			}
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// num formats with up to three significant decimals and no trailing zeros.
func num(v float64) string {
	prec := 2
	switch a := abs(v); {
	case a >= 100:
		prec = 0
	case a >= 10:
		prec = 1
	case a < 1 && a > 0:
		prec = 3
	}
	return strconv.FormatFloat(v, 'f', prec, 64)
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func cell(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(s)
}
