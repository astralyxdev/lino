package main

import (
	"regexp"
	"strings"
)

// matcher counts matching lines in a file's content, stopping at lim (0 = no
// limit). Both implementations search the whole content and only then cut the
// hit's line out, so non-matching lines are never visited one by one.
type matcher interface {
	count(content string, lim int) int
}

type literalMatcher string

func (m literalMatcher) count(content string, lim int) int {
	n, pos := 0, 0
	for {
		i := strings.Index(content[pos:], string(m))
		if i < 0 {
			return n
		}
		n++
		if lim > 0 && n >= lim {
			return n
		}
		pos = lineEnd(content, pos+i)
	}
}

type regexMatcher struct{ re *regexp.Regexp }

func (m regexMatcher) count(content string, lim int) int {
	n, pos := 0, 0
	for pos < len(content) {
		loc := m.re.FindStringIndex(content[pos:])
		if loc == nil {
			return n
		}
		at := pos + loc[0]
		start := strings.LastIndexByte(content[:at], '\n') + 1
		end := lineEnd(content, at)
		// A match may span lines; only count it if its own line matches.
		if line := strings.TrimSuffix(content[start:end], "\n"); m.re.MatchString(line) {
			n++
			if lim > 0 && n >= lim {
				return n
			}
		}
		pos = end
	}
	return n
}

// lineEnd returns the offset just past the newline ending the line at i.
func lineEnd(s string, i int) int {
	if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
		return i + j + 1
	}
	return len(s)
}
