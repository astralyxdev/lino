package history

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/linediff"
	fver "github.com/astralyx/lino/internal/version"
)

func lines(ls ...string) string { return strings.Join(ls, "\n") + "\n" }

func TestMapRange(t *testing.T) {
	base := lines("a", "b", "c", "d", "e", "f")
	tests := []struct {
		name       string
		later      []string // successive contents of "f.txt" after base
		start, end int      // range in base
		wantS      int
		wantE      int
		overlaps   int
	}{
		{"no later changes", nil, 3, 4, 3, 4, 0},
		{"inserted above", []string{lines("x", "y", "a", "b", "c", "d", "e", "f")}, 3, 4, 5, 6, 0},
		{"deleted above", []string{lines("c", "d", "e", "f")}, 3, 4, 1, 2, 0},
		{"inserted below", []string{lines("a", "b", "c", "d", "x", "e", "f")}, 3, 4, 3, 4, 0},
		{"adjacent edit above", []string{lines("a", "B", "c", "d", "e", "f")}, 3, 4, 3, 4, 0},
		{"adjacent edit below", []string{lines("a", "b", "c", "d", "E", "f")}, 3, 4, 3, 4, 0},
		{"insert right before range", []string{lines("a", "b", "x", "c", "d", "e", "f")}, 3, 4, 4, 5, 0},
		{"overlapping edit", []string{lines("a", "b", "C", "d", "e", "f")}, 3, 4, 3, 4, 1},
		{"insert inside range", []string{lines("a", "b", "c", "x", "d", "e", "f")}, 3, 4, 3, 5, 1},
		{"edit straddling start grows range", []string{lines("a", "X", "Y", "Z", "d", "e", "f")}, 3, 4, 2, 5, 1},
		{"range deleted", []string{lines("a", "b", "e", "f")}, 3, 4, 3, 2, 1},
		{"chain of shifts", []string{
			lines("x", "a", "b", "c", "d", "e", "f"),
			lines("x", "a", "c", "d", "e", "f"),
			lines("x", "a", "c", "d", "e", "f", "z"),
			lines("x", "y", "a", "c", "d", "e", "f", "z"),
		}, 3, 4, 4, 5, 0},
		{"overlap later in chain", []string{
			lines("x", "a", "b", "c", "d", "e", "f"),
			lines("x", "a", "b", "c", "D", "e", "f"),
		}, 3, 4, 4, 5, 1},
		{"position shifted", []string{lines("x", "a", "b", "c", "d", "e", "f")}, 3, 2, 4, 3, 0},
		{"insert at position overlaps", []string{lines("a", "b", "x", "c", "d", "e", "f")}, 3, 2, 4, 3, 1},
		{"position after edited line", []string{lines("a", "B", "c", "d", "e", "f")}, 3, 2, 3, 2, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSim(t)
			s.set("f.txt", base, OpWrite)
			for _, c := range tt.later {
				s.set("f.txt", c, OpEdit)
			}
			m, err := s.st.MapRange(context.Background(), s.current, "f.txt", fver.Of([]byte(base)), tt.start, tt.end)
			if err != nil {
				t.Fatal(err)
			}
			if m.Start != tt.wantS || m.End != tt.wantE || len(m.Overlaps) != tt.overlaps || m.Path != "f.txt" {
				t.Errorf("got %s %d-%d overlaps=%d, want %d-%d overlaps=%d", m.Path, m.Start, m.End, len(m.Overlaps), tt.wantS, tt.wantE, tt.overlaps)
			}
		})
	}
}

func TestMapRangeMoveAndRemove(t *testing.T) {
	ctx := context.Background()
	s := newSim(t)
	base := lines("a", "b", "c", "d")
	v := fver.Of([]byte(base))
	s.set("old.txt", base, OpWrite)
	s.set("other.txt", "unrelated\n", OpWrite)
	s.mv("old.txt", "new.txt")
	s.set("new.txt", lines("top", "a", "b", "c", "d"), OpEdit)

	m, err := s.st.MapRange(ctx, s.current, "old.txt", v, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if m.Path != "new.txt" || m.Start != 3 || m.End != 4 || len(m.Overlaps) != 0 || m.Removed {
		t.Errorf("after mv: %+v", m)
	}

	s.rm("new.txt")
	m, err = s.st.MapRange(ctx, s.current, "old.txt", v, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Removed || len(m.Overlaps) != 1 || m.Overlaps[0].Op != OpRm {
		t.Errorf("after rm: %+v", m)
	}
}

func TestMapRangeErrors(t *testing.T) {
	ctx := context.Background()
	s := newSim(t)
	s.set("f.txt", lines("a", "b"), OpWrite)
	if _, err := s.st.MapRange(ctx, s.current, "f.txt", "abcdef", 1, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown version: %v", err)
	}
	v := fver.Of([]byte(lines("a", "b")))
	for _, r := range [][2]int{{0, 1}, {2, 3}, {3, 1}} {
		if _, err := s.st.MapRange(ctx, s.current, "f.txt", v, r[0], r[1]); !errors.Is(err, ErrRange) {
			t.Errorf("range %v: %v", r, err)
		}
	}
}

func TestMapFragments(t *testing.T) {
	tests := []struct {
		name     string
		frags    []linediff.Fragment
		lo, hi   int
		wlo, whi int
		hit      bool
	}{
		{"two fragments above", []linediff.Fragment{{Pos: 0, New: []string{"x"}}, {Pos: 2, Old: []string{"c"}}}, 4, 6, 4, 6, false},
		{"above and inside", []linediff.Fragment{{Pos: 0, Old: []string{"a"}}, {Pos: 5, Old: []string{"f"}, New: []string{"F", "G"}}}, 4, 6, 3, 6, true},
		{"covers whole range", []linediff.Fragment{{Pos: 1, Old: []string{"b", "c", "d"}, New: []string{"X"}}}, 2, 3, 1, 2, true},
		{"after range", []linediff.Fragment{{Pos: 3, Old: []string{"d"}}}, 1, 3, 1, 3, false},
	}
	for _, tt := range tests {
		lo, hi, hit := mapFragments(tt.frags, tt.lo, tt.hi)
		if lo != tt.wlo || hi != tt.whi || hit != tt.hit {
			t.Errorf("%s: got [%d,%d) hit=%v, want [%d,%d) hit=%v", tt.name, lo, hi, hit, tt.wlo, tt.whi, tt.hit)
		}
	}
}

func TestMapAfterRemoveAndRecreate(t *testing.T) {
	base := lines("a", "b", "c", "d")
	tests := []struct {
		name     string
		recreate string // content written back after the rm; "" = none
		after    string // a later edit; "" = none
		removed  bool
		overlaps int
		wantS    int
	}{
		{"removed", "", "", true, 1, 2},
		{"restored as it was", base, "", false, 0, 2},
		{"restored then shifted", base, lines("top", "a", "b", "c", "d"), false, 0, 3},
		{"recreated with other content", lines("x"), "", false, 2, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSim(t)
			s.set("f.txt", base, OpWrite)
			s.rm("f.txt")
			if tt.recreate != "" {
				s.set("f.txt", tt.recreate, OpRollback)
			}
			if tt.after != "" {
				s.set("f.txt", tt.after, OpEdit)
			}
			m, err := s.st.MapAfter(context.Background(), "f.txt", 1, 2, 3)
			if err != nil {
				t.Fatal(err)
			}
			if m.Removed != tt.removed || len(m.Overlaps) != tt.overlaps || m.Start != tt.wantS {
				t.Errorf("got %+v", m)
			}
		})
	}
}
