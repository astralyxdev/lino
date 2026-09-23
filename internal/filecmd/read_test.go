package filecmd

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/cli"
)

var update = flag.Bool("update", false, "rewrite golden files")

func testRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		".lino/config":   "",
		"a.go":           "package a\n\nfunc A() {}\n",
		"crlf.txt":       "\xef\xbb\xbfone\r\ntwo\r\nthree",
		"empty.txt":      "",
		"bin.dat":        "ab\x00cd",
		"sub/nested.txt": "x\ny\n",
	}
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

func TestReadGolden(t *testing.T) {
	root := testRoot(t)
	tests := []struct {
		name string
		cwd  string
		args []string
		code int
	}{
		{"whole", "", []string{"a.go"}, 0},
		{"range", "", []string{"a.go", "--lines", "2:3"}, 0},
		{"clamp", "", []string{"a.go", "--lines", "2:99"}, 0},
		{"open_end", "", []string{"a.go", "--lines", "3:"}, 0},
		{"single", "", []string{"a.go", "--lines", "1"}, 0},
		{"crlf_bom", "", []string{"crlf.txt"}, 0},
		{"empty_file", "", []string{"empty.txt"}, 0},
		{"past_end", "", []string{"a.go", "--lines", "10:20"}, 0},
		{"from_subdir", "sub", []string{"nested.txt"}, 0},
		{"bad_range", "", []string{"a.go", "--lines", "3:2"}, 2},
		{"zero_start", "", []string{"a.go", "--lines", "0:2"}, 2},
		{"not_number", "", []string{"a.go", "--lines", "x"}, 2},
		{"missing", "", []string{"nope.go"}, 3},
		{"binary", "", []string{"bin.dat"}, 7},
		{"outside", "", []string{"../x"}, 7},
		{"dir", "", []string{"sub"}, 7},
	}
	for _, tt := range tests {
		for _, mode := range []string{"txt", "json"} {
			t.Run(tt.name+"_"+mode, func(t *testing.T) {
				args := append([]string{"read"}, tt.args...)
				if mode == "json" {
					args = append(args, "--json")
				}
				out, errOut, code := run(t, filepath.Join(root, tt.cwd), args...)
				if code != tt.code {
					t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tt.code, out, errOut)
				}
				got := "stdout:\n" + out + "stderr:\n" + errOut
				path := filepath.Join("testdata", "read_"+tt.name+"."+mode+".golden")
				if *update {
					if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("%v (run with -update)", err)
				}
				if got != string(want) {
					t.Errorf("got:\n%s\nwant:\n%s", got, want)
				}
			})
		}
	}
}

func TestReadNotRunning(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	_, errOut, code := run(t, dir, "read", "a.txt")
	if code != 8 || !strings.Contains(errOut, "lino init") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

func TestParseRange(t *testing.T) {
	tests := []struct {
		in   string
		a, b int
		err  bool
	}{
		{"", 1, 0, false},
		{"5", 5, 5, false},
		{"5:", 5, 0, false},
		{"5:9", 5, 9, false},
		{"5:5", 5, 5, false},
		{"0:3", 0, 0, true},
		{"-1:3", 0, 0, true},
		{"4:3", 0, 0, true},
		{":3", 0, 0, true},
		{"a:b", 0, 0, true},
		{"3:b", 0, 0, true},
	}
	for _, tt := range tests {
		a, b, err := ParseRange(tt.in)
		if (err != nil) != tt.err || a != tt.a || b != tt.b {
			t.Errorf("ParseRange(%q) = %d, %d, %v", tt.in, a, b, err)
		}
	}
}
