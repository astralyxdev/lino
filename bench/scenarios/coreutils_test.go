package scenarios

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/astralyx/lino/bench/harness"
)

func TestSedInPlace(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(file, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sed", sedInPlace(gnuSed(), "2s/.*/x/", file)...).CombinedOutput()
	if err != nil {
		t.Fatalf("sed: %v: %s", err, out)
	}
	if b, _ := os.ReadFile(file); string(b) != "a\nx\nc\n" {
		t.Errorf("after sed -i: %q", b)
	}
	if ents, _ := os.ReadDir(filepath.Dir(file)); len(ents) != 1 {
		t.Errorf("sed -i left %d files, want 1 (no backup)", len(ents))
	}
}

func TestRipgrepEnv(t *testing.T) {
	for _, tc := range []struct{ bench, rg, want string }{
		{"/x/bench-rg", "/x/rg", "/x/bench-rg"},
		{"", "/x/rg", "/x/rg"},
	} {
		t.Setenv("LINO_BENCH_RG", tc.bench)
		t.Setenv("RG", tc.rg)
		mk := ripgrep()
		if mk == nil {
			t.Fatalf("ripgrep() = nil with LINO_BENCH_RG=%q RG=%q", tc.bench, tc.rg)
		}
		if c := mk(); c.Path != tc.want || c.Args[0] != "rg" {
			t.Errorf("LINO_BENCH_RG=%q RG=%q: path %q argv0 %q, want %q", tc.bench, tc.rg, c.Path, c.Args[0], tc.want)
		}
	}
}

// TestVsCoreutils runs the scenario once on a tiny corpus; the numbers are
// meaningless here.
func TestVsCoreutils(t *testing.T) {
	if testing.Short() {
		t.Skip("builds lino and starts a live process")
	}
	src := t.TempDir()
	for i := range 4 {
		var b strings.Builder
		fmt.Fprintf(&b, "package p%d\n\n", i)
		for j := range 1100 {
			fmt.Fprintf(&b, "var v%d = ReadFull\n", j)
		}
		if err := os.WriteFile(filepath.Join(src, fmt.Sprintf("f%d.go", i)), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := harness.Run(context.Background(), harness.Config{
		Corpus: src,
		Match:  regexp.MustCompile("^vs-coreutils$"),
		Iters:  2,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rep.Errors {
		t.Errorf("%s: %s", e.Scenario, e.Error)
	}
	var names []string
	for _, m := range rep.Metrics {
		if m.Scenario == "vs-coreutils" {
			names = append(names, m.Name)
		}
	}
	for _, want := range []string{
		"read whole file: lino p50", "read whole file: cat p50", "read whole file: lino / cat",
		"read 50 lines: sed -n p50", "read --anchors whole file: lino p50",
		"one-line edit: lino p50", "one-line edit: sed -i p50",
		`search "ReadFull": grep -rn p50`, "edit internal errors",
	} {
		if !slices.Contains(names, want) {
			t.Errorf("metric %q missing; have %q", want, names)
		}
	}
	if b, err := os.ReadFile(filepath.Join(src, "f3.go")); err != nil || strings.Contains(string(b), "sed bench") {
		t.Errorf("sed -i touched the corpus source: %v", err)
	}
}
