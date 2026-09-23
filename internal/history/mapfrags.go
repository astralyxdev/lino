package history

import "github.com/astralyx/lino/internal/linediff"

// MapFragments carries lines start..end (1-based, inclusive; end == start-1
// for a position) of the lines before frags to the lines after them, like
// MapRange does for one change. hit reports whether frags changed the range.
func MapFragments(frags []linediff.Fragment, start, end int) (nstart, nend int, hit bool) {
	lo, hi, hit := mapFragments(frags, start-1, end)
	return lo + 1, hi, hit
}
