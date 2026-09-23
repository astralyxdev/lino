// Package linediff computes line-level diffs as fragments and applies them.
//
// A fragment replaces Old with New at Pos, a 0-based line index in the source
// the fragments apply to. Fragments from Diff are sorted by Pos, never overlap
// and never touch: at least one unchanged line separates two fragments.
package linediff

import (
	"errors"
	"fmt"
)

// Fragment is one changed range.
type Fragment struct {
	Pos int
	Old []string
	New []string
}

// ErrMismatch reports that fragments do not fit the lines they are applied to.
var ErrMismatch = errors.New("linediff: fragment does not match source")

// Diff returns the minimal fragments that turn a into b (Myers, linear space).
func Diff(a, b []string) []Fragment {
	ids := make(map[string]int, len(a)+len(b))
	intern := func(ls []string) []int {
		out := make([]int, len(ls))
		for i, l := range ls {
			id, ok := ids[l]
			if !ok {
				id = len(ids)
				ids[l] = id
			}
			out[i] = id
		}
		return out
	}
	d := &differ{a: intern(a), b: intern(b)}
	d.del = make([]bool, len(a))
	d.ins = make([]bool, len(b))

	lo, ahi, bhi := 0, len(a), len(b)
	for lo < ahi && lo < bhi && d.a[lo] == d.b[lo] {
		lo++
	}
	for ahi > lo && bhi > lo && d.a[ahi-1] == d.b[bhi-1] {
		ahi--
		bhi--
	}
	if shared(d.a[lo:ahi], d.b[lo:bhi], len(ids)) {
		d.compare(lo, lo, ahi, bhi)
	} else {
		for i := lo; i < ahi; i++ {
			d.del[i] = true
		}
		for j := lo; j < bhi; j++ {
			d.ins[j] = true
		}
	}
	return d.fragments(a, b)
}

// shared reports whether a and b have any line in common. When they have
// none the whole range is one replacement, and Myers would be quadratic.
func shared(a, b []int, n int) bool {
	seen := make([]bool, n)
	for _, id := range a {
		seen[id] = true
	}
	for _, id := range b {
		if seen[id] {
			return true
		}
	}
	return false
}

type differ struct {
	a, b     []int
	del, ins []bool
	vf, vb   []int
}

type point struct{ x, y int }

// compare marks deleted and inserted lines of the box a[left:right], b[top:bottom].
func (d *differ) compare(left, top, right, bottom int) {
	for left < right && top < bottom && d.a[left] == d.b[top] {
		left++
		top++
	}
	for right > left && bottom > top && d.a[right-1] == d.b[bottom-1] {
		right--
		bottom--
	}
	switch {
	case left == right:
		for y := top; y < bottom; y++ {
			d.ins[y] = true
		}
		return
	case top == bottom:
		for x := left; x < right; x++ {
			d.del[x] = true
		}
		return
	}
	start, end := d.midpoint(left, top, right, bottom)
	d.compare(left, top, start.x, start.y)
	// The snake is at most one move plus diagonal matches, the move either
	// first or last. Matching greedily first puts it in a valid place.
	x, y := start.x, start.y
	for x < end.x && y < end.y && d.a[x] == d.b[y] {
		x++
		y++
	}
	if end.x-x > end.y-y {
		d.del[x] = true
	} else if end.x-x < end.y-y {
		d.ins[y] = true
	}
	d.compare(end.x, end.y, right, bottom)
}

// midpoint finds the middle snake of the box, returning its start and end.
func (d *differ) midpoint(left, top, right, bottom int) (point, point) {
	w, h := right-left, bottom-top
	delta := w - h
	max := (w + h + 1) / 2
	size := 2*max + 3
	if cap(d.vf) < size {
		d.vf = make([]int, size)
		d.vb = make([]int, size)
	}
	vf, vb := d.vf[:size], d.vb[:size]
	off := max + 1
	vf[off+1] = left
	vb[off+1] = bottom
	odd := delta%2 != 0

	for step := 0; step <= max; step++ {
		for k := step; k >= -step; k -= 2 {
			var x, px int
			if k == -step || (k != step && vf[off+k-1] < vf[off+k+1]) {
				x = vf[off+k+1]
				px = x
			} else {
				px = vf[off+k-1]
				x = px + 1
			}
			y := top + (x - left) - k
			py := y
			if step != 0 && x == px {
				py = y - 1
			}
			for x < right && y < bottom && d.a[x] == d.b[y] {
				x++
				y++
			}
			vf[off+k] = x
			c := k - delta
			if odd && c >= -(step-1) && c <= step-1 && y >= vb[off+c] {
				return point{px, py}, point{x, y}
			}
		}
		for c := step; c >= -step; c -= 2 {
			var y, py int
			if c == -step || (c != step && vb[off+c-1] > vb[off+c+1]) {
				y = vb[off+c+1]
				py = y
			} else {
				py = vb[off+c-1]
				y = py - 1
			}
			k := c + delta
			x := left + (y - top) + k
			px := x
			if step != 0 && y == py {
				px = x + 1
			}
			for x > left && y > top && d.a[x-1] == d.b[y-1] {
				x--
				y--
			}
			vb[off+c] = y
			if !odd && k >= -step && k <= step && x <= vf[off+k] {
				return point{x, y}, point{px, py}
			}
		}
	}
	panic("linediff: no middle snake")
}

func (d *differ) fragments(a, b []string) []Fragment {
	var out []Fragment
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		if i < len(a) && j < len(b) && !d.del[i] && !d.ins[j] {
			i++
			j++
			continue
		}
		f := Fragment{Pos: i}
		si, sj := i, j
		for (i < len(a) && d.del[i]) || (j < len(b) && d.ins[j]) {
			for i < len(a) && d.del[i] {
				i++
			}
			for j < len(b) && d.ins[j] {
				j++
			}
		}
		if i > si {
			f.Old = a[si:i:i]
		}
		if j > sj {
			f.New = b[sj:j:j]
		}
		out = append(out, f)
	}
	return out
}

// Apply returns src with fragments applied. Fragments must be sorted, must not
// overlap, and their Old lines must match src.
func Apply(src []string, frags []Fragment) ([]string, error) {
	n := len(src)
	for _, f := range frags {
		n += len(f.New) - len(f.Old)
	}
	if n < 0 {
		return nil, ErrMismatch
	}
	out := make([]string, 0, n)
	prev := 0
	for i, f := range frags {
		if f.Pos < prev || f.Pos+len(f.Old) > len(src) {
			return nil, fmt.Errorf("%w: fragment %d at line %d out of order or range", ErrMismatch, i, f.Pos)
		}
		for k, l := range f.Old {
			if src[f.Pos+k] != l {
				return nil, fmt.Errorf("%w: fragment %d at line %d", ErrMismatch, i, f.Pos+k)
			}
		}
		out = append(out, src[prev:f.Pos]...)
		out = append(out, f.New...)
		prev = f.Pos + len(f.Old)
	}
	return append(out, src[prev:]...), nil
}

// Reverse returns fragments that undo frags: applied to the result of
// Apply(src, frags) they give src back.
func Reverse(frags []Fragment) []Fragment {
	out := make([]Fragment, len(frags))
	shift := 0
	for i, f := range frags {
		out[i] = Fragment{Pos: f.Pos + shift, Old: f.New, New: f.Old}
		shift += len(f.New) - len(f.Old)
	}
	return out
}

// Unapply undoes frags on dst, the result of applying them.
func Unapply(dst []string, frags []Fragment) ([]string, error) {
	return Apply(dst, Reverse(frags))
}
