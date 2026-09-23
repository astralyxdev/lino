package index

import (
	"context"
	"database/sql/driver"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"modernc.org/sqlite"
)

// Word tokenisation, shared by the words index and by line matching:
//
//   - A word is a maximal run of Unicode letters and digits. Everything else,
//     including '_' and '-', separates words, so snake_case and kebab-case
//     split into their parts.
//   - A word with case changes is also split into camelCase parts, and both
//     the whole word and its parts are indexed: ErrInvalidAmount indexes
//     "errinvalidamount", "err", "invalid" and "amount". HTTPServer gives
//     "http" and "server"; digits stay with the letters before them (utf8Decode
//     gives "utf8" and "decode").
//   - Matching is case-insensitive (FTS5 unicode61 folds case and diacritics).
//
// A query word matches a line when the line has the whole word or, for a
// camelCase query word, all of its parts.

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("lino_words", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		switch v := args[0].(type) {
		case string:
			return WordsText(v), nil
		case []byte:
			return WordsText(string(v)), nil
		default:
			return "", nil
		}
	})
}

// WordsText is the text the words index tokenises for content: every word
// followed by its camelCase parts, space-separated.
func WordsText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	eachWord(s, func(w string) {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(w)
		if parts := camelParts(w); len(parts) > 1 {
			for _, p := range parts {
				b.WriteByte(' ')
				b.WriteString(p)
			}
		}
	})
	return b.String()
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func eachWord(s string, fn func(string)) {
	start := -1
	for i, r := range s {
		if isWordRune(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			fn(s[start:i])
			start = -1
		}
	}
	if start >= 0 {
		fn(s[start:])
	}
}

// camelParts splits w at lower→Upper and at the last capital of an acronym
// followed by lower case (HTTPServer → HTTP, Server).
func camelParts(w string) []string {
	rs := []rune(w)
	var parts []string
	start := 0
	for i := 1; i < len(rs); i++ {
		prev, cur := rs[i-1], rs[i]
		split := unicode.IsUpper(cur) && (unicode.IsLower(prev) || unicode.IsDigit(prev)) ||
			unicode.IsUpper(prev) && unicode.IsUpper(cur) && i+1 < len(rs) && unicode.IsLower(rs[i+1])
		if split {
			parts = append(parts, string(rs[start:i]))
			start = i
		}
	}
	return append(parts, string(rs[start:]))
}

// queryTerm is one query word: the whole word and its camelCase parts, lowercase.
type queryTerm struct {
	whole string
	parts []string // nil unless the word splits
}

func parseWordsQuery(q string) []queryTerm {
	var terms []queryTerm
	seen := map[string]bool{}
	eachWord(q, func(w string) {
		t := queryTerm{whole: strings.ToLower(w)}
		if seen[t.whole] {
			return
		}
		seen[t.whole] = true
		if ps := camelParts(w); len(ps) > 1 {
			for _, p := range ps {
				t.parts = append(t.parts, strings.ToLower(p))
			}
		}
		terms = append(terms, t)
	})
	return terms
}

// ftsQuery ORs the terms; a camelCase term matches as a whole or as all its parts.
func ftsQuery(terms []queryTerm) string {
	q := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
	out := make([]string, len(terms))
	for i, t := range terms {
		if t.parts == nil {
			out[i] = q(t.whole)
			continue
		}
		ps := make([]string, len(t.parts))
		for j, p := range t.parts {
			ps[j] = q(p)
		}
		out[i] = "(" + q(t.whole) + " OR (" + strings.Join(ps, " AND ") + "))"
	}
	return strings.Join(out, " OR ")
}

// lineScore counts the query terms a line matches; a whole-word match counts
// double, so a line with ErrInvalidAmount beats one that merely has the parts.
func lineScore(line string, terms []queryTerm) int {
	toks := map[string]bool{}
	eachWord(line, func(w string) {
		toks[strings.ToLower(w)] = true
		if ps := camelParts(w); len(ps) > 1 {
			for _, p := range ps {
				toks[strings.ToLower(p)] = true
			}
		}
	})
	n := 0
	for _, t := range terms {
		if toks[t.whole] {
			n += 2
			continue
		}
		if t.parts == nil {
			continue
		}
		all := true
		for _, p := range t.parts {
			if !toks[p] {
				all = false
				break
			}
		}
		if all {
			n++
		}
	}
	return n
}

// WordsOptions bound a words search.
type WordsOptions struct {
	Limit   int                    // max hit lines over all files; <= 0 means 20
	PerFile int                    // max lines shown per file; <= 0 means 3
	Match   func(path string) bool // optional path filter
}

// WordHit is one matching line. Line is 1-based.
type WordHit struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

// WordFile is one ranked file with its best matching lines, in line order.
type WordFile struct {
	Path  string    `json:"path"`
	Score float64   `json:"score"` // BM25, higher is better
	Hits  []WordHit `json:"hits"`
}

// WordsResult is the outcome of SearchWords. More is true when further
// ranked files were left out by the limit.
type WordsResult struct {
	Files []WordFile `json:"files"`
	Hits  int        `json:"hits"`
	More  bool       `json:"more"`
}

// SearchWords ranks files by BM25 over word tokens and returns each file's
// best matching lines. A query without any word yields an empty result.
func (d *DB) SearchWords(ctx context.Context, query string, opt WordsOptions) (WordsResult, error) {
	var res WordsResult
	if opt.Limit <= 0 {
		opt.Limit = 20
	}
	if opt.PerFile <= 0 {
		opt.PerFile = 3
	}
	terms := parseWordsQuery(query)
	if len(terms) == 0 {
		return res, nil
	}
	rows, err := d.SQL.QueryContext(ctx,
		`SELECT f.path, -bm25(words) AS score FROM words JOIN files f ON f.id = words.rowid
		 WHERE words MATCH ? ORDER BY bm25(words), f.path`, ftsQuery(terms))
	if err != nil {
		return res, fmt.Errorf("words search: %w", err)
	}
	type ranked struct {
		path  string
		score float64
	}
	var files []ranked
	for rows.Next() {
		var r ranked
		if err := rows.Scan(&r.path, &r.score); err != nil {
			rows.Close()
			return res, err
		}
		if opt.Match == nil || opt.Match(r.path) {
			files = append(files, r)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	for _, f := range files {
		if res.Hits >= opt.Limit {
			res.More = true
			break
		}
		fi, ok, err := d.File(ctx, f.path)
		if err != nil {
			return res, err
		}
		if !ok {
			continue
		}
		hits := bestLines(fi.Content, terms, min(opt.PerFile, opt.Limit-res.Hits))
		if len(hits) == 0 {
			continue
		}
		res.Files = append(res.Files, WordFile{Path: f.path, Score: f.score, Hits: hits})
		res.Hits += len(hits)
	}
	return res, nil
}

func bestLines(content string, terms []queryTerm, n int) []WordHit {
	type scored struct {
		WordHit
		score int
	}
	var all []scored
	line := 0
	for len(content) > 0 {
		line++
		l, rest, _ := strings.Cut(content, "\n")
		content = rest
		l = strings.TrimSuffix(l, "\r")
		if line == 1 {
			l = strings.TrimPrefix(l, "\ufeff")
		}
		if s := lineScore(l, terms); s > 0 {
			all = append(all, scored{WordHit{line, l}, s})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })
	if len(all) > n {
		all = all[:n]
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Line < all[j].Line })
	out := make([]WordHit, len(all))
	for i, s := range all {
		out[i] = s.WordHit
	}
	return out
}
