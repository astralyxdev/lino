// Command ftsquery is the milestone 2 spike, part 2: it runs a fixed query set
// against a database built by ftsbuild (file layout) and reports p50/p95
// latency per query, in-process, including fetching candidate content and
// verifying hits line by line in Go.
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

type query struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // literal | regex | words
	Q    string `json:"q"`
}

var queries = []query{
	{"rare literal", "literal", "maxConsecutiveEmptyReads"},
	{"rare literal 2", "literal", "ErrUnexpectedEOF"},
	{"medium literal", "literal", "ReadFull"},
	{"common literal", "literal", "return err"},
	{"very common literal", "literal", "if err != nil"},
	{"no hits", "literal", "zzqxjv_nothere"},
	{"short (scan)", "literal", "ok"},
	{"regex literal", "regex", `func \w+Reader\(`},
	{"regex prefix", "regex", `ErrUnexpected\w+`},
	{"regex common", "regex", `if err := .*; err != nil`},
	{"regex no literal (scan)", "regex", `\bx\d\b`},
	{"words 1", "words", "mutex"},
	{"words 2", "words", "buffer reader"},
	{"words 3", "words", "checksum crc32"},
}

type result struct {
	Query      query   `json:"query"`
	Strategy   string  `json:"strategy"`
	Candidates int     `json:"candidates"`
	Hits       int     `json:"hits"`
	Files      int     `json:"files"`
	P50ms      float64 `json:"p50_ms"`
	P95ms      float64 `json:"p95_ms"`
	Maxms      float64 `json:"max_ms"`
}

type stats struct{ candidates, hits, files int }

func main() {
	dbPath := flag.String("db", "", "database built by ftsbuild -layout file")
	detail := flag.String("detail", "full", "trigram detail the db was built with: full | none")
	iters := flag.Int("iters", 50, "timed runs per query")
	k := flag.Int("k", 20, "hit limit; 0 counts every hit")
	engine := flag.String("engine", "sqlite", "sqlite | mem (in-memory trigram index)")
	flag.Parse()

	if err := run(*dbPath, *detail, *engine, *iters, *k); err != nil {
		fmt.Fprintln(os.Stderr, "ftsquery:", err)
		os.Exit(1)
	}
}

func run(dbPath, detail, engine string, iters, k int) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, p := range []string{"PRAGMA query_only=1", "PRAGMA cache_size=-65536", "PRAGMA mmap_size=268435456"} {
		if _, err := db.Exec(p); err != nil {
			return err
		}
	}

	var s searcher
	switch engine {
	case "sqlite":
		s = &sqliteSearcher{db: db, detail: detail}
	case "mem":
		t := time.Now()
		m, err := loadMem(db)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "mem index built in %v\n", time.Since(t))
		s = m
	default:
		return fmt.Errorf("unknown engine %q", engine)
	}

	var all []time.Duration
	var out []result
	for _, q := range queries {
		var st stats
		strategy := ""
		for range 3 {
			st, strategy, err = s.search(q, k)
			if err != nil {
				return fmt.Errorf("%s: %w", q.Name, err)
			}
		}
		ds := make([]time.Duration, iters)
		for i := range ds {
			t := time.Now()
			if _, _, err := s.search(q, k); err != nil {
				return err
			}
			ds[i] = time.Since(t)
		}
		all = append(all, ds...)
		out = append(out, result{
			Query: q, Strategy: strategy,
			Candidates: st.candidates, Hits: st.hits, Files: st.files,
			P50ms: ms(pct(ds, 50)), P95ms: ms(pct(ds, 95)), Maxms: ms(pct(ds, 100)),
		})
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{
		"engine": engine, "detail": detail, "k": k, "iters": iters,
		"overall_p50_ms": ms(pct(all, 50)), "overall_p95_ms": ms(pct(all, 95)),
		"queries": out,
	})
}

type searcher interface {
	search(q query, k int) (stats, string, error)
}

type sqliteSearcher struct {
	db      *sql.DB
	detail  string
	content *sql.Stmt
}

func (s *sqliteSearcher) search(q query, k int) (stats, string, error) {
	switch q.Kind {
	case "literal":
		v := literalMatcher(q.Q)
		if utf8.RuneCountInString(q.Q) < 3 {
			return s.scan(v, k, "full scan")
		}
		return s.candidates(trigramMatch([]string{q.Q}, s.detail), v, k)
	case "regex":
		re, err := regexp.Compile("(?m)" + q.Q)
		if err != nil {
			return stats{}, "", err
		}
		lits, err := requiredLiterals(q.Q)
		if err != nil {
			return stats{}, "", err
		}
		v := regexMatcher{re}
		if len(lits) == 0 {
			return s.scan(v, k, "full scan")
		}
		return s.candidates(trigramMatch(lits, s.detail), v, k)
	case "words":
		return s.words(q.Q, k)
	}
	return stats{}, "", fmt.Errorf("unknown kind %q", q.Kind)
}

