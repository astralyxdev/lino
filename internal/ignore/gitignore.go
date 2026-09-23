package ignore

import (
	"bytes"
	"os"
	"path"
	"sort"
	"strings"
)

// Pattern is one parsed line of an ignore file.
type Pattern struct {
	Base     string // directory of the ignore file, root-relative, "" for the root
	Negate   bool
	DirOnly  bool
	Anchored bool // contains a slash: matched against the path relative to Base
	Glob     string
}

// ParseLine parses one ignore-file line. ok is false for blanks and comments.
func ParseLine(base, line string) (Pattern, bool) {
	line = strings.TrimSuffix(line, "\r")
	line = trimTrailingSpaces(line)
	if line == "" || line[0] == '#' {
		return Pattern{}, false
	}
	p := Pattern{Base: strings.Trim(base, "/")}
	if line[0] == '!' {
		p.Negate = true
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		p.DirOnly = true
		line = strings.TrimRight(line, "/")
	}
	if line == "" {
		return Pattern{}, false
	}
	if strings.Contains(line, "/") {
		p.Anchored = true
		line = strings.TrimPrefix(line, "/")
	}
	p.Glob = line
	return p, true
}

// trimTrailingSpaces drops unescaped trailing spaces, as git does.
func trimTrailingSpaces(s string) string {
	end := len(s)
	for end > 0 && s[end-1] == ' ' {
		bs := 0
		for i := end - 2; i >= 0 && s[i] == '\\'; i-- {
			bs++
		}
		if bs%2 == 1 {
			break
		}
		end--
	}
	return s[:end]
}

// Parse parses the contents of an ignore file located in directory base.
func Parse(base string, content []byte) []Pattern {
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	var out []Pattern
	for _, line := range strings.Split(string(content), "\n") {
		if p, ok := ParseLine(base, line); ok {
			out = append(out, p)
		}
	}
	return out
}

// match reports whether p matches rel, a slash-separated root-relative path.
func (p *Pattern) match(rel string, isDir bool) bool {
	if p.DirOnly && !isDir {
		return false
	}
	sub := rel
	if p.Base != "" {
		if !strings.HasPrefix(rel, p.Base+"/") {
			return false
		}
		sub = rel[len(p.Base)+1:]
	}
	if p.Anchored {
		return wildmatch(p.Glob, sub)
	}
	return wildmatch(p.Glob, path.Base(sub))
}

// Matcher holds patterns from any number of ignore files. Patterns from deeper
// directories take precedence; within one file, the last matching line wins.
type Matcher struct {
	patterns []Pattern
}

// New returns an empty matcher.
func New() *Matcher { return &Matcher{} }

// Add appends patterns, keeping them ordered by base depth.
func (m *Matcher) Add(ps ...Pattern) {
	m.patterns = append(m.patterns, ps...)
	sort.SliceStable(m.patterns, func(i, j int) bool {
		return depth(m.patterns[i].Base) < depth(m.patterns[j].Base)
	})
}

// AddFile parses content as an ignore file in directory base and adds it.
func (m *Matcher) AddFile(base string, content []byte) {
	m.Add(Parse(base, content)...)
}

// LoadFile reads fsPath as an ignore file for base. A missing file is not an error.
func (m *Matcher) LoadFile(base, fsPath string) error {
	b, err := os.ReadFile(fsPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	m.AddFile(base, b)
	return nil
}

func depth(base string) int {
	if base == "" {
		return 0
	}
	return strings.Count(base, "/") + 1
}

// MatchSelf reports whether rel itself is ignored, ignoring its parents.
// Tree walkers that skip ignored directories should use this.
func (m *Matcher) MatchSelf(rel string, isDir bool) bool {
	for i := len(m.patterns) - 1; i >= 0; i-- {
		if m.patterns[i].match(rel, isDir) {
			return !m.patterns[i].Negate
		}
	}
	return false
}

// Match reports whether rel is ignored, either itself or because one of its
// parent directories is ignored (git cannot re-include inside an excluded dir).
func (m *Matcher) Match(rel string, isDir bool) bool {
	rel = strings.Trim(rel, "/")
	if rel == "" || rel == "." {
		return false
	}
	for i := 0; i < len(rel); i++ {
		if rel[i] == '/' && m.MatchSelf(rel[:i], true) {
			return true
		}
	}
	return m.MatchSelf(rel, isDir)
}
