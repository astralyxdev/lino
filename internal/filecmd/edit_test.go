package filecmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/anchor"
	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/version"
)

func runEdit(t *testing.T, cwd, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var o, e bytes.Buffer
	code = cli.Default.Main(context.Background(), args,
		cli.Env{Cwd: cwd, Stdin: strings.NewReader(stdin), Getenv: func(string) string { return "" }}, &o, &e)
	return o.String(), e.String(), code
}

func TestEdit(t *testing.T) {
	const five = "one\ntwo\nthree\nfour\nfive\n"
	anc := func(content string, n int) string {
		lines := strings.Split(content, "\n")
		return anchor.Of(n, strings.TrimSuffix(lines[n-1], "\r")).String()
	}
	tests := []struct {
		name       string
		content    string
		start, end func(c string) string
		stdin      string
		v          func(c string) string // default: version of content
		wantCode   int
		want       string // file after; default: unchanged
		wantOut    string
		wantErr    string
	}{
		{
			name: "anchored", content: five,
			start: func(c string) string { return anc(c, 2) }, end: func(c string) string { return anc(c, 3) },
			stdin: "TWO\nTHREE\n", want: "one\nTWO\nTHREE\nfour\nfive\n", wantOut: "lines 2-3",
		},
		{
			name: "plain numbers", content: five,
			start: editLit("4"), end: editLit("4"),
			stdin: "FOUR\n", want: "one\ntwo\nthree\nFOUR\nfive\n", wantOut: "lines 4-4",
		},
		{
			name: "grow", content: five,
			start: editLit("2"), end: editLit("2"),
			stdin: "a\nb\nc\n", want: "one\na\nb\nc\nthree\nfour\nfive\n", wantOut: "lines 2-4",
		},
		{
			name: "shrink", content: five,
			start: editLit("1"), end: editLit("4"),
			stdin: "x\n", want: "x\nfive\n", wantOut: "lines 1-1",
		},
		{
			name: "no trailing newline on stdin", content: five,
			start: editLit("5"), end: editLit("5"),
			stdin: "FIVE", want: "one\ntwo\nthree\nfour\nFIVE\n",
		},
		{
			name: "crlf file keeps crlf and bom", content: "\xef\xbb\xbfa\r\nb\r\nc\r\n",
			start: func(c string) string { return anc(strings.TrimPrefix(c, "\xef\xbb\xbf"), 2) }, end: editLit("2"),
			stdin: "B1\nB2\n", want: "\xef\xbb\xbfa\r\nB1\r\nB2\r\nc\r\n",
		},
		{
			name: "no final newline kept", content: "a\nb",
			start: editLit("2"), end: editLit("2"),
			stdin: "B\n", want: "a\nB",
		},
		{
			name: "empty stdin", content: five,
			start: editLit("1"), end: editLit("1"), stdin: "", wantCode: 2, wantErr: "no content on stdin",
		},
		{
			name: "missing v", content: five,
			start: editLit("1"), end: editLit("1"), stdin: "x\n", v: editLit(""), wantCode: 6, wantErr: "--v is required",
		},
		{
			name: "stale v", content: five,
			start: editLit("1"), end: editLit("1"), stdin: "x\n", v: editLit("000000"), wantCode: 6, wantErr: "changed since",
		},
		{
			name: "stale anchor", content: five,
			start: editLit("2:zzz"), end: editLit("2:zzz"), stdin: "x\n", wantCode: 4,
		},
		{
			name: "past end", content: five,
			start: editLit("5"), end: editLit("9"), stdin: "x\n", wantCode: 4,
		},
		{
			name: "end before start", content: five,
			start: editLit("3"), end: editLit("2"), stdin: "x\n", wantCode: 2,
		},
		{
			name: "bad anchor", content: five,
			start: editLit("x"), end: editLit("2"), stdin: "x\n", wantCode: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFiles(t, root, map[string]string{".lino/config": "", "f.txt": tt.content})
			v := version.Of([]byte(tt.content))
			if tt.v != nil {
				v = tt.v(tt.content)
			}
			args := []string{"edit", "f.txt", tt.start(tt.content), tt.end(tt.content)}
			if v != "" {
				args = append(args, "--v", v)
			}
			out, errOut, code := runEdit(t, root, tt.stdin, args...)
			if code != tt.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tt.wantCode, out, errOut)
			}
			got, err := os.ReadFile(filepath.Join(root, "f.txt"))
			if err != nil {
				t.Fatal(err)
			}
			want := tt.want
			if want == "" {
				want = tt.content
			}
			if string(got) != want {
				t.Errorf("file = %q, want %q", got, want)
			}
			if tt.wantCode == 0 {
				head := "updated f.txt v=" + version.Of([]byte(tt.content)) + "→" + version.Of(got) + " lines "
				if !strings.HasPrefix(out, head) {
					t.Errorf("stdout %q, want prefix %q", out, head)
				}
			}
			if !strings.Contains(out, tt.wantOut) {
				t.Errorf("stdout %q missing %q", out, tt.wantOut)
			}
			if !strings.Contains(errOut, tt.wantErr) {
				t.Errorf("stderr %q missing %q", errOut, tt.wantErr)
			}
		})
	}
}

func TestEditRelocatesAfterInsertAbove(t *testing.T) {
	root := t.TempDir()
	orig := "a\nb\nc\n"
	writeFiles(t, root, map[string]string{".lino/config": "", "f.txt": "new\n" + orig})
	// Anchor from the original read, v of the current file.
	start := anchor.Of(2, "b").String()
	cur := version.Of([]byte("new\n" + orig))
	out, errOut, code := runEdit(t, root, "B\n", "edit", "f.txt", start, start, "--v", cur)
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	got, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(got) != "new\na\nB\nc\n" || !strings.Contains(out, "lines 3-3") {
		t.Fatalf("file %q, out %q", got, out)
	}
}

func TestEditJSONAndUnchanged(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{".lino/config": "", "f.txt": "a\nb\n"})
	v := version.Of([]byte("a\nb\n"))
	out, _, code := runEdit(t, root, "b\n", "edit", "f.txt", "2", "2", "--v", v, "--json")
	if code != 0 || !strings.Contains(out, `"outcome":"ok"`) || !strings.Contains(out, `"unchanged":true`) {
		t.Fatalf("unchanged: exit %d %s", code, out)
	}
	out, _, code = runEdit(t, root, "B\n", "edit", "f.txt", "2", "2", "--v", v, "--json", "--by", "agent-9")
	for _, w := range []string{`"outcome":"updated"`, `"old_version":"` + v + `"`, `"start":2`, `"end":2`, `"op":"edit"`, `"by":"agent-9"`} {
		if !strings.Contains(out, w) {
			t.Errorf("json %s missing %s (exit %d)", out, w, code)
		}
	}
}

func editLit(s string) func(string) string { return func(string) string { return s } }
