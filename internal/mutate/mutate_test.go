package mutate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/anchor"
	"github.com/astralyx/lino/internal/fileio"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/paths"
	"github.com/astralyx/lino/internal/textfile"
	"github.com/astralyx/lino/internal/version"
)

// fakeOp replaces line 2 with "X" (or a fixed target) and records calls.
type fakeOp struct {
	target  Range
	applied bool
	err     error
}

func (o *fakeOp) Name() string { return "fake" }
func (o *fakeOp) Target([]string) (Range, error) {
	return o.target, o.err
}
func (o *fakeOp) Apply(d *textfile.Doc, t Range) (Range, error) {
	o.applied = true
	d.Replace(t.Start-1, t.Len(), []string{"X"})
	return Range{t.Start, t.Start}, nil
}

func setup(t *testing.T, content string, mode os.FileMode) (*Pipeline, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "f.txt"), mode); err != nil {
		t.Fatal(err)
	}
	root, err := paths.NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &Pipeline{Root: root}, filepath.Join(dir, "f.txt")
}

const base = "a\nb\nc\n"

func TestRunErrors(t *testing.T) {
	cur := version.Of([]byte(base))
	tests := []struct {
		name  string
		v     string
		op    *fakeOp
		want  outcome.Outcome
		lines bool
	}{
		{"missing v", "", &fakeOp{target: Range{2, 2}}, outcome.Conflict, false},
		{"malformed v", "xyz", &fakeOp{target: Range{2, 2}}, outcome.Usage, false},
		{"stale v", "000000", &fakeOp{target: Range{2, 2}}, outcome.Conflict, true},
		{"target error", cur, &fakeOp{err: outcome.New(outcome.AnchorMismatch, "moved").WithLines("", []outcome.Line{{N: 1, Text: "a"}})}, outcome.AnchorMismatch, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, file := setup(t, base, 0o644)
			hooked := false
			p.Hooks = []Hook{func(context.Context, *Commit) error { hooked = true; return nil }}
			_, err := p.Run(context.Background(), Request{Path: "f.txt", V: tt.v, Op: tt.op})
			if !outcome.Is(err, tt.want) {
				t.Fatalf("err = %v, want %s", err, tt.want)
			}
			e, _ := outcome.As(err)
			if tt.lines {
				if len(e.Lines) == 0 || e.Path != "f.txt" {
					t.Fatalf("want region with path, got %+v", e)
				}
			}
			if tt.op.applied || hooked {
				t.Fatal("op applied or hook ran on failure")
			}
			if b, _ := os.ReadFile(file); string(b) != base {
				t.Fatalf("file changed: %q", b)
			}
		})
	}
}

func TestStaleRegion(t *testing.T) {
	p, _ := setup(t, base, 0o644)
	_, err := p.Run(context.Background(), Request{Path: "f.txt", V: "000000", Op: &fakeOp{target: Range{2, 2}}})
	e, _ := outcome.As(err)
	var got []string
	for _, l := range e.Lines {
		got = append(got, l.Text)
		if l.Anchor != anchor.Hash(l.Text) {
			t.Fatalf("bad anchor in %+v", l)
		}
	}
	if strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("region = %v", got)
	}
	if !strings.Contains(e.Message, "v=000000") || !strings.Contains(e.Hint, version.Of([]byte(base))) {
		t.Fatalf("message %q hint %q", e.Message, e.Hint)
	}
}

func TestRunSuccess(t *testing.T) {
	p, file := setup(t, "a\r\nb\r\nc", 0o600)
	var commits []*Commit
	p.Hooks = []Hook{func(_ context.Context, c *Commit) error { commits = append(commits, c); return nil }}
	old := version.Of([]byte("a\r\nb\r\nc"))
	res, err := p.Run(context.Background(), Request{Path: "f.txt", V: old, By: "agent-1", Op: &fakeOp{target: Range{2, 2}}})
	if err != nil {
		t.Fatal(err)
	}
	want := "a\r\nX\r\nc"
	b, _ := os.ReadFile(file)
	if string(b) != want {
		t.Fatalf("content %q, want %q", b, want)
	}
	st, _ := os.Stat(file)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	if res.OldV != old || res.NewV != version.Of([]byte(want)) || res.Path != "f.txt" ||
		res.Op != "fake" || res.By != "agent-1" || res.Changed != (Range{2, 2}) || res.Total != 3 {
		t.Fatalf("result %+v", res)
	}
	if len(commits) != 1 || string(commits[0].Before) != "a\r\nb\r\nc" || string(commits[0].After) != want {
		t.Fatalf("hook commits %+v", commits)
	}
}

