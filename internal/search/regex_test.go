package search

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/textfile"
)

func TestRegexMatch(t *testing.T) {
	tests := []struct {
		pattern string
		want    string
	}{
		{`ErrInv`, `"err" AND "rri" AND "rin" AND "inv"`},
		{`foo.*bar`, `("foo") AND ("bar")`},
		{`^func\s+Withdraw\(`, `("fun" AND "unc") AND ("wit" AND "ith" AND "thd" AND "hdr" AND "dra" AND "raw" AND "aw(")`},
		{`abc|xyz`, `("abc") OR ("xyz")`},
		{`abc|x`, ``},
		{`colou?r`, `("col" AND "olo" AND "lor") OR ("col" AND "olo" AND "lou" AND "our")`},
		{`[Ff]oo`, `"foo"`},
		{`(?i)FOO`, `"foo"`},
		{`ba[rz]qux`, `("bar" AND "arq" AND "rqu" AND "qux") OR ("baz" AND "azq" AND "zqu" AND "qux")`},
		{`[a-z]+Error`, `"err" AND "rro" AND "ror"`},
		{`(abc)+`, `"abc"`},
		{`(abc)*`, ``},
		{`a.c`, ``},
		{`\d{3}`, ``},
		{`x{2}yz`, `"xxy" AND "xyz"`},
		{`"quoted"`, `"""qu" AND "quo" AND "uot" AND "ote" AND "ted" AND "ed"""`},
	}
	for _, tt := range tests {
		got, err := RegexMatch(tt.pattern)
		if err != nil {
			t.Fatalf("%q: %v", tt.pattern, err)
		}
		if got != tt.want {
			t.Errorf("RegexMatch(%q) =\n  %s\nwant\n  %s", tt.pattern, got, tt.want)
		}
	}
	if _, err := RegexMatch(`a(b`); !outcome.Is(err, outcome.Usage) {
		t.Errorf("invalid regex: %v", err)
	}
}

var regexFixture = map[string]string{
	".lino/config":      "",
	"wallet/service.go": "package wallet\n\nfunc Withdraw(ctx context.Context, id, amt int64) error {\n\tif amt <= 0 {\n\t\treturn ErrInvalidAmount\n\t}\n\treturn nil\n}\n",
	"wallet/errors.go":  "package wallet\n\nvar (\n\tErrInvalidAmount = errors.New(\"invalid amount\")\n\tErrTimeout = errors.New(\"timeout\")\n)\n",
	"docs/notes.md":     "# Notes\nThe colour and the color.\nFOO foo Foo fOo\nbarqux bazqux baxqux\n",
	"crlf.txt":          "\xef\xbb\xbfErrInvalidAmount first\r\nsecond\r\nabc123 end",
	"bin.dat":           "ErrInvalidAmount\x00binary",
	"unicode.txt":       "привет мир\nçà et là\nМИР\n",
}

func bruteForce(t *testing.T, root string, re *regexp.Regexp) []Hit {
	t.Helper()
	hits := []Hit{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if d.IsDir() {
			if d.Name() == ".lino" {
				return filepath.SkipDir
			}
			return nil
		}
		b, _ := os.ReadFile(p)
		if textfile.Classify(b) == textfile.Binary {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		EachLine(string(b), func(n int, line string) bool {
			if re.MatchString(line) {
				hits = append(hits, Hit{Path: filepath.ToSlash(rel), Line: n, Text: line})
			}
			return true
		})
		return nil
	})
	return hits
}

func TestRegexBruteForce(t *testing.T) {
	root := newRoot(t, regexFixture)
	ws, err := filecmd.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := OpenIndex(context.Background(), ws)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	patterns := []string{
		`ErrInvalidAmount`, `Err[A-Z]\w+`, `^\s*return\s`, `colou?r`, `(?i)foo`, `[Ff]oo`, `FOO|МИР`,
		`ba[rz]qux`, `ba.qux`, `\d+`, `^$`, `end$`, `(?i)мир`, `errors\.New\("(invalid|timeout)`,
		`amt\s*<=\s*0`, `nothing-matches-this`, `x*`, `(abc)+\d`, `[^a-z]{3}`, `^package wallet$`,
	}
	for _, p := range patterns {
		t.Run(p, func(t *testing.T) {
			re := regexp.MustCompile(p)
			got, err := Regex(context.Background(), db, p, Options{})
			if err != nil {
				t.Fatal(err)
			}
			want := bruteForce(t, root, re)
			if !reflect.DeepEqual(got.Hits, want) {
				t.Errorf("hits (%s)\n got %v\nwant %v", got.Source, got.Hits, want)
			}
		})
	}
}

func TestSearchRegexCommand(t *testing.T) {
	root := newRoot(t, regexFixture)
	tests := []struct {
		name     string
		args     []string
		code     int
		stdoutIn string
		stderrIn string
	}{
		{"alternation", []string{`Err(Timeout|InvalidAmount) =`, "--regex"}, 0, "4   ErrInvalidAmount = errors", ""},
		{"index source", []string{`func\s+Withdraw`, "--regex"}, 0, "1 hit in 1 file (index)", ""},
		{"scan source", []string{`\d+`, "--regex"}, 0, "(scan)", "regex has no literal run"},
		{"anchors", []string{`^package`, "--regex"}, 0, "2 hits in 2 files", ""},
		{"invalid", []string{`a(b`, "--regex"}, 2, "", "invalid regex"},
		{"no hits", []string{`zz[0-9]zz`, "--regex"}, 0, "0 hits", "no hits"},
		{"with words", []string{`abc`, "--regex", "--words"}, 2, "", "exclusive"},
		{"literal is not regex", []string{`a.c`}, 0, "0 hits", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errOut, code := run(t, root, append([]string{"search"}, tt.args...)...)
			if code != tt.code || !strings.Contains(out, tt.stdoutIn) || !strings.Contains(errOut, tt.stderrIn) {
				t.Errorf("code=%d stdout=%q stderr=%q", code, out, errOut)
			}
		})
	}
}
