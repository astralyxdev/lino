package main

import (
	"database/sql"
	"regexp"
	"slices"
	"unicode/utf8"
)

// memSearcher is the fallback candidate from the Decisions section: content and
// a byte-trigram posting index held in the live process. Words queries still go
// to SQLite FTS5.
type memSearcher struct {
	paths    []string
	contents []string
	post     map[uint32][]int32
	sql      *sqliteSearcher
}

func loadMem(db *sql.DB) (*memSearcher, error) {
	rows, err := db.Query("SELECT path, content FROM files ORDER BY path")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := &memSearcher{post: map[uint32][]int32{}, sql: &sqliteSearcher{db: db}}
	seen := map[uint32]struct{}{}
	for rows.Next() {
		var p, c string
		if err := rows.Scan(&p, &c); err != nil {
			return nil, err
		}
		id := int32(len(m.paths))
		m.paths = append(m.paths, p)
		m.contents = append(m.contents, c)
		clear(seen)
		for i := 0; i+3 <= len(c); i++ {
			t := tri(c[i:])
			if _, ok := seen[t]; ok {
				continue
			}
			seen[t] = struct{}{}
			m.post[t] = append(m.post[t], id)
		}
	}
	return m, rows.Err()
}

func tri(s string) uint32 { return uint32(s[0])<<16 | uint32(s[1])<<8 | uint32(s[2]) }

func (m *memSearcher) search(q query, k int) (stats, string, error) {
	var v matcher
	var lits []string
	switch q.Kind {
	case "words":
		return m.sql.words(q.Q, k)
	case "literal":
		v = literalMatcher(q.Q)
		if utf8.RuneCountInString(q.Q) >= 3 {
			lits = []string{q.Q}
		}
	case "regex":
		re, err := regexp.Compile("(?m)" + q.Q)
		if err != nil {
			return stats{}, "", err
		}
		v = regexMatcher{re}
		if lits, err = requiredLiterals(q.Q); err != nil {
			return stats{}, "", err
		}
	}

	var cand []int32
	strategy := "mem trigram"
	if len(lits) == 0 {
		strategy = "mem full scan"
		cand = make([]int32, len(m.paths))
		for i := range cand {
			cand[i] = int32(i)
		}
	} else {
		first := true
		for _, l := range lits {
			for i := 0; i+3 <= len(l); i++ {
				p := m.post[tri(l[i:])]
				if first {
					cand, first = slices.Clone(p), false
				} else {
					cand = intersect(cand, p)
				}
			}
		}
	}

	var st stats
	for _, id := range cand {
		st.candidates++
		lim := 0
		if k > 0 {
			lim = k - st.hits
		}
		if n := v.count(m.contents[id], lim); n > 0 {
			st.files++
			st.hits += n
		}
		if k > 0 && st.hits >= k {
			break
		}
	}
	return st, strategy, nil
}

func intersect(a, b []int32) []int32 {
	out := a[:0]
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] < b[j]:
			i++
		case a[i] > b[j]:
			j++
		default:
			out = append(out, a[i])
			i++
			j++
		}
	}
	return out
}
