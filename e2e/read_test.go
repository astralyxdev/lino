package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

// serviceGo is the 240-line file behind the scope's read example. It is
// generated rather than kept as a fixture so gofmt over the repo stays clean.
func serviceGo() string {
	var b strings.Builder
	for i := 1; i <= 240; i++ {
		switch i {
		case 12:
			b.WriteString("func Withdraw(ctx context.Context, id, amt int64) error {\n")
		case 13:
			b.WriteString("    if amt <= 0 {\n")
		case 14:
			b.WriteString("        return ErrInvalidAmount\n")
		case 15:
			b.WriteString("    }\n")
		default:
			fmt.Fprintf(&b, "// %d\n", i)
		}
	}
	return b.String()
}

func TestRead(t *testing.T) {
	tests := []struct {
		name string
		cmd  Cmd
		exit int
	}{
		{"read_plain", Cmd{Args: []string{"read", "wallet/errors.go", "--direct"}}, 0},
		{"read_range", Cmd{Args: []string{"read", "wallet/service.go", "--lines", "12:15", "--direct"}}, 0},
		{"read_anchors", Cmd{Args: []string{"read", "wallet/service.go", "--lines", "12:15", "--anchors", "--direct"}}, 0},
		{"read_anchors_json", Cmd{Args: []string{"read", "wallet/service.go", "--lines", "12:13", "--anchors", "--json", "--direct"}}, 0},
		{"read_from_subdir", Cmd{Args: []string{"read", "errors.go", "--lines", "6", "--direct"}, Dir: "wallet"}, 0},
		{"read_truncated", Cmd{Args: []string{"read", "big.txt", "--direct"}}, 0},
		{"read_truncated_next", Cmd{Args: []string{"read", "big.txt", "--lines", "501:1000", "--direct"}}, 0},
		{"read_not_found", Cmd{Args: []string{"read", "nope.go", "--direct"}}, 3},
		{"read_outside", Cmd{Args: []string{"read", "../x", "--direct"}}, 7},
		{"read_bad_range", Cmd{Args: []string{"read", "big.txt", "--lines", "5:2", "--direct"}}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewInit(t).Fixture("wallet").
				Write("wallet/service.go", serviceGo()).
				Write("big.txt", numbered(1200))
			r := h.Exec(tt.cmd)
			h.ExpectExit(r, tt.exit)
			h.Golden(tt.name, r)
		})
	}
}

func TestReadTruncatedBoundaries(t *testing.T) {
	tests := []struct {
		lines   int
		args    string
		outcome string
		hint    string
		end     int
	}{
		{500, "", "ok", "", 500},
		{501, "", "truncated", "--lines 501:501", 500},
		{1200, "", "truncated", "--lines 501:1000", 500},
		{1200, "1001:", "ok", "", 1200},
		{1200, "600:1100", "truncated", "--lines 1100:1100", 1099},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d_%s", tt.lines, tt.args), func(t *testing.T) {
			h := NewInit(t).Write("f.txt", numbered(tt.lines))
			args := []string{"read", "f.txt", "--json", "--direct"}
			if tt.args != "" {
				args = append(args, "--lines", tt.args)
			}
			r := h.Run(args...)
			h.ExpectExit(r, 0)
			var env struct {
				OK      bool   `json:"ok"`
				Outcome string `json:"outcome"`
				Hint    string `json:"hint"`
				Data    struct {
					End int `json:"end"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(r.Stdout), &env); err != nil {
				t.Fatalf("bad json %q: %v", r.Stdout, err)
			}
			if !env.OK || env.Outcome != tt.outcome || env.Hint != tt.hint || env.Data.End != tt.end {
				t.Errorf("got ok=%v outcome=%s hint=%q end=%d, want %s %q %d",
					env.OK, env.Outcome, env.Hint, env.Data.End, tt.outcome, tt.hint, tt.end)
			}
		})
	}
}

func TestNotRunningOutsideRoot(t *testing.T) {
	h := New(t).Write("a.txt", "a\n")
	r := h.Run("read", "a.txt", "--direct")
	h.ExpectExit(r, 8)
	if !strings.Contains(r.Stderr, "lino init") {
		t.Errorf("stderr lacks init hint: %q", r.Stderr)
	}
	if r.Stdout != "" {
		t.Errorf("stdout not empty: %q", r.Stdout)
	}
}
