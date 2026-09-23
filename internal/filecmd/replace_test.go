package filecmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/version"
)

func replaceRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files[".lino/config"] = ""
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

func replaceRun(t *testing.T, root, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var o, e bytes.Buffer
	code = cli.Default.Main(context.Background(), append([]string{"replace"}, args...),
		cli.Env{Cwd: root, Stdin: strings.NewReader(stdin)}, &o, &e)
	return o.String(), e.String(), code
}

func TestReplace(t *testing.T) {
	const src = "func A() {\n\treturn 1\n}\n\nfunc B() {\n\treturn 1\n}\n"
	const crlf = "\xef\xbb\xbfa\r\nold\r\nc\r\n"
	tests := []struct {
		name    string
		file    string
		content string
		stdin   string
		args    []string
		staleV  bool
		code    int
		want    string // file content after
		outPart string
		errPart string
	}{
		{name: "unique", file: "a.go", content: src,
			stdin: "func B() {\n\treturn 1\n<<<lino>>>\nfunc B() {\n\treturn 2\n",
			code:  0, want: strings.Replace(src, "B() {\n\treturn 1", "B() {\n\treturn 2", 1),
			outPart: "lines 5-6"},
		{name: "grow", file: "a.go", content: "x\ny\nz\n", stdin: "y\n<<<lino>>>\ny1\ny2\ny3\n",
			code: 0, want: "x\ny1\ny2\ny3\nz\n", outPart: "lines 2-4"},
		{name: "empty new block deletes", file: "a.go", content: "x\ny\nz\n", stdin: "y\n<<<lino>>>\n",
			code: 0, want: "x\nz\n", outPart: "removed lines 2-2"},
		{name: "keeps crlf and bom", file: "w.txt", content: crlf, stdin: "old\n<<<lino>>>\nnew\n",
			code: 0, want: "\xef\xbb\xbfa\r\nnew\r\nc\r\n"},
		{name: "crlf stdin", file: "a.go", content: "x\ny\n", stdin: "y\r\n<<<lino>>>\r\nz\r\n",
			code: 0, want: "x\nz\n"},
		{name: "custom sep", file: "a.go", content: "a\n<<<lino>>>\nb\n", stdin: "<<<lino>>>\n---\n<<<LINO>>>\n",
			args: []string{"--sep", "---"}, code: 0, want: "a\n<<<LINO>>>\nb\n"},
		{name: "not found", file: "a.go", content: src, stdin: "return 1\n<<<lino>>>\nreturn 2\n",
			code: 3, want: src, errPart: "not_found"},
		{name: "partial line is not a match", file: "a.go", content: "abc\n", stdin: "b\n<<<lino>>>\nx\n",
			code: 3, want: "abc\n"},
		{name: "ambiguous", file: "a.go", content: src, stdin: "\treturn 1\n}\n<<<lino>>>\n\treturn 2\n}\n",
			code: 5, want: src, outPart: "lines 2-3\nlines 6-7\n", errPart: "matches 2 times"},
		{name: "missing sep", file: "a.go", content: src, stdin: "func A() {\n",
			code: 2, want: src, errPart: "no separator"},
		{name: "default sep with custom sep set", file: "a.go", content: src, stdin: "func A() {\n<<<lino>>>\nx\n",
			args: []string{"--sep", "==="}, code: 2, want: src},
		{name: "sep twice", file: "a.go", content: src, stdin: "a\n<<<lino>>>\nb\n<<<lino>>>\n",
			code: 2, want: src, errPart: "--sep"},
		{name: "empty old", file: "a.go", content: src, stdin: "<<<lino>>>\nx\n", code: 2, want: src},
		{name: "empty stdin", file: "a.go", content: src, stdin: "", code: 2, want: src},
		{name: "stale v", file: "a.go", content: src, stdin: "func A() {\n<<<lino>>>\nfunc AA() {\n",
			staleV: true, code: 6, want: src, errPart: "conflict"},
		{name: "unchanged", file: "a.go", content: "x\n", stdin: "x\n<<<lino>>>\nx\n", code: 0, want: "x\n",
			outPart: "unchanged"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := replaceRoot(t, map[string]string{tt.file: tt.content})
			v := version.Of([]byte(tt.content))
			if tt.staleV {
				v = version.Of([]byte("something else"))
			}
			args := append([]string{tt.file, "--v", v}, tt.args...)
			out, errOut, code := replaceRun(t, root, tt.stdin, args...)
			if code != tt.code {
				t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", code, tt.code, out, errOut)
			}
			got, _ := os.ReadFile(filepath.Join(root, tt.file))
			if string(got) != tt.want {
				t.Errorf("file = %q, want %q", got, tt.want)
			}
			if !strings.Contains(out, tt.outPart) {
				t.Errorf("stdout %q lacks %q", out, tt.outPart)
			}
			if !strings.Contains(errOut, tt.errPart) {
				t.Errorf("stderr %q lacks %q", errOut, tt.errPart)
			}
			if code == 0 && !strings.Contains(out, "unchanged") {
				wantHead := "updated " + tt.file + " v=" + v + "→" + version.Of(got)
				if !strings.HasPrefix(out, wantHead) {
					t.Errorf("stdout %q, want prefix %q", out, wantHead)
				}
			}
		})
	}
}

func TestReplaceMissingV(t *testing.T) {
	root := replaceRoot(t, map[string]string{"a.go": "x\n"})
	_, errOut, code := replaceRun(t, root, "x\n<<<lino>>>\ny\n", "a.go")
	if code != 6 || !strings.Contains(errOut, "--v is required") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

func TestReplaceJSON(t *testing.T) {
	root := replaceRoot(t, map[string]string{"a.go": "x\ny\nx\ny\n"})
	v := version.Of([]byte("x\ny\nx\ny\n"))
	out, _, code := replaceRun(t, root, "x\ny\n<<<lino>>>\nz\n", "a.go", "--v", v, "--json")
	if code != 5 {
		t.Fatalf("code = %d", code)
	}
	for _, want := range []string{`"outcome":"ambiguous"`, `"candidates":["lines 1-2","lines 3-4"]`} {
		if !strings.Contains(out, want) {
			t.Errorf("json %s lacks %s", out, want)
		}
	}
}
