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

// TestResourceScenarios runs the resource scenarios on a tiny corpus with a
// small history workload; the numbers are meaningless here.
func TestResourceScenarios(t *testing.T) {
	if testing.Short() {
		t.Skip("builds lino and starts a live process")
	}
	defer func(e, f int) { historyEdits, historyFiles = e, f }(historyEdits, historyFiles)
	historyEdits, historyFiles = 20, 4
	src := t.TempDir()
	for i := range 5 {
		var b strings.Builder
		fmt.Fprintf(&b, "package p%d\n\n", i)
		for j := range 300 {
			fmt.Fprintf(&b, "var v%d = %d\n", j, j)
		}
		if err := os.WriteFile(filepath.Join(src, fmt.Sprintf("f%d.go", i)), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := harness.Run(context.Background(), harness.Config{
		Corpus: src,
		Match:  regexp.MustCompile("^(index-size|idle-rss|history-disk)$"),
		Iters:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rep.Errors {
		t.Errorf("%s: %s", e.Scenario, e.Error)
	}
	got := map[string]harness.Metric{}
	for _, m := range rep.Metrics {
		got[m.Scenario+"/"+m.Name] = m
	}
	for _, name := range []string{"setup/init wall time", "index-size/index / source", "idle-rss/idle RSS",
		"history-disk/history growth per 1,000 edits", "history-disk/edit internal errors"} {
		m, ok := got[name]
		if !ok {
			t.Errorf("no metric %s", name)
			continue
		}
		if m.Value < 0 || (m.Target == nil) != (name != "setup/init wall time" && name != "idle-rss/idle RSS" && name != "history-disk/edit internal errors") {
			t.Errorf("%s: %+v", name, m)
		}
	}
	if m := got["history-disk/history growth per 1,000 edits"]; !strings.HasPrefix(m.Note, "20 edits in 4 files") {
		t.Errorf("history note %q", m.Note)
	}
}

func TestSpreadFiles(t *testing.T) {
	root := t.TempDir()
	for i := range 10 {
		n := 50
		if i%2 == 0 {
			n = 250
		}
		p := filepath.Join(root, fmt.Sprintf("d%d", i), "f.go")
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(strings.Repeat("x\n", n)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := spreadFiles(root, 3, 200)
	if err != nil || strings.Join(got, " ") != "d0/f.go d2/f.go d6/f.go" {
		t.Errorf("spreadFiles = %v, %v", got, err)
	}
	if _, err := spreadFiles(root, 6, 200); err == nil {
		t.Error("want error for too few files")
	}
}
