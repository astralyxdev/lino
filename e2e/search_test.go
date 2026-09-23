package e2e

import (
	"fmt"
	"strings"
	"testing"
)

// manyHits writes n lines containing "needle", spread over hits/a..e.txt.
func manyHits(h *Harness, n int) {
	files := make([]strings.Builder, 5)
	for i := range n {
		fmt.Fprintf(&files[i%5], "needle %d\n", i)
	}
	for i := range files {
		h.Write(fmt.Sprintf("hits/%c.txt", 'a'+i), files[i].String())
	}
}

func TestSearchOptions(t *testing.T) {
	long := "LongLine " + strings.Repeat("x", 300) + "\n"
	tests := []struct {
		name string
		cmd  Cmd
		exit int
	}{
		{"search_plain", Cmd{Args: []string{"search", "ErrInvalidAmount", "--direct"}}, 0},
		{"search_k", Cmd{Args: []string{"search", "needle", "-k", "3", "--direct"}}, 0},
		{"search_k_too_big", Cmd{Args: []string{"search", "needle", "-k", "201", "--direct"}}, 2},
		{"search_default_truncated", Cmd{Args: []string{"search", "needle", "--direct"}}, 0},
		{"search_max_truncated", Cmd{Args: []string{"search", "needle", "-k", "200", "--path", "hits/a.txt", "--direct"}}, 0},
		{"search_context", Cmd{Args: []string{"search", "ErrInvalidAmount", "-C", "1", "--path", "wallet/service.go", "--direct"}}, 0},
		{"search_context_merge", Cmd{Args: []string{"search", "mark", "-C", "2", "--direct"}}, 0},
		{"search_path_glob", Cmd{Args: []string{"search", "ErrInvalidAmount", "--path", "wallet/**", "--direct"}}, 0},
		{"search_path_repeat", Cmd{Args: []string{"search", "ErrInvalidAmount", "--path", "docs", "--path", "*.md", "--direct"}}, 0},
		{"search_path_basename", Cmd{Args: []string{"search", "ErrInvalidAmount", "--path", "errors.go", "--direct"}}, 0},
		{"search_path_subdir", Cmd{Args: []string{"search", "ErrInvalidAmount", "--path", "*.go", "--direct"}, Dir: "wallet"}, 0},
		{"search_path_none", Cmd{Args: []string{"search", "ErrInvalidAmount", "--path", "nope/**", "--direct"}}, 0},
		{"search_cut_line", Cmd{Args: []string{"search", "LongLine", "--direct"}}, 0},
		{"search_json", Cmd{Args: []string{"search", "ErrInvalidAmount", "-C", "1", "-k", "2", "--json", "--direct"}}, 0},
		{"search_json_cut", Cmd{Args: []string{"search", "LongLine", "--json", "--direct"}}, 0},
		{"search_bad_context", Cmd{Args: []string{"search", "x", "-C", "-1", "--direct"}}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewInit(t).Fixture("wallet").
				Write("wallet/service.go", serviceGo()).
				Write("docs/notes.md", "# Notes\nsee ErrInvalidAmount\n").
				Write("long.txt", long).
				Write("marks.txt", "one\nmark a\ntwo\nthree\nmark b\nfour\nfive\nsix\nseven\nmark c\n")
			manyHits(h, 1100)
			r := h.Exec(tt.cmd)
			h.ExpectExit(r, tt.exit)
			h.Golden(tt.name, r)
		})
	}
}
