package scenarios

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/astralyx/lino/bench/harness"
)

// TestLatencyScenarios runs every latency scenario once on a tiny corpus to
// check they work end to end; the numbers are meaningless here.
func TestLatencyScenarios(t *testing.T) {
	if testing.Short() {
		t.Skip("builds lino and starts a live process")
	}
	src := t.TempDir()
	for i := range 4 {
		var b strings.Builder
		fmt.Fprintf(&b, "package p%d\n\n", i)
		for j := range 1100 {
			fmt.Fprintf(&b, "var v%d = ReadFull // if err != nil { return err }\n", j)
		}
		if err := os.WriteFile(filepath.Join(src, fmt.Sprintf("f%d.go", i)), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := harness.Run(context.Background(), harness.Config{
		Corpus: src,
		Match:  regexp.MustCompile("^(search|edit-reindex|reconstruct|external-edit|direct-cold)$"),
		Iters:  2,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rep.Errors {
		t.Errorf("%s: %s", e.Scenario, e.Error)
	}
	targets := map[string]int{}
	for _, m := range rep.Metrics {
		if m.Target != nil {
			targets[m.Scenario]++
		}
	}
	for _, s := range []string{"search", "edit-reindex", "reconstruct", "external-edit", "direct-cold"} {
		if targets[s] == 0 {
			t.Errorf("scenario %s: no metric with a target", s)
		}
	}
}

func TestPickFile(t *testing.T) {
	root := t.TempDir()
	for name, n := range map[string]int{"a.go": 5, "b.go": 20, "c/d.go": 30, "e.txt": 50, ".lino/x.go": 99} {
		p := filepath.Join(root, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(strings.Repeat("x\n", n)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		min, nth int
		want     string
		lines    int
	}{{10, 0, "b.go", 20}, {10, 1, "c/d.go", 30}, {1, 0, "a.go", 5}, {25, 0, "c/d.go", 30}, {10, 2, "", 0}, {40, 0, "", 0}} {
		got, n, err := pickFile(root, tc.min, tc.nth)
		if got != tc.want || n != tc.lines || (err != nil) != (tc.want == "") {
			t.Errorf("pickFile(%d, %d) = %q, %d, %v; want %q, %d", tc.min, tc.nth, got, n, err, tc.want, tc.lines)
		}
	}
}
