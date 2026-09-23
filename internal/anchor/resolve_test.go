package anchor

import (
	"fmt"
	"slices"
	"testing"

	"github.com/astralyx/lino/internal/outcome"
)

// file returns n distinct lines "line 1".."line n".
func file(n int) []string {
	l := make([]string, n)
	for i := range l {
		l[i] = fmt.Sprintf("line %d", i+1)
	}
	return l
}

func insertAt(lines []string, at int, add ...string) []string {
	return slices.Insert(slices.Clone(lines), at, add...)
}

func TestResolveRange(t *testing.T) {
	orig := file(200)
	a := func(n int) Anchor { return Of(n, orig[n-1]) }
	plain := func(n int) Anchor { return Anchor{Line: n} }

	pad := func(k int) []string {
		s := make([]string, k)
		for i := range s {
			s[i] = fmt.Sprintf("new %d", i)
		}
		return s
	}
	shiftedDown := insertAt(orig, 10, pad(3)...)         // lines after 10 move +3
	shiftedUp := slices.Delete(slices.Clone(orig), 5, 9) // lines after 9 move -4
	far := insertAt(orig, 0, pad(51)...)                 // beyond the window
	edited := slices.Clone(orig)
	edited[99] = "changed"
	// start line duplicated nearby: pairing with the end still picks one offset
	dupStart := insertAt(orig, 20, "line 30", "x")

	braces := []string{"a {", "}", "b {", "}", "c {", "}"}
	bracesShift := insertAt(braces, 0, "top")

	blanks := []string{"x", "", "y", "", "z"}
	blanksShift := insertAt(blanks, 0, "", "w")

	tests := []struct {
		name       string
		lines      []string
		start, end Anchor
		want       Resolved
		err        outcome.Outcome
	}{
		{"exact", orig, a(50), a(55), Resolved{Start: 50, End: 55}, outcome.OK},
		{"exact single", orig, a(1), a(1), Resolved{Start: 1, End: 1}, outcome.OK},
		{"plain", edited, plain(99), plain(101), Resolved{Start: 99, End: 101}, outcome.OK},
		{"shifted down", shiftedDown, a(50), a(55), Resolved{Start: 53, End: 58, Offset: 3}, outcome.OK},
		{"shifted up", shiftedUp, a(50), a(55), Resolved{Start: 46, End: 51, Offset: -4}, outcome.OK},
		{"beyond window", far, a(50), a(50), Resolved{}, outcome.AnchorMismatch},
		{"at window edge", insertAt(orig, 0, pad(50)...), a(60), a(60), Resolved{Start: 110, End: 110, Offset: 50}, outcome.OK},
		{"changed line", edited, a(100), a(100), Resolved{}, outcome.AnchorMismatch},
		{"range with changed end", edited, a(95), a(100), Resolved{}, outcome.AnchorMismatch},
		{"different offsets", shiftedDown, a(8), a(12), Resolved{}, outcome.AnchorMismatch},
		{"plain start hashed end moved", shiftedDown, plain(50), a(55), Resolved{}, outcome.AnchorMismatch},
		{"pairing disambiguates", dupStart, a(30), a(31), Resolved{Start: 32, End: 33, Offset: 2}, outcome.OK},
		{"duplicate brace ambiguous", bracesShift, Of(4, "}"), Of(4, "}"), Resolved{}, outcome.AnchorMismatch},
		{"duplicate brace in place", braces, Of(4, "}"), Of(4, "}"), Resolved{Start: 4, End: 4}, outcome.OK},
		{"duplicate brace range unique", bracesShift, Of(3, "b {"), Of(4, "}"), Resolved{Start: 4, End: 5, Offset: 1}, outcome.OK},
		{"blank ambiguous", blanksShift, Of(2, ""), Of(2, ""), Resolved{}, outcome.AnchorMismatch},
		{"end before start", orig, a(10), a(9), Resolved{}, outcome.Usage},
		{"zero line", orig, plain(0), plain(1), Resolved{}, outcome.Usage},
		{"plain beyond EOF", orig, plain(199), plain(201), Resolved{}, outcome.AnchorMismatch},
		{"hashed beyond EOF", orig[:100], a(150), a(150), Resolved{}, outcome.AnchorMismatch},
		{"hashed beyond EOF relocates", orig[40:], a(150), a(150), Resolved{Start: 110, End: 110, Offset: -40}, outcome.OK},
		{"empty file", nil, a(1), a(1), Resolved{}, outcome.AnchorMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveRange(tt.lines, tt.start, tt.end)
			if o := outcome.Of(err); o != tt.err {
				t.Fatalf("outcome %s (%v), want %s", o, err, tt.err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestResolveSingle(t *testing.T) {
	orig := file(20)
	moved := insertAt(orig, 0, "a", "b")
	got, err := Resolve(moved, Of(7, orig[6]))
	if err != nil || got != (Resolved{Start: 9, End: 9, Offset: 2}) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestMismatchPayload(t *testing.T) {
	orig := file(100)
	edited := slices.Clone(orig)
	edited[49] = "changed"
	tests := []struct {
		name       string
		lines      []string
		start, end Anchor
		wantLines  []int
	}{
		{"single", edited, Of(50, orig[49]), Of(50, orig[49]), []int{48, 49, 50, 51, 52}},
		{"range", edited, Of(49, orig[48]), Of(50, orig[49]), []int{47, 48, 49, 50, 51, 52}},
		{"file start", slices.Concat([]string{"changed"}, orig[1:]), Of(1, "line 1"), Of(1, "line 1"), []int{1, 2, 3}},
		{"beyond EOF", orig[:10], Anchor{Line: 30}, Anchor{Line: 30}, []int{8, 9, 10}},
		{"capped", edited, Anchor{Line: 1}, Anchor{Line: 101}, seq(1, regionMax)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolveRange(tt.lines, tt.start, tt.end)
			e, ok := outcome.As(err)
			if !ok || e.Outcome != outcome.AnchorMismatch {
				t.Fatalf("err = %v", err)
			}
			var ns []int
			for _, l := range e.Lines {
				ns = append(ns, l.N)
				if l.Text != tt.lines[l.N-1] || l.Anchor != Hash(l.Text) {
					t.Fatalf("line %d payload %+v", l.N, l)
				}
			}
			if !slices.Equal(ns, tt.wantLines) {
				t.Fatalf("lines %v, want %v", ns, tt.wantLines)
			}
		})
	}
}

func seq(a, b int) []int {
	var s []int
	for i := a; i <= b; i++ {
		s = append(s, i)
	}
	return s
}
