// Package search runs queries against the index: trigram candidates from the
// FTS5 table, verified line by line on the stored content.
package search

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/astralyx/lino/internal/index"
)

// Source says how candidates were found.
type Source string

const (
	FromIndex Source = "index" // trigram index
	FromScan  Source = "scan"  // every indexed text file
)

// MinTrigram is the shortest literal the trigram index can serve.
const MinTrigram = 3

// maxTrigrams caps the tokens in one MATCH expression; a spread subset of a
// long literal's trigrams selects candidates as well as all of them.
const maxTrigrams = 12

// Hit is one matching line. Line is 1-based; Text has no line ending.
type Hit struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
	// Before and After are -C context lines; hit lines are never repeated.
	Before []ContextLine `json:"before,omitempty"`
	After  []ContextLine `json:"after,omitempty"`
}

// Result is the outcome of a search. More is set when hits beyond Limit exist.
type Result struct {
	Hits   []Hit  `json:"hits"`
	Files  int    `json:"files"`
	Source Source `json:"source"`
	More   bool   `json:"more,omitempty"`
	Note   string `json:"note,omitempty"` // why the index was not used
}

// Options bound a search. Limit <= 0 means no limit.
type Options struct {
	Limit    int
	Paths    []string // root-relative globs (see MatchPath); none = all files
	Context  int      // -C: lines of context around each hit
	ScanNote string   // reported when the search falls back to a scan
}

// LineMatcher reports whether a line (without its line ending) is a hit.
type LineMatcher func(line string) bool

// Literal finds lines containing q exactly (case-sensitive). Queries shorter
// than MinTrigram runes scan every indexed text file.
func Literal(ctx context.Context, db *index.DB, q string, opt Options) (Result, error) {
	if q == "" {
		return Result{Source: FromScan}, nil
	}
	if opt.ScanNote == "" {
		opt.ScanNote = ShortQueryNote
	}
	return Run(ctx, db, TrigramMatch(q), func(l string) bool { return strings.Contains(l, q) }, opt)
}

// TrigramMatch returns an FTS5 MATCH expression that every file containing
// lit satisfies (the AND of its trigrams), or "" when lit is too short.
// The trigram table is case-insensitive, so the result is a superset.
func TrigramMatch(lit string) string {
	runes := []rune(lit)
	n := len(runes) - MinTrigram + 1
	if n < 1 {
		return ""
	}
	seen := map[string]bool{}
	var terms []string
	add := func(i int) {
		t := strings.ToLower(string(runes[i : i+MinTrigram]))
		if seen[t] {
			return
		}
		seen[t] = true
		terms = append(terms, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
	}
	if n <= maxTrigrams {
		for i := 0; i < n; i++ {
			add(i)
		}
	} else {
		for k := 0; k < maxTrigrams; k++ {
			add(k * (n - 1) / (maxTrigrams - 1))
		}
	}
	return strings.Join(terms, " AND ")
}

// Batch sizes for loading candidate content: small first, since common
// queries fill -k from the first few files, then growing.
const (
	firstBatch = 4
	maxBatch   = 64
)

// Run verifies the lines of the candidate files with m, in path order.
// match is an FTS5 expression over the trigram table, whose rows are file
// chunks: a file is a candidate when one chunk satisfies it, which every
// matching line's chunk does. "" scans all text
// files. Candidates stream in path order and their content is loaded in
// small batches, so the search stops as soon as Limit+1 hits are seen.
func Run(ctx context.Context, db *index.DB, match string, m LineMatcher, opt Options) (Result, error) {
	res := Result{Source: FromIndex, Hits: []Hit{}}
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, fmt.Errorf("search: %w", err)
	}
	defer tx.Rollback()
	// files_stat is ordered by path and covers id, so no candidate content
	// is read or sorted up front. Binary files have empty content and so
	// never match a trigram; loadContent skips them for scans.
	var rows *sql.Rows
	if match == "" {
		res.Source = FromScan
		res.Note = noteFor(opt)
		rows, err = tx.QueryContext(ctx, `SELECT id, path FROM files INDEXED BY files_stat ORDER BY path`)
	} else {
		rows, err = tx.QueryContext(ctx, `SELECT id, path FROM files INDEXED BY files_stat
			WHERE id IN (`+index.MatchFiles+`) ORDER BY path`, match)
	}
	if err != nil {
		return Result{}, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	type cand struct {
		id   int64
		path string
	}
	var (
		batch []cand
		size  = firstBatch
		last  string
		stop  bool
	)
	verify := func() error {
		ids := make([]int64, len(batch))
		for i, c := range batch {
			ids[i] = c.id
		}
		contents, err := loadContent(ctx, tx, ids)
		if err != nil {
			return err
		}
		for _, c := range batch {
			content, ok := contents[c.id]
			if !ok {
				continue
			}
			first := len(res.Hits)
			EachLine(content, func(n int, line string) bool {
				if !m(line) {
					return true
				}
				if opt.Limit > 0 && len(res.Hits) >= opt.Limit {
					res.More, stop = true, true
					return false
				}
				res.Hits = append(res.Hits, Hit{Path: c.path, Line: n, Text: line})
				if c.path != last {
					res.Files++
					last = c.path
				}
				return true
			})
			addContext(content, res.Hits[first:], opt.Context)
			if stop {
				break
			}
		}
		batch = batch[:0]
		size = min(size*2, maxBatch)
		return ctx.Err()
	}
	for !stop && rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.path); err != nil {
			return Result{}, fmt.Errorf("search: %w", err)
		}
		if !MatchPath(opt.Paths, c.path) {
			continue
		}
		if batch = append(batch, c); len(batch) >= size {
			if err := verify(); err != nil {
				return Result{}, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return Result{}, fmt.Errorf("search: %w", err)
	}
	if !stop && len(batch) > 0 {
		if err := verify(); err != nil {
			return Result{}, err
		}
	}
	return res, nil
}

// loadContent returns the content of the text files among ids.
func loadContent(ctx context.Context, tx *sql.Tx, ids []int64) (map[int64]string, error) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	q := `SELECT id, content FROM files WHERE binary = 0 AND id IN (?` + strings.Repeat(",?", len(ids)-1) + `)`
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]string, len(ids))
	for rows.Next() {
		var (
			id      int64
			content string
		)
		if err := rows.Scan(&id, &content); err != nil {
			return nil, fmt.Errorf("search: %w", err)
		}
		out[id] = content
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	return out, nil
}

// EachLine calls fn with every line of content (1-based, without "\n" or a
// trailing "\r", BOM stripped from the first line) until fn returns false.
func EachLine(content string, fn func(n int, line string) bool) {
	content = strings.TrimPrefix(content, "\uFEFF")
	for n := 1; content != ""; n++ {
		line, rest, _ := strings.Cut(content, "\n")
		content = rest
		if !fn(n, strings.TrimSuffix(line, "\r")) {
			return
		}
	}
}

// Cut shortens s to at most max runes, marking the cut with "…".
func Cut(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}
