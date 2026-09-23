package harness

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestPercentile(t *testing.T) {
	ts := Timings{5, 1, 4, 2, 3, 10, 9, 8, 7, 6}
	for _, tc := range []struct {
		p    float64
		want time.Duration
	}{{50, 5}, {95, 10}, {90, 9}, {10, 1}, {100, 10}, {1, 1}} {
		if got := ts.Percentile(tc.p); got != tc.want {
			t.Errorf("p%v = %v, want %v", tc.p, got, tc.want)
		}
	}
	if got := (Timings{}).Percentile(50); got != 0 {
		t.Errorf("empty = %v", got)
	}
}

func TestTargetMet(t *testing.T) {
	for _, tc := range []struct {
		op   string
		v    float64
		want bool
	}{{"<", 9, true}, {"<", 10, false}, {"<=", 10, true}, {">", 10, false}, {">=", 10, true}, {"?", 1, false}} {
		if got := (Target{Op: tc.op, Value: 10}).Met(tc.v); got != tc.want {
			t.Errorf("%v %s 10 = %v", tc.v, tc.op, got)
		}
	}
}

var corpusFiles = map[string]string{
	"a.go":              "package a\n\nfunc A() {}\n",
	"b/b.go":            "package b\n",
	"b/c.txt":           "one\ntwo", // no final newline: still 2 lines
	"b/testdata/x.go":   "skipped\n",
	".git/config":       "skipped\n",
	"b/.hidden":         "skipped\n",
	"bin.dat":           "a\x00b\n",
	"z/last.go":         "package z\n",
	"b/testdata.go":     "package b // a file named like the dir is kept\n",
	"nested/deep/d.txt": "d\n",
}

