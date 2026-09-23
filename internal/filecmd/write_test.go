package filecmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/version"
)

func runIn(t *testing.T, cwd, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var o, e bytes.Buffer
	code = cli.Default.Main(context.Background(), args, cli.Env{Cwd: cwd, Stdin: strings.NewReader(stdin)}, &o, &e)
	return o.String(), e.String(), code
}

func verOf(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return version.Of(b)
}

func TestWrite(t *testing.T) {
	tests := []struct {
		name     string
		cwd      string
		stdin    string
		args     func(root string) []string
		code     int
		stdoutIn string
		stderrIn string
		file     string // path to check afterwards
		want     string // expected content; "-" = unchanged original
		mode     os.FileMode
	}{
		{
			name: "create", stdin: "hello\nworld", args: func(string) []string { return []string{"new.txt"} },
			code: 0, stdoutIn: "created new.txt v=", file: "new.txt", want: "hello\nworld\n", mode: 0o644,
		},
		{
			name: "create crlf input normalised to lf", stdin: "\xef\xbb\xbfa\r\nb\r\n", args: func(string) []string { return []string{"n.txt"} },
			code: 0, file: "n.txt", want: "a\nb\n",
		},
		{
			name: "create in new dir", stdin: "x\n", args: func(string) []string { return []string{"deep/er/f.go"} },
			code: 0, stdoutIn: "created deep/er/f.go", file: "deep/er/f.go", want: "x\n",
		},
		{
			name: "create relative to cwd", cwd: "sub", stdin: "y\n", args: func(string) []string { return []string{"here.txt"} },
			code: 0, stdoutIn: "created sub/here.txt", file: "sub/here.txt", want: "y\n",
		},
		{
			name: "create empty file", stdin: "", args: func(string) []string { return []string{"e.txt"} },
			code: 0, file: "e.txt", want: "",
		},
		{
			name: "overwrite with --v", stdin: "package b\n",
			args: func(root string) []string { return []string{"a.go", "--v", verOf(t, filepath.Join(root, "a.go"))} },
			code: 0, stdoutIn: "updated a.go v=", file: "a.go", want: "package b\n", mode: 0o600,
		},
		{
			name: "without --v", stdin: "x\n", args: func(string) []string { return []string{"a.go"} },
			code: 6, stderrIn: "--v is required", file: "a.go", want: "-",
		},
		{
			name: "stale --v", stdin: "x\n", args: func(string) []string { return []string{"a.go", "--v", "000000"} },
			code: 6, stdoutIn: "| package a", file: "a.go", want: "-",
		},
		{
			name: "--force", stdin: "forced\n", args: func(string) []string { return []string{"a.go", "--force"} },
			code: 0, stdoutIn: "updated a.go", file: "a.go", want: "forced\n", mode: 0o600,
		},
		{
			name: "keeps crlf bom and missing final newline", stdin: "1\n2\n", args: func(string) []string { return []string{"crlf.txt", "--force"} },
			code: 0, file: "crlf.txt", want: "\xef\xbb\xbf1\r\n2",
		},
		{
			name: "same content unchanged", stdin: "package a\n\nfunc A() {}\n", args: func(string) []string { return []string{"a.go", "--force"} },
			code: 0, stdoutIn: "unchanged a.go", file: "a.go", want: "-",
		},
		{
			name: "empty stdin over existing", stdin: "", args: func(string) []string { return []string{"a.go", "--force"} },
			code: 2, stderrIn: "no content", file: "a.go", want: "-",
		},
		{
			name: "--v and --force", stdin: "x", args: func(string) []string { return []string{"a.go", "--force", "--v", "abcdef"} },
			code: 2, file: "a.go", want: "-",
		},
		{
			name: "--v on missing file", stdin: "x", args: func(string) []string { return []string{"gone.txt", "--v", "abcdef"} },
			code: 6, stderrIn: "no longer exists",
		},
		{
			name: "binary content refused", stdin: "a\x00b", args: func(string) []string { return []string{"b.txt"} },
			code: 7,
		},
		{
			name: "binary target refused", stdin: "x", args: func(string) []string { return []string{"bin.dat", "--force"} },
			code: 7, file: "bin.dat", want: "-",
		},
		{
			name: "outside root refused", stdin: "x", args: func(string) []string { return []string{"../evil.txt"} },
			code: 7,
		},
		{
			name: "meta dir refused", stdin: "x", args: func(string) []string { return []string{".lino/config", "--force"} },
			code: 7,
		},
		{
			name: "json created", stdin: "z\n", args: func(string) []string { return []string{"j.txt", "--json"} },
			code: 0, stdoutIn: `"outcome":"created"`, file: "j.txt", want: "z\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := testRoot(t)
			if err := os.Chmod(filepath.Join(root, "a.go"), 0o600); err != nil {
				t.Fatal(err)
			}
			var orig []byte
			if tt.file != "" {
				orig, _ = os.ReadFile(filepath.Join(root, tt.file))
			}
			args := append([]string{"write"}, tt.args(root)...)
			out, errOut, code := runIn(t, filepath.Join(root, tt.cwd), tt.stdin, args...)
			if code != tt.code {
				t.Fatalf("code = %d, want %d\nstdout: %s\nstderr: %s", code, tt.code, out, errOut)
			}
			if !strings.Contains(out, tt.stdoutIn) {
				t.Errorf("stdout = %q, want %q", out, tt.stdoutIn)
			}
			if !strings.Contains(errOut, tt.stderrIn) {
				t.Errorf("stderr = %q, want %q", errOut, tt.stderrIn)
			}
			if tt.file == "" {
				return
			}
			p := filepath.Join(root, tt.file)
			got, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			want := tt.want
			if want == "-" {
				want = string(orig)
			}
			if string(got) != want {
				t.Errorf("content = %q, want %q", got, want)
			}
			if tt.mode != 0 {
				st, _ := os.Stat(p)
				if st.Mode().Perm() != tt.mode {
					t.Errorf("mode = %v, want %v", st.Mode().Perm(), tt.mode)
				}
			}
		})
	}
}

func TestWriteHooks(t *testing.T) {
	root := testRoot(t)
	var got *mutate.Commit
	Hooks = []mutate.Hook{func(_ context.Context, c *mutate.Commit) error { got = c; return nil }}
	defer func() { Hooks = nil }()
	if _, errOut, code := runIn(t, root, "q\n", "write", "a.go", "--force", "--by", "agent-9"); code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if got == nil || got.Op != "write" || got.By != "agent-9" || string(got.After) != "q\n" || got.OldV == "" || got.Target.End != 3 {
		t.Errorf("commit = %+v", got)
	}
}
