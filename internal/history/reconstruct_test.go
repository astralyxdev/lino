package history

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/linediff"
	"github.com/astralyx/lino/internal/textfile"
	fver "github.com/astralyx/lino/internal/version"
)

// sim applies changes to an in-memory tree and records them in history the
// way histrec does.
type sim struct {
	tb    testing.TB
	st    *Store
	files map[string][]byte
}

func newSim(tb testing.TB) *sim {
	return &sim{tb: tb, st: openTemp(tb), files: map[string][]byte{}}
}

func openTemp(tb testing.TB) *Store {
	st, err := OpenPath(context.Background(), tb.TempDir()+"/history.db")
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { st.Close() })
	return st
}

func vOf(b []byte, ok bool) string {
	if !ok {
		return ""
	}
	return fver.Of(b)
}

func isBinary(b []byte) bool { return textfile.Classify(b) == textfile.Binary }

func (s *sim) add(c Change) {
	if _, err := s.st.Add(context.Background(), c); err != nil {
		s.tb.Fatal(err)
	}
}

func (s *sim) set(path, content, op string) {
	old, existed := s.files[path]
	nb := []byte(content)
	c := Change{Source: SourceLino, Path: path, Op: op, VBefore: vOf(old, existed), VAfter: fver.Of(nb)}
	c.Extra.Created = !existed
	switch {
	case existed && isBinary(old), isBinary(nb):
		c.Extra.Binary = true
	case existed:
		c.Fragments = linediff.Diff(textfile.Parse(old).Lines, textfile.Parse(nb).Lines)
	default:
		c.Fragments = linediff.Diff(nil, textfile.Parse(nb).Lines)
	}
	s.add(c)
	s.files[path] = nb
}

func (s *sim) rm(path string) {
	old := s.files[path]
	d := textfile.Parse(old)
	c := Change{Source: SourceLino, Path: path, Op: OpRm, VBefore: fver.Of(old)}
	c.Extra = Extra{Removed: true, CRLF: d.Format.EOL == textfile.CRLF, BOM: d.Format.BOM,
		NoFinalNewline: len(old) > 0 && !d.Format.FinalNewline}
	c.Fragments = linediff.Diff(d.Lines, nil)
	s.add(c)
	delete(s.files, path)
}

func (s *sim) mv(from, to string) {
	b := s.files[from]
	s.add(Change{Source: SourceLino, Path: to, Op: OpMv, VBefore: fver.Of(b), VAfter: fver.Of(b), Extra: Extra{From: from}})
	delete(s.files, from)
	s.files[to] = b
}

func (s *sim) current(_ context.Context, path string) ([]byte, bool, error) {
	b, ok := s.files[path]
	if ok && isBinary(b) {
		return nil, false, ErrNotFound
	}
	return b, ok, nil
}

func TestReconstruct(t *testing.T) {
	s := newSim(t)
	s.set("a.txt", "l1\nl2\nl3\n", OpWrite)
	s.set("a.txt", "l1\nL2\nl3\n", OpEdit)
	s.set("a.txt", "l0\nl1\nL2\nl3\n", OpInsert)
	s.set("a.txt", "l0\nl1\nL2\nl3x", OpExternal) // also drops the final newline
	s.mv("a.txt", "b.txt")
	s.set("b.txt", "l0\nL2\nl3x\nl4", OpDelete)
	s.rm("b.txt")
	s.set("b.txt", "fresh\r\n", OpWrite)
	s.set("b.txt", "fresh\r\nmore\r\n", OpEdit)

	s.set("c.txt", "\xef\xbb\xbfx\r\ny\r\n", OpWrite)
	s.rm("c.txt")
	s.set("c.txt", "z\n", OpExternal)

	s.set("d.txt", "a\nb\n", OpWrite)
	s.set("d.txt", "a\nb", OpEdit) // no fragments
	s.set("d.txt", "a\r\nb", OpExternal)
	s.set("d.txt", "a\r\nB\r\nc\r\n", OpEdit)

	s.set("e.txt", "t\n", OpWrite)
	s.set("e.txt", "bin\x00", OpExternal)
	s.set("e.txt", "u\n", OpExternal)
	s.set("e.txt", "u\nw\n", OpEdit)

	s.set("f.txt", "mixed\r\nlf\nend\r\n", OpWrite)
	s.set("f.txt", "mixed\r\nlf\nmid\nend\r\n", OpInsert)

	s.set("g.txt", "g\n", OpWrite)
	s.mv("g.txt", "h.txt")
	s.set("h.txt", "g\nh\n", OpEdit)
	s.mv("h.txt", "i.txt")
	s.set("i.txt", "g\nh\ni\n", OpEdit)

	tests := []struct {
		query, content string
		path           string // where the version was; "" = query
		found          bool
	}{
		{"b.txt", "fresh\r\nmore\r\n", "", true},
		{"b.txt", "fresh\r\n", "", true},
		{"b.txt", "l0\nL2\nl3x\nl4", "", true},
		{"b.txt", "l0\nl1\nL2\nl3x", "", true},
		{"b.txt", "l0\nl1\nL2\nl3\n", "a.txt", true},
		{"b.txt", "l1\nL2\nl3\n", "a.txt", true},
		{"b.txt", "l1\nl2\nl3\n", "a.txt", true},
		{"a.txt", "l0\nl1\nL2\nl3x", "", true},
		{"a.txt", "l1\nl2\nl3\n", "", true},
		{"a.txt", "fresh\r\n", "", false},
		{"b.txt", "never\n", "", false},
		{"c.txt", "z\n", "", true},
		{"c.txt", "\xef\xbb\xbfx\r\ny\r\n", "", true},
		{"d.txt", "a\r\nb", "", true},
		{"d.txt", "a\nb", "", true},
		{"d.txt", "a\nb\n", "", true},
		{"e.txt", "u\n", "", true},
		{"e.txt", "t\n", "", false},
		{"f.txt", "mixed\r\nlf\nend\r\n", "", true},
		{"i.txt", "g\nh\n", "", true},
		{"i.txt", "g\n", "h.txt", true},
		{"g.txt", "g\n", "", true},
		{"g.txt", "g\nh\n", "", false},
		{"h.txt", "g\nh\n", "", true},
		{"nope.txt", "x\n", "", false},
	}
	ctx := context.Background()
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s@%q", tt.query, tt.content), func(t *testing.T) {
			v := fver.Of([]byte(tt.content))
			got, err := s.st.Reconstruct(ctx, s.current, tt.query, v)
			if !tt.found {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("got %q, %v; want not found", got.Content, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := tt.path
			if want == "" {
				want = tt.query
			}
			if string(got.Content) != tt.content || got.Path != want {
				t.Fatalf("got %s %q, want %s %q", got.Path, got.Content, want, tt.content)
			}
			if !bytes.Equal(got.Doc.Bytes(), got.Content) {
				t.Fatalf("doc renders %q", got.Doc.Bytes())
			}
		})
	}
}

