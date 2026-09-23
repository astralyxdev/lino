package filecmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, s := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func TestReadAnchorsGolden(t *testing.T) {
	root := t.TempDir()
	var svc strings.Builder
	for i := 1; i <= 240; i++ {
		switch i {
		case 12:
			svc.WriteString("func Withdraw(ctx context.Context, id, amt int64) error {\n")
		case 13:
			svc.WriteString("    if amt <= 0 {\n")
		case 14:
			svc.WriteString("        return ErrInvalidAmount\n")
		case 15:
			svc.WriteString("    }\n")
		default:
			fmt.Fprintf(&svc, "// %d\n", i)
		}
	}
	writeFiles(t, root, map[string]string{
		".lino/config":      "",
		"wallet/service.go": svc.String(),
		"crlf.txt":          "one\r\ntwo\r\n",
	})
	tests := []struct {
		name string
		args []string
	}{
		{"scope_example", []string{"wallet/service.go", "--lines", "12:15", "--anchors"}},
		{"crlf", []string{"crlf.txt", "--anchors"}},
	}
	for _, tt := range tests {
		for _, mode := range []string{"txt", "json"} {
			t.Run(tt.name+"_"+mode, func(t *testing.T) {
				args := append([]string{"read"}, tt.args...)
				if mode == "json" {
					args = append(args, "--json")
				}
				out, errOut, code := run(t, root, args...)
				if code != 0 {
					t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
				}
				got := "stdout:\n" + out + "stderr:\n" + errOut
				path := filepath.Join("testdata", "read_anchors_"+tt.name+"."+mode+".golden")
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

	out, _, _ := run(t, root, "read", "wallet/service.go", "--lines", "12:15", "--anchors")
	re := regexp.MustCompile(`^\d+:[0-9A-Za-z]{3}\| `)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if lines[0] != "wallet/service.go v="+lines[0][len("wallet/service.go v="):len("wallet/service.go v=")+6]+" lines 12-15 of 240" {
		t.Errorf("header %q", lines[0])
	}
	for i, l := range lines[1:] {
		if !re.MatchString(l) || !strings.HasPrefix(l, fmt.Sprint(12+i)+":") {
			t.Errorf("line %q does not match N:hash| text", l)
		}
	}
}

func TestReadTruncation(t *testing.T) {
	small := t.TempDir()
	writeFiles(t, small, map[string]string{".lino/config": "read.lines = 3\n", "f.txt": numbered(10)})
	big := t.TempDir()
	writeFiles(t, big, map[string]string{".lino/config": "", "f.txt": numbered(1200)})

	tests := []struct {
		name       string
		root       string
		lines      string
		start, end int
		hint       string
	}{
		{"default limit", big, "", 1, 500, "--lines 501:1000"},
		{"default limit near end", big, "700:", 700, 1199, "--lines 1200:1200"},
		{"default tail fits", big, "701:", 701, 1200, ""},
		{"default exactly limit", big, "501:1000", 501, 1000, ""},
		{"whole", small, "", 1, 3, "--lines 4:6"},
		{"exact limit", small, "1:3", 1, 3, ""},
		{"one over", small, "1:4", 1, 3, "--lines 4:4"},
		{"tail fits", small, "8:", 8, 10, ""},
		{"middle", small, "5:10", 5, 7, "--lines 8:10"},
		{"clamped end", small, "2:100", 2, 4, "--lines 5:7"},
		{"last window", small, "6:", 6, 8, "--lines 9:10"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := []string{"read", "f.txt", "--json"}
			if tt.lines != "" {
				args = append(args, "--lines", tt.lines)
			}
			out, _, code := run(t, tt.root, args...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
			var env struct {
				Outcome string   `json:"outcome"`
				Hint    string   `json:"hint"`
				Data    ReadData `json:"data"`
			}
			if err := json.Unmarshal([]byte(out), &env); err != nil {
				t.Fatal(err)
			}
			wantOutcome := "ok"
			if tt.hint != "" {
				wantOutcome = "truncated"
			}
			if env.Outcome != wantOutcome || env.Hint != tt.hint || env.Data.Start != tt.start || env.Data.End != tt.end {
				t.Fatalf("got %s %q %d-%d, want %s %q %d-%d", env.Outcome, env.Hint, env.Data.Start, env.Data.End,
					wantOutcome, tt.hint, tt.start, tt.end)
			}
			if len(env.Data.Lines) != tt.end-tt.start+1 || env.Data.Lines[0] != fmt.Sprintf("line %d", tt.start) {
				t.Fatalf("lines %v", env.Data.Lines)
			}
		})
	}

	_, errOut, code := run(t, small, "read", "f.txt", "--lines", "5:10")
	if code != 0 || !strings.Contains(errOut, "hint: --lines 8:10") || !strings.Contains(errOut, "truncated") {
		t.Fatalf("text mode: exit %d stderr %q", code, errOut)
	}
}
