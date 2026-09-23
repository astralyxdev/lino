// Package harness runs lino benchmark scenarios against a prepared corpus
// root and collects their metrics into a report.
//
// A scenario is a function over an Env: a lino binary, an initialised root
// with a live process, and a private HOME. Scenarios register themselves with
// Register (see bench/scenarios) and return Metrics, optionally with a target
// they pass or fail.
package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Env is what a scenario runs against.
type Env struct {
	Bin    string // lino binary
	Root   string // initialised corpus root; scenarios may modify it
	Home   string // private HOME for every lino call
	Corpus Corpus
	Iters  int       // timed iterations per measurement
	Warmup int       // untimed iterations before them
	Log    io.Writer // progress notes
}

// Result is one finished lino call.
type Result struct {
	Stdout, Stderr []byte
	Exit           int
	Dur            time.Duration
}

// Cmd returns an exec.Cmd for lino in the root with the private HOME.
func (e *Env) Cmd(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, e.Bin, args...)
	c.Dir = e.Root
	c.Env = append(os.Environ(), "HOME="+e.Home, "LINO_BY=bench")
	return c
}

// Run runs lino with stdin and times the whole process, start to exit. A
// non-zero exit is not an error; a failure to start is.
func (e *Env) Run(ctx context.Context, stdin string, args ...string) (Result, error) {
	c := e.Cmd(ctx, args...)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if stdin != "" {
		c.Stdin = strings.NewReader(stdin)
	}
	start := time.Now()
	err := c.Run()
	r := Result{Stdout: out.Bytes(), Stderr: errb.Bytes(), Dur: time.Since(start)}
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		r.Exit = ee.ExitCode()
	case err != nil:
		return r, fmt.Errorf("lino %s: %w", strings.Join(args, " "), err)
	}
	return r, nil
}

// Envelope is lino's --json wire form with the data left raw.
type Envelope struct {
	OK      bool            `json:"ok"`
	Outcome string          `json:"outcome"`
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
}

// JSON runs lino with --json, requires ok, and decodes data into v (if v is
// not nil).
func (e *Env) JSON(ctx context.Context, v any, args ...string) (Envelope, error) {
	return e.JSONIn(ctx, v, "", args...)
}

// JSONIn is JSON with stdin.
func (e *Env) JSONIn(ctx context.Context, v any, stdin string, args ...string) (Envelope, error) {
	r, err := e.Run(ctx, stdin, append(args, "--json")...)
	if err != nil {
		return Envelope{}, err
	}
	var env Envelope
	if err := json.Unmarshal(r.Stdout, &env); err != nil {
		return env, fmt.Errorf("lino %s: bad json (exit %d): %v: %s%s", strings.Join(args, " "), r.Exit, err, r.Stdout, r.Stderr)
	}
	if !env.OK {
		return env, fmt.Errorf("lino %s: %s: %s", strings.Join(args, " "), env.Outcome, env.Message)
	}
	if v != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, v); err != nil {
			return env, fmt.Errorf("lino %s: decode data: %w", strings.Join(args, " "), err)
		}
	}
	return env, nil
}

// Logf writes a progress note.
func (e *Env) Logf(format string, args ...any) {
	if e.Log != nil {
		fmt.Fprintf(e.Log, format+"\n", args...)
	}
}

// Time calls fn e.Warmup times untimed, then e.Iters times timed.
func (e *Env) Time(fn func() error) (Timings, error) {
	return Measure(e.Warmup, e.Iters, fn)
}

// Measure calls fn warm times untimed, then n times timed.
func Measure(warm, n int, fn func() error) (Timings, error) {
	for range warm {
		if err := fn(); err != nil {
			return nil, err
		}
	}
	ts := make(Timings, 0, n)
	for range n {
		start := time.Now()
		if err := fn(); err != nil {
			return ts, err
		}
		ts = append(ts, time.Since(start))
	}
	return ts, nil
}

// Timings is a set of measured durations.
type Timings []time.Duration

// Percentile returns the nearest-rank p-th percentile (0 < p <= 100).
func (ts Timings) Percentile(p float64) time.Duration {
	if len(ts) == 0 {
		return 0
	}
	s := append(Timings(nil), ts...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	rank := int(math.Ceil(p / 100 * float64(len(s))))
	return s[min(max(rank, 1), len(s))-1]
}

// Max returns the longest duration.
func (ts Timings) Max() time.Duration { return ts.Percentile(100) }

// Ms converts a duration to fractional milliseconds.
func Ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// Target is a bound a metric must stay under (Op "<", "<=") or over (">", ">=").
type Target struct {
	Op    string  `json:"op"`
	Value float64 `json:"value"`
}

// Met reports whether v satisfies t.
func (t Target) Met(v float64) bool {
	switch t.Op {
	case "<":
		return v < t.Value
	case "<=":
		return v <= t.Value
	case ">":
		return v > t.Value
	case ">=":
		return v >= t.Value
	}
	return false
}

// Under is the common "< v" target.
func Under(v float64) *Target { return &Target{Op: "<", Value: v} }

// Metric is one measured value.
type Metric struct {
	Scenario string  `json:"scenario"`
	Name     string  `json:"name"`
	Value    float64 `json:"value"`
	Unit     string  `json:"unit"`
	Target   *Target `json:"target,omitempty"`
	Pass     *bool   `json:"pass,omitempty"`
	Note     string  `json:"note,omitempty"`
}

// Judge sets Pass from Target.
func (m *Metric) Judge() {
	if m.Target == nil {
		m.Pass = nil
		return
	}
	ok := m.Target.Met(m.Value)
	m.Pass = &ok
}

// Scenario is one named benchmark.
type Scenario struct {
	Name string
	Doc  string
	Run  func(ctx context.Context, e *Env) ([]Metric, error)
}

var scenarios []Scenario

// Register adds s to the scenario list; names must be unique.
func Register(s Scenario) {
	for _, o := range scenarios {
		if o.Name == s.Name {
			panic("harness: duplicate scenario " + s.Name)
		}
	}
	scenarios = append(scenarios, s)
}

// Scenarios returns the registered scenarios in registration order.
func Scenarios() []Scenario { return append([]Scenario(nil), scenarios...) }