func TestReconstructNext(t *testing.T) {
	s := newSim(t)
	s.set("a", "1\n", OpWrite)
	s.set("a", "2\n", OpEdit)
	s.set("a", "3\n", OpEdit)
	ctx := context.Background()
	for _, tt := range []struct {
		content string
		next    int64
	}{{"3\n", 0}, {"2\n", 3}, {"1\n", 2}} {
		got, err := s.st.Reconstruct(ctx, s.current, "a", fver.Of([]byte(tt.content)))
		if err != nil || got.Next != tt.next {
			t.Fatalf("%q: next %d, %v; want %d", tt.content, got.Next, err, tt.next)
		}
	}
}

func TestReconstructPruned(t *testing.T) {
	s := newSim(t)
	s.set("a", "1\n", OpWrite)
	s.set("a", "2\n", OpEdit)
	s.set("a", "3\n", OpEdit)
	ctx := context.Background()
	if _, err := s.st.SQL.ExecContext(ctx, `DELETE FROM changes WHERE id <= 2`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.Reconstruct(ctx, s.current, "a", fver.Of([]byte("1\n"))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pruned: %v", err)
	}
	if got, err := s.st.Reconstruct(ctx, s.current, "a", fver.Of([]byte("2\n"))); err != nil || string(got.Content) != "2\n" {
		t.Fatalf("retained: %q %v", got.Content, err)
	}
}

func TestReconstructBrokenChain(t *testing.T) {
	s := newSim(t)
	s.set("a", "1\n2\n", OpWrite)
	s.set("a", "1\nX\n", OpEdit)
	s.files["a"] = []byte("other\n") // index moved on without history
	_, err := s.st.Reconstruct(context.Background(), s.current, "a", fver.Of([]byte("1\n2\n")))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestReconstructManyPages(t *testing.T) {
	s := newSim(t)
	s.set("a", "0\n", OpWrite)
	for i := 1; i <= 3*pageSize; i++ {
		s.set("a", fmt.Sprintf("%d\n", i), OpEdit)
	}
	got, err := s.st.Reconstruct(context.Background(), s.current, "a", fver.Of([]byte("0\n")))
	if err != nil || string(got.Content) != "0\n" {
		t.Fatalf("%q %v", got.Content, err)
	}
}

// BenchmarkReconstruct100 reconstructs a 2,000-line file 100 changes back.
// Target: under 50 ms per op.
func BenchmarkReconstruct100(b *testing.B) {
	s := newSim(b)
	lines := make([]string, 2000)
	for i := range lines {
		lines[i] = fmt.Sprintf("\tline %d: some typical source text, err := call(ctx, x)", i)
	}
	join := func() string { return strings.Join(lines, "\n") + "\n" }
	s.set("f.go", join(), OpWrite)
	first := fver.Of([]byte(join()))
	for i := 0; i < 100; i++ {
		at := (i * 37) % len(lines)
		lines[at] = fmt.Sprintf("\tedited %d", i)
		if i%3 == 0 {
			lines = append(lines[:at+1], append([]string{"\tinserted"}, lines[at+1:]...)...)
		}
		s.set("f.go", join(), OpEdit)
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		got, err := s.st.Reconstruct(ctx, s.current, "f.go", first)
		if err != nil || fver.Of(got.Content) != first {
			b.Fatal(err)
		}
		if d := time.Since(start); d > 50*time.Millisecond {
			b.Logf("slow: %v", d)
		}
	}
}
