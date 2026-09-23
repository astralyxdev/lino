package search

import (
	"context"
	"regexp"
	"regexp/syntax"
	"sort"
	"strings"

	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/outcome"
)

// Limits that keep the candidate query small; beyond them the analysis gives
// up precision (never correctness).
const (
	maxExact      = 16 // strings in an exact set
	maxClassRunes = 8  // runes in a char class expanded to an exact set
)

// CompileRegex compiles a Go regexp. Invalid syntax is a usage error.
func CompileRegex(pattern string) (*regexp.Regexp, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, outcome.Wrap(outcome.Usage, err, "invalid regex: "+err.Error())
	}
	return re, nil
}

// Regex finds lines matching the Go regexp pattern. Candidates come from the
// trigrams of the literal runs the pattern requires; a pattern without a
// required literal of MinTrigram characters scans every indexed text file.
func Regex(ctx context.Context, db *index.DB, pattern string, opt Options) (Result, error) {
	re, err := CompileRegex(pattern)
	if err != nil {
		return Result{}, err
	}
	match, err := RegexMatch(pattern)
	if err != nil {
		return Result{}, err
	}
	if opt.ScanNote == "" {
		opt.ScanNote = RegexScanNote
	}
	return Run(ctx, db, match, re.MatchString, opt)
}

// RegexMatch returns an FTS5 MATCH expression over the trigram table that
// every file with a line matching pattern satisfies, or "" when the pattern
// requires no usable literal.
func RegexMatch(pattern string) (string, error) {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return "", outcome.Wrap(outcome.Usage, err, "invalid regex: "+err.Error())
	}
	return analyze(re.Simplify()).full().String(), nil
}

// info is what a sub-expression guarantees about any string it matches:
// q holds, and if exact != nil the string is one of exact (lowercased).
type info struct {
	q     query
	exact []string
}

func (i info) full() query {
	if i.exact == nil {
		return i.q
	}
	return and(i.q, exactQuery(i.exact))
}

func anyInfo() info              { return info{q: query{}} }
func exactInfo(s ...string) info { return info{q: query{}, exact: dedup(s)} }

func analyze(re *syntax.Regexp) info {
	switch re.Op {
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine,
		syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return exactInfo("")
	case syntax.OpLiteral:
		return exactInfo(strings.ToLower(string(re.Rune)))
	case syntax.OpCharClass:
		var rs []string
		for i := 0; i+1 < len(re.Rune); i += 2 {
			lo, hi := re.Rune[i], re.Rune[i+1]
			if int(hi-lo)+1+len(rs) > maxClassRunes {
				return anyInfo()
			}
			for r := lo; r <= hi; r++ {
				rs = append(rs, strings.ToLower(string(r)))
			}
		}
		if len(rs) == 0 {
			return anyInfo()
		}
		return exactInfo(rs...)
	case syntax.OpCapture:
		return analyze(re.Sub[0])
	case syntax.OpQuest:
		sub := analyze(re.Sub[0])
		if sub.exact != nil && sub.q.isAll() && len(sub.exact) < maxExact {
			return exactInfo(append(sub.exact, "")...)
		}
		return anyInfo()
	case syntax.OpPlus:
		return info{q: analyze(re.Sub[0]).full()}
	case syntax.OpRepeat:
		if re.Min >= 1 {
			return info{q: analyze(re.Sub[0]).full()}
		}
		return anyInfo()
	case syntax.OpConcat:
		acc := exactInfo("")
		for _, sub := range re.Sub {
			acc = concat(acc, analyze(sub))
		}
		return acc
	case syntax.OpAlternate:
		acc := analyze(re.Sub[0])
		for _, sub := range re.Sub[1:] {
			acc = alternate(acc, analyze(sub))
		}
		return acc
	}
	return anyInfo() // AnyChar, AnyCharNotNL, Star, NoMatch
}

func concat(a, b info) info {
	if a.exact != nil && b.exact != nil && len(a.exact)*len(b.exact) <= maxExact {
		var xs []string
		for _, x := range a.exact {
			for _, y := range b.exact {
				xs = append(xs, x+y)
			}
		}
		return info{q: and(a.q, b.q), exact: dedup(xs)}
	}
	return info{q: and(a.full(), b.full())}
}

func alternate(a, b info) info {
	if a.exact != nil && b.exact != nil && len(a.exact)+len(b.exact) <= maxExact {
		return info{q: or(a.q, b.q), exact: dedup(append(append([]string(nil), a.exact...), b.exact...))}
	}
	return info{q: or(a.full(), b.full())}
}

func dedup(s []string) []string {
	out := make([]string, 0, len(s))
	seen := map[string]bool{}
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// exactQuery is the OR over the strings of the AND of their trigrams. Any
// string shorter than a trigram makes it unconstrained.
func exactQuery(set []string) query {
	var alts []query
	for _, s := range set {
		m := TrigramMatch(s)
		if m == "" {
			return query{}
		}
		alts = append(alts, query{leaf: m})
	}
	if len(alts) == 0 {
		return query{}
	}
	q := alts[0]
	for _, x := range alts[1:] {
		q = or(q, x)
	}
	return q
}

// query is a boolean trigram expression. The zero value matches everything.
type query struct {
	op   byte // 0 leaf/all, '&' and, '|' or
	leaf string
	subs []query
}

func (q query) isAll() bool { return q.op == 0 && q.leaf == "" }

func and(a, b query) query {
	switch {
	case a.isAll():
		return b
	case b.isAll():
		return a
	}
	return query{op: '&', subs: append(append([]query(nil), flatten(a, '&')...), flatten(b, '&')...)}
}

func or(a, b query) query {
	if a.isAll() || b.isAll() {
		return query{}
	}
	return query{op: '|', subs: append(append([]query(nil), flatten(a, '|')...), flatten(b, '|')...)}
}

func flatten(q query, op byte) []query {
	if q.op == op {
		return q.subs
	}
	return []query{q}
}

// String renders q as an FTS5 expression; "" means match everything.
func (q query) String() string {
	if q.op == 0 {
		return q.leaf
	}
	sep := " AND "
	if q.op == '|' {
		sep = " OR "
	}
	parts := make([]string, len(q.subs))
	for i, s := range q.subs {
		parts[i] = "(" + s.String() + ")"
	}
	return strings.Join(parts, sep)
}