func TestHookErrorAndCustomCheck(t *testing.T) {
	p, file := setup(t, base, 0o644)
	var gotV string
	p.Check = func(_ context.Context, _ *fileio.File, v string, _ Op, _ Range) error { gotV = v; return nil }
	p.Hooks = []Hook{func(context.Context, *Commit) error { return errors.New("index down") }}
	res, err := p.Run(context.Background(), Request{Path: "f.txt", V: "000000", Op: &fakeOp{target: Range{1, 1}}})
	if err == nil || res == nil {
		t.Fatalf("want result and hook error, got %v %v", res, err)
	}
	if gotV != "000000" {
		t.Fatal("custom check not used")
	}
	if b, _ := os.ReadFile(file); string(b) != "X\nb\nc\n" {
		t.Fatalf("write should stay: %q", b)
	}
}

func TestUnchangedSkipsWrite(t *testing.T) {
	p, _ := setup(t, "X\n", 0o644)
	hooked := false
	p.Hooks = []Hook{func(context.Context, *Commit) error { hooked = true; return nil }}
	res, err := p.Run(context.Background(), Request{Path: "f.txt", V: version.Of([]byte("X\n")), Op: &fakeOp{target: Range{1, 1}}})
	if err != nil || !res.Unchanged || hooked {
		t.Fatalf("res %+v err %v hooked %v", res, err, hooked)
	}
}

func TestContent(t *testing.T) {
	tests := []struct {
		in   string
		want []string
		err  bool
	}{
		{"", nil, true},
		{"\n", []string{""}, false},
		{"x\ny\n", []string{"x", "y"}, false},
		{"x\r\ny", []string{"x", "y"}, false},
	}
	for _, tt := range tests {
		got, err := Content([]byte(tt.in))
		if tt.err {
			if !outcome.Is(err, outcome.Usage) {
				t.Fatalf("Content(%q) err %v", tt.in, err)
			}
			continue
		}
		if err != nil || strings.Join(got, "|") != strings.Join(tt.want, "|") || len(got) != len(tt.want) {
			t.Fatalf("Content(%q) = %q, %v", tt.in, got, err)
		}
	}
}

func a(n int, text string) anchor.Anchor { return anchor.Of(n, text) }

func TestOps(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		op      Op
		want    string
		changed Range
		fail    outcome.Outcome
	}{
		{"edit", "a\nb\nc\n", &Splice{Start: a(2, "b"), End: a(3, "c"), Lines: []string{"x", "y", "z"}}, "a\nx\ny\nz\n", Range{2, 4}, ""},
		{"delete", "a\nb\nc\n", &Splice{Start: a(1, "a"), End: a(2, "b")}, "c\n", Range{1, 0}, ""},
		{"delete all", "a\nb\n", &Splice{Start: a(1, "a"), End: a(2, "b")}, "", Range{1, 0}, ""},
		{"edit relocated", "new\na\nb\n", &Splice{Start: a(1, "a"), End: a(1, "a"), Lines: []string{"A"}}, "new\nA\nb\n", Range{2, 2}, ""},
		{"edit mismatch", "a\nb\n", &Splice{Start: a(1, "zz"), End: a(1, "zz"), Lines: []string{"A"}}, "", Range{}, outcome.AnchorMismatch},
		{"insert before", "a\nb\n", &Insert{Where: Before, At: a(2, "b"), Lines: []string{"x"}}, "a\nx\nb\n", Range{2, 2}, ""},
		{"insert after", "a\nb\n", &Insert{Where: After, At: a(2, "b"), Lines: []string{"x", "y"}}, "a\nb\nx\ny\n", Range{3, 4}, ""},
		{"insert at start", "a\n", &Insert{Where: AtStart, Lines: []string{"x"}}, "x\na\n", Range{1, 1}, ""},
		{"insert at end", "a\n", &Insert{Where: AtEnd, Lines: []string{"x"}}, "a\nx\n", Range{2, 2}, ""},
		{"insert at end no final newline", "a", &Insert{Where: AtEnd, Lines: []string{"x"}}, "a\nx", Range{2, 2}, ""},
		{"insert at end crlf", "a\r\n", &Insert{Where: AtEnd, Lines: []string{"x"}}, "a\r\nx\r\n", Range{2, 2}, ""},
		{"insert into empty", "", &Insert{Where: AtEnd, Lines: []string{"x"}}, "x\n", Range{1, 1}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, file := setup(t, tt.in, 0o644)
			res, err := p.Run(context.Background(), Request{Path: "f.txt", V: version.Of([]byte(tt.in)), Op: tt.op})
			if tt.fail != "" {
				if !outcome.Is(err, tt.fail) {
					t.Fatalf("err %v, want %s", err, tt.fail)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(file)
			if string(b) != tt.want {
				t.Fatalf("content %q, want %q", b, tt.want)
			}
			if res.Changed != tt.changed {
				t.Fatalf("changed %+v, want %+v", res.Changed, tt.changed)
			}
		})
	}
}
