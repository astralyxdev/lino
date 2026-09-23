package statscmd

import (
	"testing"
	"time"

	"github.com/astralyx/lino/internal/metrics"
	"github.com/astralyx/lino/internal/outcome"
)

func TestUs(t *testing.T) {
	for _, c := range []struct {
		in   int64
		want string
	}{
		{0, "0µs"}, {850, "850µs"}, {1234, "1.2ms"}, {35_000, "35ms"}, {2_100_000, "2.1s"},
	} {
		if got := us(c.in); got != c.want {
			t.Errorf("us(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOutcomes(t *testing.T) {
	for _, c := range []struct {
		in   map[outcome.Outcome]uint64
		want string
	}{
		{nil, ""},
		{map[outcome.Outcome]uint64{outcome.OK: 3, outcome.Conflict: 1, outcome.Empty: 0}, "ok 3, conflict 1"},
		{map[outcome.Outcome]uint64{outcome.Updated: 2, outcome.AnchorMismatch: 2}, "anchor_mismatch 2, updated 2"},
	} {
		if got := outcomes(c.in); got != c.want {
			t.Errorf("outcomes(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFillMetricsRates(t *testing.T) {
	c := metrics.Load(t.TempDir() + "/stats.json")
	for _, o := range []struct {
		cmd string
		out outcome.Outcome
	}{
		{"edit", outcome.Updated}, {"edit", outcome.Conflict}, {"insert", outcome.AnchorMismatch},
		{"write", outcome.Created}, {"read", outcome.OK}, {"read", outcome.NotFound},
	} {
		c.Observe(o.cmd, o.out, time.Millisecond)
	}
	var d Data
	fillMetrics(&d, c.Snapshot())
	if d.Calls != 6 || len(d.Commands) != 4 {
		t.Fatalf("calls %d, commands %v", d.Calls, d.Commands)
	}
	want := map[outcome.Outcome][2]uint64{outcome.Conflict: {1, 4}, outcome.AnchorMismatch: {1, 4}}
	for _, r := range d.Rates {
		if w := want[r.Outcome]; r.Count != w[0] || r.Of != w[1] || r.Rate != 0.25 {
			t.Errorf("rate %+v, want %v", r, w)
		}
	}
}
