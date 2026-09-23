package anchor

import (
	"github.com/astralyx/lino/internal/outcome"
)

// Window is how far (in lines, each way) a hashed anchor may relocate.
const Window = 50

// Region lines printed around a mismatch, and the cap on their number.
const (
	regionContext = 2
	regionMax     = 40
)

// Resolved is a range located in the current lines (1-based, inclusive).
// Offset is how far the anchors moved; 0 when they matched in place.
type Resolved struct {
	Start, End int
	Offset     int
}

// Resolve locates a single anchor in lines (without trailing \r). A plain
// line number only needs to be in range. A hashed anchor is valid if its line
// still has the hash, otherwise it relocates to the unique line with that hash
// within ±Window. Failures are anchor_mismatch errors carrying the current
// lines of the region; the caller sets Path.
func Resolve(lines []string, a Anchor) (Resolved, error) {
	return ResolveRange(lines, a, a)
}

// ResolveRange locates start..end. Both anchors must match at the same offset
// and that offset must be unique within ±Window; plain line numbers only match
// in place. The resolved end must not precede the start.
func ResolveRange(lines []string, start, end Anchor) (Resolved, error) {
	if start.Line < 1 || end.Line < 1 {
		return Resolved{}, outcome.New(outcome.Usage, "line numbers start at 1")
	}
	if end.Line < start.Line {
		return Resolved{}, outcome.New(outcome.Usage, "range end %d is before start %d", end.Line, start.Line)
	}
	n := len(lines)
	matches := func(a Anchor, d int) bool {
		i := a.Line + d
		if i < 1 || i > n {
			return false
		}
		if !a.HasHash() {
			return d == 0
		}
		return Hash(lines[i-1]) == a.Hash
	}
	both := func(d int) bool { return matches(start, d) && matches(end, d) }

	if both(0) {
		return Resolved{Start: start.Line, End: end.Line}, nil
	}
	if !start.HasHash() && !end.HasHash() {
		return Resolved{}, mismatch(lines, start, end, "line %s is beyond the end of the file (%d lines)", end, n)
	}
	found, count := 0, 0
	for d := -Window; d <= Window; d++ {
		if d != 0 && both(d) {
			found, count = d, count+1
		}
	}
	switch {
	case count == 1:
		return Resolved{Start: start.Line + found, End: end.Line + found, Offset: found}, nil
	case count > 1:
		return Resolved{}, mismatch(lines, start, end, "%s matches %d places within ±%d lines; re-read with --anchors", label(start, end), count, Window)
	default:
		return Resolved{}, mismatch(lines, start, end, "%s no longer matches; re-read with --anchors", label(start, end))
	}
}

func label(start, end Anchor) string {
	if start == end {
		return "anchor " + start.String()
	}
	return "range " + start.String() + " " + end.String()
}

func mismatch(lines []string, start, end Anchor, format string, args ...any) *outcome.Error {
	e := outcome.New(outcome.AnchorMismatch, format, args...)
	return e.WithLines("", Region(lines, start.Line, end.Line))
}

// Region returns the current lines from..to (1-based) with regionContext lines
// on each side, clamped to the file and capped at regionMax lines, with anchors.
// For a range past EOF it returns the file's last lines.
func Region(lines []string, from, to int) []outcome.Line {
	n := len(lines)
	if n == 0 {
		return nil
	}
	lo, hi := from-regionContext, to+regionContext
	if lo > n {
		lo = n - regionContext
	}
	lo = max(lo, 1)
	hi = min(hi, n, lo+regionMax-1)
	out := make([]outcome.Line, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		out = append(out, outcome.Line{N: i, Anchor: Hash(lines[i-1]), Text: lines[i-1]})
	}
	return out
}