func (s *sqliteSearcher) candidates(match string, v matcher, k int) (stats, string, error) {
	st, err := s.verifyIDs("SELECT f.id FROM tri JOIN files f ON f.id = tri.rowid WHERE tri MATCH ? ORDER BY f.path", v, k, match)
	return st, "trigram " + s.detail, err
}

func (s *sqliteSearcher) scan(v matcher, k int, strategy string) (stats, string, error) {
	st, err := s.verifyIDs("SELECT id FROM files ORDER BY path", v, k)
	return st, strategy, err
}

// verifyIDs lists candidate ids in path order first, then loads content one
// file at a time, so a truncated search never reads content it won't show.
func (s *sqliteSearcher) verifyIDs(idQuery string, m matcher, k int, args ...any) (stats, error) {
	var st stats
	rows, err := s.db.Query(idQuery, args...)
	if err != nil {
		return st, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return st, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}
	if s.content == nil {
		if s.content, err = s.db.Prepare("SELECT content FROM files WHERE id = ?"); err != nil {
			return st, err
		}
	}
	for _, id := range ids {
		var content string
		if err := s.content.QueryRow(id).Scan(&content); err != nil {
			return st, err
		}
		st.candidates++
		lim := 0
		if k > 0 {
			lim = k - st.hits
		}
		if n := m.count(content, lim); n > 0 {
			st.files++
			st.hits += n
		}
		if k > 0 && st.hits >= k {
			break // truncated: the caller continues from the last file shown
		}
	}
	return st, nil
}

func (s *sqliteSearcher) words(q string, k int) (stats, string, error) {
	lim := k
	if lim == 0 {
		lim = 200
	}
	rows, err := s.db.Query("SELECT f.path, f.content FROM words JOIN files f ON f.id = words.rowid WHERE words MATCH ? ORDER BY rank LIMIT ?", q, lim)
	if err != nil {
		return stats{}, "", err
	}
	terms := strings.Fields(strings.ToLower(q))
	v := func(line string) bool {
		l := strings.ToLower(line)
		for _, t := range terms {
			if strings.Contains(l, t) {
				return true
			}
		}
		return false
	}
	// BM25 ranks files; show up to k matching lines, at most 3 per file.
	defer rows.Close()
	var st stats
	for rows.Next() {
		var path, content string
		if err := rows.Scan(&path, &content); err != nil {
			return st, "", err
		}
		st.candidates++
		n := verify(content, v, 3)
		if n > 0 {
			st.files++
			st.hits += n
		}
	}
	return st, "fts5 words bm25", rows.Err()
}

// verify counts lines of content matched by v, stopping at lim (0 = no limit).
func verify(content string, v func(string) bool, lim int) int {
	n := 0
	for len(content) > 0 {
		i := strings.IndexByte(content, '\n')
		line := content
		if i >= 0 {
			line, content = content[:i], content[i+1:]
		} else {
			content = ""
		}
		if v(line) {
			n++
			if lim > 0 && n >= lim {
				break
			}
		}
	}
	return n
}

// trigramMatch builds an FTS5 MATCH expression requiring every literal. With
// detail=full a literal is a phrase; detail=none rejects phrases, so each
// literal becomes an AND of its trigrams.
func trigramMatch(lits []string, detail string) string {
	var parts []string
	for _, l := range lits {
		if detail != "none" {
			parts = append(parts, quote(l))
			continue
		}
		for _, t := range trigrams(l) {
			parts = append(parts, quote(t))
		}
	}
	return strings.Join(parts, " AND ")
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func trigrams(s string) []string {
	r := []rune(strings.ToLower(s))
	var out []string
	for i := 0; i+3 <= len(r); i++ {
		t := string(r[i : i+3])
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// requiredLiterals returns case-sensitive literal runs of 3+ runes that every
// match must contain: literals directly under a top-level concatenation.
func requiredLiterals(expr string) ([]string, error) {
	re, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return nil, err
	}
	re = re.Simplify()
	subs := []*syntax.Regexp{re}
	if re.Op == syntax.OpConcat {
		subs = re.Sub
	}
	var out []string
	for _, s := range subs {
		if s.Op == syntax.OpLiteral && s.Flags&syntax.FoldCase == 0 && len(s.Rune) >= 3 {
			out = append(out, string(s.Rune))
		}
	}
	return out, nil
}

func pct(ds []time.Duration, p int) time.Duration {
	s := slices.Clone(ds)
	slices.Sort(s)
	i := (len(s)*p + 99) / 100
	return s[max(i-1, 0)]
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
