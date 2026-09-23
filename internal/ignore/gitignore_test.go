package ignore

import (
	"os"
	"path/filepath"
	"testing"
)

// Cases from git's t/t3070-wildmatch.sh, "wildmatch" column (WM_PATHNAME).
var wildmatchCases = []struct {
	match   bool
	text    string
	pattern string
}{
	{true, "foo", "foo"},
	{false, "foo", "bar"},
	{true, "", ""},
	{true, "foo", "???"},
	{false, "foo", "??"},
	{true, "foo", "*"},
	{true, "foo", "f*"},
	{false, "foo", "*f"},
	{true, "foo", "*foo*"},
	{true, "foobar", "*ob*a*r*"},
	{true, "aaaaaaabababab", "*ab"},
	{true, "foo*", `foo\*`},
	{false, "foobar", `foo\*bar`},
	{true, `f\oo`, `f\\oo`},
	{true, "ball", "*[al]?"},
	{false, "ten", "[ten]"},
	{true, "ten", "**[!te]"},
	{false, "ten", "**[!ten]"},
	{true, "ten", "t[a-g]n"},
	{false, "ten", "t[!a-g]n"},
	{true, "ton", "t[!a-g]n"},
	{true, "ton", "t[^a-g]n"},
	{true, "a]b", "a[]]b"},
	{true, "a-b", "a[]-]b"},
	{true, "a]b", "a[]-]b"},
	{false, "aab", "a[]-]b"},
	{true, "aab", "a[]a-]b"},
	{true, "]", "]"},

	{false, "foo/baz/bar", "foo*bar"},
	{false, "foo/baz/bar", "foo**bar"},
	{true, "foobazbar", "foo**bar"},
	{true, "foo/baz/bar", "foo/**/bar"},
	{true, "foo/baz/bar", "foo/**/**/bar"},
	{true, "foo/b/a/z/bar", "foo/**/bar"},
	{true, "foo/b/a/z/bar", "foo/**/**/bar"},
	{true, "foo/bar", "foo/**/bar"},
	{true, "foo/bar", "foo/**/**/bar"},
	{false, "foo/bar", "foo?bar"},
	{false, "foo/bar", "foo[/]bar"},
	{false, "foo/bar", "foo[^a-z]bar"},
	{false, "foo/bar", "f[^eiu][^eiu][^eiu][^eiu][^eiu]r"},
	{true, "foo-bar", "f[^eiu][^eiu][^eiu][^eiu][^eiu]r"},
	{true, "foo", "**/foo"},
	{true, "XXX/foo", "**/foo"},
	{true, "bar/baz/foo", "**/foo"},
	{false, "bar/baz/foo", "*/foo"},
	{false, "foo/bar/baz", "**/bar*"},
	{true, "deep/foo/bar/baz", "**/bar/*"},
	{false, "deep/foo/bar/baz/", "**/bar/*"},
	{true, "deep/foo/bar/baz/", "**/bar/**"},
	{false, "deep/foo/bar", "**/bar/*"},
	{true, "deep/foo/bar/", "**/bar/**"},
	{false, "foo/bar/baz", "**/bar**"},
	{true, "foo/bar/baz/x", "*/bar/**"},
	{false, "deep/foo/bar/baz/x", "*/bar/**"},
	{true, "deep/foo/bar/baz/x", "**/bar/*/*"},

	{false, "acrt", "a[c-c]st"},
	{true, "acrt", "a[c-c]rt"},
	{false, "]", "[!]-]"},
	{true, "a", "[!]-]"},
	{false, "", `\`},
	{false, `\`, `\`},
	{false, "XXX/\\", `*/\`},
	{true, "XXX/\\", `*/\\`},
	{true, "foo", "foo"},
	{true, "@foo", "@foo"},
	{false, "foo", "@foo"},
	{true, "[ab]", `\[ab]`},
	{true, "[ab]", "[[]ab]"},
	{true, "[ab]", "[[:]ab]"},
	{false, "[ab]", "[[::]ab]"},
	{true, "[ab]", "[[:digit]ab]"},
	{true, "[ab]", `[\[:]ab]`},
	{true, "?a?b", `\??\?b`},
	{true, "abc", `\a\b\c`},
	{false, "foo", ""},
	{true, "foo/bar/baz/to", "**/t[o]"},

	{true, "a1B", "[[:alpha:]][[:digit:]][[:upper:]]"},
	{false, "a", "[[:digit:][:upper:][:space:]]"},
	{true, "A", "[[:digit:][:upper:][:space:]]"},
	{true, "1", "[[:digit:][:upper:][:space:]]"},
	{false, "1", "[[:digit:][:upper:][:spaci:]]"},
	{true, " ", "[[:digit:][:upper:][:space:]]"},
	{false, ".", "[[:digit:][:upper:][:space:]]"},
	{true, ".", "[[:digit:][:punct:][:space:]]"},
	{true, "5", "[[:xdigit:]]"},
	{true, "f", "[[:xdigit:]]"},
	{true, "D", "[[:xdigit:]]"},
	{true, "_", "[[:alnum:][:alpha:][:blank:][:cntrl:][:digit:][:graph:][:lower:][:print:][:punct:][:space:][:upper:][:xdigit:]]"},
	{true, ".", "[^[:alnum:][:alpha:][:blank:][:cntrl:][:digit:][:lower:][:space:][:upper:][:xdigit:]]"},
	{true, "5", "[a-c[:digit:]x-z]"},
	{true, "b", "[a-c[:digit:]x-z]"},
	{true, "y", "[a-c[:digit:]x-z]"},
	{false, "q", "[a-c[:digit:]x-z]"},

	{true, "]", `[\\-^]`},
	{false, "[", `[\\-^]`},
	{true, "-", `[\-_]`},
	{true, "]", `[\]]`},
	{false, `\]`, `[\]]`},
	{false, `\`, `[\]]`},
	{false, "ab", "a[]b"},
	{false, "a[]b", "a[]b"},
	{false, "ab[", "ab["},
	{false, "ab", "[!"},
	{false, "ab", "[-"},
	{true, "-", "[-]"},
	{false, "-", "[a-"},
	{false, "-", "[!a-"},
	{true, "-", "[--A]"},
	{true, "5", "[--A]"},
	{true, " ", "[ --]"},
	{true, "$", "[ --]"},
	{true, "-", "[ --]"},
	{false, "0", "[ --]"},
	{true, "-", "[---]"},
	{true, "-", "[------]"},
	{false, "j", "[a-e-n]"},
	{true, "-", "[a-e-n]"},
	{true, "a", "[!------]"},
	{false, "[", "[]-a]"},
	{true, "^", "[]-a]"},
	{false, "^", "[!]-a]"},
	{true, "[", "[!]-a]"},
	{true, "^", "[a^bc]"},
	{true, "-b]", "[a-]b]"},
	{false, `\`, `[\]`},
	{true, `\`, `[\\]`},
	{false, `\`, `[!\\]`},
	{true, "G", `[A-\\]`},
	{false, "aaabbb", "b*a"},
	{false, "aabcaa", "*ba*"},
	{true, ",", "[,]"},
	{true, ",", `[\\,]`},
	{true, `\`, `[\\,]`},
	{true, "-", "[,-.]"},
	{false, "+", "[,-.]"},
	{false, "-.]", "[,-.]"},
	{true, "2", `[\1-\3]`},
	{true, "3", `[\1-\3]`},
	{false, "4", `[\1-\3]`},
	{true, `\`, `[[-\]]`},
	{true, "[", `[[-\]]`},
	{true, "]", `[[-\]]`},
	{false, "-", `[[-\]]`},

	{true, "-adobe-courier-bold-o-normal--12-120-75-75-m-70-iso8859-1", "-*-*-*-*-*-*-12-*-*-*-m-*-*-*"},
	{false, "-adobe-courier-bold-o-normal--12-120-75-75-X-70-iso8859-1", "-*-*-*-*-*-*-12-*-*-*-m-*-*-*"},
	{false, "-adobe-courier-bold-o-normal--12-120-75-75-/-70-iso8859-1", "-*-*-*-*-*-*-12-*-*-*-m-*-*-*"},
	{true, "XXX/adobe/courier/bold/o/normal//12/120/75/75/m/70/iso8859/1", "XXX/*/*/*/*/*/*/12/*/*/*/m/*/*/*"},
	{false, "XXX/adobe/courier/bold/o/normal//12/120/75/75/X/70/iso8859/1", "XXX/*/*/*/*/*/*/12/*/*/*/m/*/*/*"},
	{true, "abcd/abcdefg/abcdefghijk/abcdefghijklmnop.txt", "**/*a*b*g*n*t"},
	{false, "abcd/abcdefg/abcdefghijk/abcdefghijklmnop.txtz", "**/*a*b*g*n*t"},
	{false, "foo", "*/*/*"},
	{false, "foo/bar", "*/*/*"},
	{true, "foo/bba/arr", "*/*/*"},
	{false, "foo/bb/aa/rr", "*/*/*"},
	{true, "foo/bb/aa/rr", "**/**/**"},
	{true, "abcXdefXghi", "*X*i"},
	{false, "ab/cXd/efXg/hi", "*X*i"},
	{true, "ab/cXd/efXg/hi", "*/*X*/*/*i"},
	{true, "ab/cXd/efXg/hi", "**/*X*/**/*i"},
}

func TestWildmatch(t *testing.T) {
	for _, tt := range wildmatchCases {
		if got := wildmatch(tt.pattern, tt.text); got != tt.match {
			t.Errorf("wildmatch(%q, %q) = %v, want %v", tt.pattern, tt.text, got, tt.match)
		}
	}
}

type pathCase struct {
	path    string
	isDir   bool
	ignored bool
}

// Gitignore semantics modelled on git's t/t0008-ignores.sh and the gitignore
// documentation examples; each case was checked against git check-ignore.
var gitignoreCases = []struct {
	name  string
	files map[string]string // ignore-file dir -> content
	cases []pathCase
}{
	{
		name:  "basename anywhere",
		files: map[string]string{"": "*.log\n"},
		cases: []pathCase{
			{"a.log", false, true},
			{"x/y/a.log", false, true},
			{"a.log.txt", false, false},
			{"logs", true, false},
		},
	},
	{
		name:  "comments blanks and escapes",
		files: map[string]string{"": "# comment\n\n\\#hash\n\\!bang\ntrail   \nesc\\ \n"},
		cases: []pathCase{
			{"# comment", false, false},
			{"#hash", false, true},
			{"!bang", false, true},
			{"trail", false, true},
			{"trail   ", false, false},
			{"esc ", false, true},
			{"esc", false, false},
		},
	},
	{
		name:  "dir only",
		files: map[string]string{"": "build/\n"},
		cases: []pathCase{
			{"build", true, true},
			{"build", false, false},
			{"src/build", true, true},
			{"build/out.o", false, true},
			{"src/build/out.o", false, true},
		},
	},
	{
		name:  "anchored by leading slash",
		files: map[string]string{"": "/todo\n"},
		cases: []pathCase{
			{"todo", false, true},
			{"sub/todo", false, false},
		},
	},
	{
		name:  "anchored by middle slash",
		files: map[string]string{"": "doc/frotz\n"},
		cases: []pathCase{
			{"doc/frotz", true, true},
			{"a/doc/frotz", true, false},
			{"doc/frotz/x", false, true},
		},
	},
	{
		name:  "negation",
		files: map[string]string{"": "*.txt\n!keep.txt\n"},
		cases: []pathCase{
			{"a.txt", false, true},
			{"keep.txt", false, false},
			{"d/keep.txt", false, false},
		},
	},
	{
		name:  "cannot re-include inside excluded dir",
		files: map[string]string{"": "out/\n!out/keep\n"},
		cases: []pathCase{
			{"out/keep", false, true},
			{"out/other", false, true},
		},
	},
	{
		name:  "re-include via dir contents pattern",
		files: map[string]string{"": "/*\n!/foo\n/foo/*\n!/foo/bar\n"},
		cases: []pathCase{
			{"top", false, true},
			{"foo", true, false},
			{"foo/baz", false, true},
			{"foo/bar", true, false},
			{"foo/bar/x", false, false},
		},
	},
	{
		name:  "double star forms",
		files: map[string]string{"": "**/gen\nabc/**\na/**/b\n"},
		cases: []pathCase{
			{"gen", false, true},
			{"x/y/gen", true, true},
			{"abc", true, false},
			{"abc/x", false, true},
			{"abc/x/y", false, true},
			{"a/b", false, true},
			{"a/x/b", false, true},
			{"a/x/y/b", false, true},
			{"z/a/b", false, false},
		},
	},
	{
		name: "nested ignore files",
		files: map[string]string{
			"":    "*.o\n",
			"sub": "!keep.o\n/local\ndeep/*.tmp\n",
		},
		cases: []pathCase{
			{"a.o", false, true},
			{"sub/a.o", false, true},
			{"sub/keep.o", false, false},
			{"keep.o", false, true},
			{"sub/x/keep.o", false, false},
			{"sub/local", false, true},
			{"local", false, false},
			{"sub/x/local", false, false},
			{"sub/deep/a.tmp", false, true},
			{"deep/a.tmp", false, false},
		},
	},
	{
		name: "deeper file overrides parent",
		files: map[string]string{
			"":  "!*.gen\n",
			"a": "*.gen\n",
		},
		cases: []pathCase{
			{"a/x.gen", false, true},
			{"x.gen", false, false},
		},
	},
	{
		name:  "character classes and question mark",
		files: map[string]string{"": "file[0-9].txt\n?.c\n"},
		cases: []pathCase{
			{"file1.txt", false, true},
			{"filea.txt", false, false},
			{"a.c", false, true},
			{"ab.c", false, false},
		},
	},
}

func buildMatcher(files map[string]string) *Matcher {
	m := New()
	for base, content := range files {
		m.AddFile(base, []byte(content))
	}
	return m
}

func TestGitignore(t *testing.T) {
	for _, tc := range gitignoreCases {
		t.Run(tc.name, func(t *testing.T) {
			m := buildMatcher(tc.files)
			for _, c := range tc.cases {
				if got := m.Match(c.path, c.isDir); got != c.ignored {
					t.Errorf("Match(%q, dir=%v) = %v, want %v", c.path, c.isDir, got, c.ignored)
				}
			}
		})
	}
}

func TestParseLine(t *testing.T) {
	tests := []struct {
		line string
		ok   bool
		want Pattern
	}{
		{"", false, Pattern{}},
		{"# x", false, Pattern{}},
		{"   ", false, Pattern{}},
		{"!", false, Pattern{}},
		{"/", false, Pattern{}},
		{"foo\r", true, Pattern{Glob: "foo"}},
		{"!foo/", true, Pattern{Negate: true, DirOnly: true, Glob: "foo"}},
		{"/foo", true, Pattern{Anchored: true, Glob: "foo"}},
		{"a/b/", true, Pattern{Anchored: true, DirOnly: true, Glob: "a/b"}},
	}
	for _, tt := range tests {
		got, ok := ParseLine("", tt.line)
		if ok != tt.ok || got != tt.want {
			t.Errorf("ParseLine(%q) = %+v, %v; want %+v, %v", tt.line, got, ok, tt.want, tt.ok)
		}
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	m := New()
	if err := m.LoadFile("", filepath.Join(dir, "missing")); err != nil {
		t.Fatalf("missing file: %v", err)
	}
	f := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(f, []byte("\xef\xbb\xbf*.bak\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.LoadFile("", f); err != nil {
		t.Fatal(err)
	}
	if !m.Match("x.bak", false) {
		t.Error("BOM-prefixed pattern not applied")
	}
}
