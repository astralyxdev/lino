package search

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/cli"
)

var fixture = map[string]string{
	".lino/config":      "",
	"wallet/service.go": "package wallet\n\nfunc Withdraw(ctx context.Context, id, amt int64) error {\n\tif amt <= 0 {\n\t\treturn ErrInvalidAmount\n\t}\n\treturn nil\n}\n",
	"wallet/errors.go":  "package wallet\n\nvar (\n\tErrInvalidAmount = errors.New(\"invalid amount\")\n)\n",
	"docs/notes.md":     "# Notes\nerrinvalidamount in lower case\nuse a[0] + b.* (x) \"quoted\" 100% $HOME\n",
	"crlf.txt":          "\xef\xbb\xbfErrInvalidAmount first\r\nsecond\r\nErrInvalidAmount third",
	"bin.dat":           "ErrInvalidAmount\x00binary",
	"unicode.txt":       "привет мир\nçà et là\n",
}

func newRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, s := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func run(t *testing.T, cwd string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var o, e bytes.Buffer
	code = cli.Default.Main(context.Background(), args, cli.Env{Cwd: cwd, Stdin: strings.NewReader("")}, &o, &e)
	return o.String(), e.String(), code
}

func TestSearchText(t *testing.T) {
	root := newRoot(t, fixture)
	tests := []struct {
		name   string
		query  string
		code   int
		stdout string
		stderr string
	}{
		{"across files", "ErrInvalidAmount", 0,
			"crlf.txt\n  1   ErrInvalidAmount first\n  3   ErrInvalidAmount third\n" +
				"wallet/errors.go\n  4   ErrInvalidAmount = errors.New(\"invalid amount\")\n" +
				"wallet/service.go\n  5   return ErrInvalidAmount\n" +
				"4 hits in 3 files (index)\n", ""},
		{"one hit", "func Withdraw", 0,
			"wallet/service.go\n  3   func Withdraw(ctx context.Context, id, amt int64) error {\n1 hit in 1 file (index)\n", ""},
		{"no hits", "NoSuchIdentifier", 0, "0 hits in 0 files (index)\n", "no hits\n"},
		{"case sensitive", "errinvalidamount", 0,
			"docs/notes.md\n  2   errinvalidamount in lower case\n1 hit in 1 file (index)\n", ""},
		{"special chars", `a[0] + b.* (x) "quoted" 100% $HOME`, 0,
			"docs/notes.md\n  3   use a[0] + b.* (x) \"quoted\" 100% $HOME\n1 hit in 1 file (index)\n", ""},
		{"fts syntax", `"quoted"`, 0,
			"docs/notes.md\n  3   use a[0] + b.* (x) \"quoted\" 100% $HOME\n1 hit in 1 file (index)\n", ""},
		{"fts operators", "AND OR NOT", 0, "0 hits in 0 files (index)\n", "no hits\n"},
		{"unicode", "мир", 0, "unicode.txt\n  1   привет мир\n1 hit in 1 file (index)\n", ""},
		{"short query scans", "là", 0, "unicode.txt\n  2   çà et là\n1 hit in 1 file (scan)\n",
			"query shorter than 3 characters: scanned all indexed files\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errOut, code := run(t, root, "search", tt.query)
			if code != tt.code || out != tt.stdout || errOut != tt.stderr {
				t.Errorf("code=%d\nstdout:\n%s\nstderr:\n%s\nwant code=%d\nstdout:\n%s\nstderr:\n%s",
					code, out, errOut, tt.code, tt.stdout, tt.stderr)
			}
		})
	}
}

func TestSearchJSON(t *testing.T) {
	root := newRoot(t, fixture)
	tests := []struct {
		query   string
		outcome string
		hits    int
	}{
		{"ErrInvalidAmount", "ok", 4},
		{"NoSuchIdentifier", "empty", 0},
	}
	for _, tt := range tests {
		out, _, code := run(t, filepath.Join(root, "wallet"), "search", tt.query, "--json")
		if code != 0 {
			t.Fatalf("%s: code %d: %s", tt.query, code, out)
		}
		var env struct {
			OK      bool   `json:"ok"`
			Outcome string `json:"outcome"`
			Data    Data   `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatal(err)
		}
		if !env.OK || env.Outcome != tt.outcome || len(env.Data.Hits) != tt.hits || env.Data.Query != tt.query {
			t.Errorf("%s: got %+v", tt.query, env)
		}
		if last := len(env.Data.Hits) - 1; tt.hits > 0 && !reflect.DeepEqual(env.Data.Hits[last], Hit{Path: "wallet/service.go", Line: 5, Text: "\t\treturn ErrInvalidAmount"}) {
			t.Errorf("json keeps indentation: got %+v", env.Data.Hits)
		}
	}
}

func TestSearchErrors(t *testing.T) {
	root := newRoot(t, fixture)
	tests := []struct {
		name  string
		cwd   string
		args  []string
		code  int
		errIn string
	}{
		{"no query", root, []string{"search"}, 2, ""},
		{"empty query", root, []string{"search", ""}, 2, ""},
		{"two queries", root, []string{"search", "a", "b"}, 2, ""},
		{"not initialised", t.TempDir(), []string{"search", "abc"}, 8, ""},
		{"anchors", root, []string{"search", "Withdraw", "--anchors"}, 2, "lino read <file> --lines A:B --anchors"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, errOut, code := run(t, tt.cwd, tt.args...)
			if code != tt.code || !strings.Contains(errOut, tt.errIn) {
				t.Errorf("code %d, want %d (%s)", code, tt.code, errOut)
			}
		})
	}
}

func TestSearchLimit(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 30; i++ {
		b.WriteString("needle\n")
	}
	root := newRoot(t, map[string]string{".lino/config": "", "a.txt": b.String(), "b.txt": "needle\n"})
	out, errOut, code := run(t, root, "search", "needle")
	if code != 0 || !strings.HasSuffix(out, "20 hits in 1 file (index)\n") || !strings.Contains(errOut, "truncated at 20 hits") {
		t.Errorf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
}

func TestTrigramMatch(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"ab", ""},
		{"abc", `"abc"`},
		{"AbCd", `"abc" AND "bcd"`},
		{`a"b`, `"a""b"`},
		{"aaaa", `"aaa"`},
		{"мир!", `"мир" AND "ир!"`},
	}
	for _, tt := range tests {
		if got := TrigramMatch(tt.in); got != tt.want {
			t.Errorf("TrigramMatch(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if n := strings.Count(TrigramMatch(strings.Repeat("abcdefghij", 10)), " AND "); n+1 > maxTrigrams {
		t.Errorf("long literal: %d terms", n+1)
	}
}

func TestEachLine(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a\n", []string{"a"}},
		{"a\n\nb", []string{"a", "", "b"}},
		{"\uFEFFa\r\nb\r\n", []string{"a", "b"}},
	}
	for _, tt := range tests {
		var got []string
		EachLine(tt.in, func(n int, l string) bool {
			if n != len(got)+1 {
				t.Errorf("%q: line number %d", tt.in, n)
			}
			got = append(got, l)
			return true
		})
		if strings.Join(got, "|") != strings.Join(tt.want, "|") || len(got) != len(tt.want) {
			t.Errorf("EachLine(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