func writeTree(t *testing.T, dir string) {
	t.Helper()
	for name, body := range corpusFiles {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func writeTar(t *testing.T, name, top string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(tw.WriteHeader(&tar.Header{Name: top + "/", Typeflag: tar.TypeDir, Mode: 0o755}))
	must(tw.WriteHeader(&tar.Header{Name: top + "/README", Typeflag: tar.TypeReg, Mode: 0o644, Size: 3}))
	_, err := tw.Write([]byte("hi\n"))
	must(err)
	for name, body := range corpusFiles {
		must(tw.WriteHeader(&tar.Header{Name: top + "/src/" + name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}))
		_, err := tw.Write([]byte(body))
		must(err)
	}
	must(tw.Close())
	must(gz.Close())
	must(os.WriteFile(name, buf.Bytes(), 0o644))
}

func listTree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

func TestCopyCorpus(t *testing.T) {
	src := t.TempDir()
	writeTree(t, filepath.Join(src, "src"))
	archive := filepath.Join(t.TempDir(), "repo.tar.gz")
	writeTar(t, archive, "repo-abc123")
	if err := os.WriteFile(archive+".pin", []byte("owner/repo@abc123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	all := "a.go b/b.go b/c.txt b/testdata.go nested/deep/d.txt z/last.go"

	for _, tc := range []struct {
		name, src, sub string
		max            int
		files, lines   int
		list, pin      string
	}{
		{"dir", src, "src", 0, 6, 9, all, ""},
		{"tar", archive, "src", 0, 6, 9, all, "owner/repo@abc123"},
		{"dir capped", src, "src", 4, 2, 4, "a.go b/b.go", ""},
		{"tar capped", archive, "src", 4, 2, 4, "a.go b/b.go", "owner/repo@abc123"},
		{"tar whole", archive, "", 0, 7, 10, "README src/a.go src/b/b.go src/b/c.txt src/b/testdata.go src/nested/deep/d.txt src/z/last.go", "owner/repo@abc123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst := t.TempDir()
			c, err := CopyCorpus(tc.src, tc.sub, dst, tc.max)
			if err != nil {
				t.Fatal(err)
			}
			if c.Files != tc.files || c.Lines != tc.lines || c.Pin != tc.pin {
				t.Errorf("got files=%d lines=%d pin=%q, want %d %d %q", c.Files, c.Lines, c.Pin, tc.files, tc.lines, tc.pin)
			}
			if got := strings.Join(listTree(t, dst), " "); got != tc.list {
				t.Errorf("copied %q, want %q", got, tc.list)
			}
		})
	}

	d1, err := CopyCorpus(src, "src", t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := CopyCorpus(archive, "src", t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if d1.Digest != d2.Digest || d1.Bytes != d2.Bytes {
		t.Errorf("dir and tar differ: %s/%d vs %s/%d", d1.Digest, d1.Bytes, d2.Digest, d2.Bytes)
	}
}

func TestMarkdown(t *testing.T) {
	r := &Report{
		Started: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Machine: Machine{OS: "linux", Arch: "amd64", CPUs: 4, GoVersion: "go1.24"},
		Lino:    Lino{Version: "lino dev"},
		Corpus:  Corpus{Pin: "golang/go@abc", Files: 2, Lines: 1500, Bytes: 2_500_000, Digest: "0123456789abcdef"},
	}
	r.add(Metric{Scenario: "s", Name: "fast", Value: 3.14159, Unit: "ms", Target: Under(10)})
	r.add(Metric{Scenario: "s", Name: "slow", Value: 12.5, Unit: "ms", Target: Under(10), Note: "a|b"})
	r.add(Metric{Scenario: "s", Name: "size", Value: 250, Unit: "MB"})
	r.Errors = []ScenarioError{{Scenario: "x", Error: "boom"}}
	var b strings.Builder
	if err := r.Markdown(&b); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"- Corpus: golang/go@abc: 2 files, 1500 lines, 2.5 MB, sha256 0123456789ab\n",
		"| s | fast | 3.14 ms | < 10.0 ms | pass |\n",
		`| s | slow (a\|b) | 12.5 ms | < 10.0 ms | FAIL |` + "\n",
		"| s | size | 250 MB |  |  |\n",
		"- x: boom\n",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("markdown lacks %q:\n%s", want, b.String())
		}
	}
}

// TestRun drives the whole harness with the real binary on a tiny corpus.
func TestRun(t *testing.T) {
	if testing.Short() {
		t.Skip("builds lino and starts a live process")
	}
	src := t.TempDir()
	writeTree(t, src)
	defer func(s []Scenario) { scenarios = s }(scenarios)
	scenarios = nil
	Register(Scenario{Name: "probe", Run: func(ctx context.Context, e *Env) ([]Metric, error) {
		ts, err := e.Time(func() error {
			_, err := e.JSON(ctx, nil, "search", "package")
			return err
		})
		return []Metric{{Name: "search p95", Value: Ms(ts.Percentile(95)), Unit: "ms", Target: Under(1e6)}}, err
	}})
	Register(Scenario{Name: "skipped", Run: func(context.Context, *Env) ([]Metric, error) {
		t.Error("filtered scenario ran")
		return nil, nil
	}})

	rep, err := Run(context.Background(), Config{Corpus: src, Match: regexp.MustCompile("^probe$"), Iters: 3, Warmup: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) > 0 {
		t.Fatalf("errors: %+v", rep.Errors)
	}
	if len(rep.Metrics) != 2 || rep.Metrics[1].Scenario != "probe" || rep.Metrics[1].Pass == nil || !*rep.Metrics[1].Pass {
		t.Fatalf("metrics: %+v", rep.Metrics)
	}
	if rep.Stats == nil || rep.Stats.Index.Files == 0 {
		t.Fatalf("stats: %+v", rep.Stats)
	}
	out := t.TempDir()
	if err := rep.Write(out); err != nil {
		t.Fatal(err)
	}
	var back Report
	b, err := os.ReadFile(filepath.Join(out, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &back); err != nil || len(back.Metrics) != 2 {
		t.Fatalf("results.json: %v %+v", err, back.Metrics)
	}
	if md, err := os.ReadFile(filepath.Join(out, "results.md")); err != nil || !strings.Contains(string(md), "| probe | search p95") {
		t.Fatalf("results.md: %v\n%s", err, md)
	}
}
