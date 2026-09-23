package linediff

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
)

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func TestDiff(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want []Fragment
	}{
		{"equal", "a,b,c", "a,b,c", nil},
		{"both empty", "", "", nil},
		{"create", "", "a,b", []Fragment{{Pos: 0, New: []string{"a", "b"}}}},
		{"clear", "a,b", "", []Fragment{{Pos: 0, Old: []string{"a", "b"}}}},
		{"edit middle", "a,b,c", "a,x,c", []Fragment{{Pos: 1, Old: []string{"b"}, New: []string{"x"}}}},
		{"insert end", "a,b", "a,b,c", []Fragment{{Pos: 2, New: []string{"c"}}}},
		{"delete start", "a,b,c", "b,c", []Fragment{{Pos: 0, Old: []string{"a"}}}},
		{"two hunks", "a,b,c,d,e", "x,b,c,d,y", []Fragment{
			{Pos: 0, Old: []string{"a"}, New: []string{"x"}},
			{Pos: 4, Old: []string{"e"}, New: []string{"y"}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Diff(lines(tt.a), lines(tt.b))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Diff = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func randLines(r *rand.Rand, n, alphabet int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprint(r.IntN(alphabet))
	}
	return out
}

func mutate(r *rand.Rand, a []string, alphabet int) []string {
	b := append([]string(nil), a...)
	for range r.IntN(6) {
		pos := 0
		if len(b) > 0 {
			pos = r.IntN(len(b) + 1)
		}
		switch r.IntN(3) {
		case 0:
			b = append(b[:pos], append(randLines(r, r.IntN(4)+1, alphabet), b[pos:]...)...)
		case 1:
			end := min(len(b), pos+r.IntN(4)+1)
			b = append(b[:pos], b[end:]...)
		default:
			if pos < len(b) {
				b[pos] = "m" + fmt.Sprint(r.IntN(alphabet))
			}
		}
	}
	return b
}

func lcs(a, b []string) int {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	return dp[0][0]
}

func TestDiffProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for iter := range 5000 {
		alphabet := []int{2, 4, 20}[iter%3]
		a := randLines(r, r.IntN(40), alphabet)
		var b []string
		if iter%2 == 0 {
			b = randLines(r, r.IntN(40), alphabet)
		} else {
			b = mutate(r, a, alphabet)
		}
		frags := Diff(a, b)

		got, err := Apply(a, frags)
		if err != nil || !eq(got, b) {
			t.Fatalf("apply(diff(a,b), a) != b\na=%v\nb=%v\nfrags=%v\ngot=%v err=%v", a, b, frags, got, err)
		}
		back, err := Unapply(b, frags)
		if err != nil || !eq(back, a) {
			t.Fatalf("reverse(diff(a,b), b) != a\na=%v\nb=%v\nfrags=%v\ngot=%v err=%v", a, b, frags, back, err)
		}

		changed := 0
		for i, f := range frags {
			changed += len(f.Old) + len(f.New)
			if len(f.Old)+len(f.New) == 0 {
				t.Fatalf("empty fragment %v", frags)
			}
			if i > 0 && f.Pos <= frags[i-1].Pos+len(frags[i-1].Old) {
				t.Fatalf("fragments touch or overlap: %v", frags)
			}
		}
		if want := len(a) + len(b) - 2*lcs(a, b); changed != want {
			t.Fatalf("not minimal: %d changed lines, want %d\na=%v\nb=%v\nfrags=%v", changed, want, a, b, frags)
		}
	}
}

func eq(a, b []string) bool {
	return len(a) == len(b) && (len(a) == 0 || reflect.DeepEqual(a, b))
}

func TestApplyMismatch(t *testing.T) {
	src := lines("a,b,c")
	tests := []struct {
		name  string
		frags []Fragment
	}{
		{"old differs", []Fragment{{Pos: 1, Old: []string{"x"}}}},
		{"out of range", []Fragment{{Pos: 3, Old: []string{"c"}}}},
		{"out of order", []Fragment{{Pos: 2, Old: []string{"c"}}, {Pos: 0, Old: []string{"a"}}}},
		{"overlap", []Fragment{{Pos: 0, Old: []string{"a", "b"}}, {Pos: 1, Old: []string{"b"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Apply(src, tt.frags); !errors.Is(err, ErrMismatch) {
				t.Fatalf("err = %v, want ErrMismatch", err)
			}
		})
	}
}

func benchPair(n int, changes int, seed uint64) ([]string, []string) {
	r := rand.New(rand.NewPCG(seed, seed))
	a := make([]string, n)
	for i := range a {
		a[i] = fmt.Sprintf("\tline %d: value = compute(%d)", i, r.IntN(1000))
	}
	b := append([]string(nil), a...)
	for range changes {
		pos := r.IntN(len(b))
		switch r.IntN(3) {
		case 0:
			b[pos] = "changed " + b[pos]
		case 1:
			b = append(b[:pos], b[pos+1:]...)
		default:
			b = append(b[:pos], append([]string{"inserted"}, b[pos:]...)...)
		}
	}
	return a, b
}

func BenchmarkDiff10k(b *testing.B) {
	for _, bc := range []struct {
		name    string
		changes int
	}{{"1edit", 1}, {"50edits", 50}, {"1000edits", 1000}} {
		x, y := benchPair(10000, bc.changes, 7)
		b.Run(bc.name, func(b *testing.B) {
			for b.Loop() {
				Diff(x, y)
			}
		})
	}
	b.Run("disjoint", func(b *testing.B) {
		x, _ := benchPair(10000, 0, 1)
		y := make([]string, len(x))
		for i := range y {
			y[i] = "other " + x[i]
		}
		for b.Loop() {
			Diff(x, y)
		}
	})
}

func BenchmarkApply10k(b *testing.B) {
	x, y := benchPair(10000, 50, 7)
	frags := Diff(x, y)
	for b.Loop() {
		if _, err := Apply(x, frags); err != nil {
			b.Fatal(err)
		}
	}
}
