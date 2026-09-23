package history

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/astralyx/lino/internal/linediff"
)

// ErrRange reports a line range that does not fit the version it refers to.
var ErrRange = errors.New("history: line range outside the version")

// Mapped is a line range of an older version carried to the current file.
type Mapped struct {
	Path string // current path (follows mv)
	// Start and End are 1-based and inclusive in the current file. End ==
	// Start-1 is a position before line Start (an empty range). When later
	// changes overlapped, the range grows to cover their new lines.
	Start, End int
	// Overlaps are the later changes, oldest first, that touched the range
	// (without fragments). Changes that only touch adjacent lines are not
	// overlaps.
	Overlaps []Change
	Removed  bool // the file was removed after the version
}

// MapRange carries lines start..end (1-based, inclusive; end == start-1 for
// a position) of path at version v through every later change, shifting it
// by lines inserted or removed above and reporting the changes that touched
// it. An unknown or pruned version is ErrNotFound.
func (s *Store) MapRange(ctx context.Context, cur CurrentFunc, path, v string, start, end int) (Mapped, error) {
	ver, err := s.Reconstruct(ctx, cur, path, v)
	if err != nil {
		return Mapped{}, err
	}
	if start < 1 || end < start-1 || end > len(ver.Doc.Lines) {
		return Mapped{}, fmt.Errorf("%w: lines %d-%d of %d", ErrRange, start, end, len(ver.Doc.Lines))
	}
	m := Mapped{Path: ver.Path, Start: start, End: end}
	if ver.Next == 0 {
		return m, nil
	}
	return s.walk(ctx, m, ver.Next-1)
}

// MapAfter is MapRange for lines start..end of path as it was right after
// change id, located by id instead of by version. The range is not checked
// against the file's length at that point.
func (s *Store) MapAfter(ctx context.Context, path string, id int64, start, end int) (Mapped, error) {
	if start < 1 || end < start-1 {
		return Mapped{}, fmt.Errorf("%w: lines %d-%d", ErrRange, start, end)
	}
	return s.walk(ctx, Mapped{Path: path, Start: start, End: end}, id)
}

// walk carries m through every change after point on its path. A removal
// followed by a re-creation with the same content (an undone rm) cancels
// out; a re-creation with other content is an overlap.
func (s *Store) walk(ctx context.Context, m Mapped, point int64) (Mapped, error) {
	lo, hi := m.Start-1, m.End // 0-based, hi exclusive
	var gone Change            // the removal, while m.Removed
	for {
		cs, err := s.List(ctx, Filter{Path: m.Path, Since: point, Limit: pageSize, Asc: true})
		if err != nil {
			return Mapped{}, err
		}
		moved := false
		for _, c := range cs {
			point = c.ID
			switch {
			case m.Removed:
				m.Removed = false
				if c.Extra.Created && !c.Extra.Binary && c.VAfter != "" && c.VAfter == gone.VBefore {
					m.Overlaps = slices.DeleteFunc(m.Overlaps, func(o Change) bool { return o.ID == gone.ID })
				} else {
					m.Overlaps = append(m.Overlaps, c)
				}
				m.Removed = c.Extra.Removed
				if c.Extra.From != "" && c.Extra.From == m.Path {
					m.Path, moved = c.Path, true
				}
			case c.Extra.From == m.Path: // moved away: follow it
				m.Path = c.Path
				moved = true
			case c.Extra.From != "": // another file moved over this one
				m.Overlaps = append(m.Overlaps, c)
			case c.Extra.Binary || c.Extra.Symlink:
				m.Overlaps = append(m.Overlaps, c)
			case c.Extra.Removed:
				m.Overlaps = append(m.Overlaps, c)
				m.Removed, gone = true, c
			default:
				frags, err := s.Fragments(ctx, c.ID)
				if err != nil {
					return Mapped{}, err
				}
				var hit bool
				lo, hi, hit = mapFragments(frags, lo, hi)
				if hit {
					m.Overlaps = append(m.Overlaps, c)
				}
			}
			if moved {
				break
			}
		}
		if !moved && len(cs) < pageSize {
			break
		}
	}
	m.Start, m.End = lo+1, hi
	return m, nil
}

// mapFragments carries the 0-based half-open range [lo, hi) of the version
// before frags to the version after them. hit reports whether a fragment
// changed lines inside the range (for an empty range: replaced lines around
// the position or inserted exactly at it). Boundaries inside a fragment move
// to the edge of its new lines, so the result covers them.
func mapFragments(frags []linediff.Fragment, lo, hi int) (nlo, nhi int, hit bool) {
	empty := lo == hi
	nlo, nhi = lo, hi
	shift := 0
	for _, f := range frags {
		p, o, n := f.Pos, len(f.Old), len(f.New)
		d := n - o
		if empty {
			hit = hit || (o > 0 && p < lo && lo < p+o) || (o == 0 && p == lo)
		} else {
			hit = hit || (o > 0 && p < hi && p+o > lo) || (o == 0 && lo < p && p < hi)
		}
		switch {
		case p+o <= lo && (o > 0 || p <= lo):
			nlo += d
		case p < lo && lo < p+o:
			nlo = p + shift
		}
		switch {
		case p+o <= hi && (o > 0 || p < hi):
			nhi += d
		case p < hi && hi < p+o:
			nhi = p + shift + n
		}
		shift += d
	}
	if empty {
		nhi = nlo
	}
	return nlo, nhi, hit
}
