package e2e

import (
	"os"
	"strings"
	"testing"
	"time"
)

// indexRoot is the starting tree for the milestone 2 cases.
func indexRoot(t *testing.T) *Harness {
	return NewInit(t).Fixture("wallet").
		Write("wallet/service.go", serviceGo()).
		Write("docs/notes.md", "# Notes\nThe colour of money.\nwallet withdraw limits and the wallet service\n").
		Write("docs/deep/more.md", "withdraw withdraw withdraw\n").
		Write(".gitignore", "build/\n*.log\n").
		Write(".linoignore", "secret/\n").
		Write("build/out.go", "ErrInvalidAmount in build output\n").
		Write("app.log", "ErrInvalidAmount in a log\n").
		Write("secret/keys.txt", "ErrInvalidAmount secret\n").
		Write("bin.dat", "ErrInvalidAmount\x00binary")
}

func TestIndexCommands(t *testing.T) {
	tests := []struct {
		name string
		cmd  Cmd
		exit int
	}{
		{"index_search_literal", Cmd{Args: []string{"search", "ErrInvalidAmount", "--direct"}}, 0},
		{"index_search_short", Cmd{Args: []string{"search", "if", "--direct"}}, 0},
		{"index_search_empty", Cmd{Args: []string{"search", "NoSuchThing", "--direct"}}, 0},
		{"index_search_regex", Cmd{Args: []string{"search", `Err\w+ =|colou?r`, "--regex", "--direct"}}, 0},
		{"index_search_regex_scan", Cmd{Args: []string{"search", `^\S{7}$`, "--regex", "--direct"}}, 0},
		{"index_search_regex_invalid", Cmd{Args: []string{"search", `a(b`, "--regex", "--direct"}}, 2},
		{"index_search_words", Cmd{Args: []string{"search", "wallet withdraw", "--words", "--direct"}}, 0},
		{"index_search_words_json", Cmd{Args: []string{"search", "withdraw", "--words", "--json", "--direct"}}, 0},
		{"index_ls_root", Cmd{Args: []string{"ls", "--direct"}}, 0},
		{"index_ls_depth", Cmd{Args: []string{"ls", "docs", "--depth", "1", "--direct"}}, 0},
		{"index_ls_missing", Cmd{Args: []string{"ls", "nope", "--direct"}}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := indexRoot(t)
			r := h.Exec(tt.cmd)
			h.ExpectExit(r, tt.exit)
			h.Golden(tt.name, r)
		})
	}
}

// Ignored paths never show up in search or ls.
func TestIndexIgnored(t *testing.T) {
	h := indexRoot(t)
	for _, args := range [][]string{
		{"search", "ErrInvalidAmount", "--direct"},
		{"search", "ErrInvalid", "--regex", "--direct"},
		{"search", "ErrInvalidAmount", "--words", "--direct"},
		{"ls", "--direct"},
	} {
		r := h.Run(args...)
		for _, p := range []string{"build/", "app.log", "secret/", ".lino/"} {
			if strings.Contains(r.Stdout, p) {
				t.Errorf("lino %v lists ignored %s:\n%s", args, p, h.Transcript(r))
			}
		}
	}
}

func TestIndexEditThenSearch(t *testing.T) {
	h := indexRoot(t)
	h.ExpectExit(h.Run("search", "ErrInvalidAmount", "--direct"), 0) // builds the index

	r := h.Exec(Cmd{Args: []string{"edit", "wallet/service.go", "14", "14", "--v", h.ver("wallet/service.go"), "--direct"},
		Stdin: "        return ErrAmountTooSmall\n"})
	h.ExpectExit(r, 0)
	r = h.Exec(Cmd{Args: []string{"write", "wallet/limits.go", "--direct"}, Stdin: "package wallet\n\nconst MaxWithdraw = 100 // ErrAmountTooSmall below\n"})
	h.ExpectExit(r, 0)

	r = h.Run("search", "ErrAmountTooSmall", "--direct")
	h.ExpectExit(r, 0)
	h.Golden("index_edit_then_search", r)

	r = h.Run("search", "return ErrInvalidAmount", "--direct")
	if !strings.Contains(r.Stdout, "0 hits") {
		t.Errorf("old line still found after edit:\n%s", h.Transcript(r))
	}

	r = h.Exec(Cmd{Args: []string{"mv", "wallet/limits.go", "wallet/max.go", "--direct"}})
	h.ExpectExit(r, 0)
	r = h.Exec(Cmd{Args: []string{"rm", "wallet/errors.go", "--force", "--direct"}})
	h.ExpectExit(r, 0)
	r = h.Run("search", "Err", "--direct")
	h.Golden("index_mv_rm_then_search", r)
}

func TestIndexExternalEditThenSearch(t *testing.T) {
	h := indexRoot(t)
	h.ExpectExit(h.Run("search", "ErrInvalidAmount", "--direct"), 0)

	// Size and mtime both change, so a stat-based freshness check sees it.
	time.Sleep(20 * time.Millisecond)
	h.Write("wallet/errors.go", "package wallet\n\nvar ErrExternal = errors.New(\"external\")\n")
	if err := os.Remove(h.Path("docs/notes.md")); err != nil {
		t.Fatal(err)
	}

	// Stale hits from the old content are dropped.
	r := h.Run("search", "ErrInvalidAmount", "--direct")
	h.ExpectExit(r, 0)
	h.Golden("index_external_then_search", r)

	r = h.Run("search", "colour", "--direct")
	if !strings.Contains(r.Stdout, "0 hits") {
		t.Errorf("deleted file still found:\n%s", h.Transcript(r))
	}
	r = h.Run("ls", "--direct")
	if strings.Contains(r.Stdout, "notes.md") || !strings.Contains(r.Stdout, "errors.go  3 lines") {
		t.Errorf("ls not fresh after external change:\n%s", h.Transcript(r))
	}

}

// New content of an externally edited file is searchable in direct mode,
// even before any other call has looked at that file.
func TestIndexExternalNewContent(t *testing.T) {
	h := indexRoot(t)
	h.ExpectExit(h.Run("search", "ErrInvalidAmount", "--direct"), 0)
	time.Sleep(20 * time.Millisecond)
	h.Write("docs/notes.md", "# Notes\nErrExternal was added outside lino\n")
	r := h.Run("search", "ErrExternal", "--direct")
	if !strings.Contains(r.Stdout, "docs/notes.md") {
		t.Errorf("direct search missed externally edited content:\n%s", h.Transcript(r))
	}
}

// lino init builds the index; search then answers from it.
func TestIndexInit(t *testing.T) {
	h := New(t).Fixture("wallet").Write("wallet/service.go", serviceGo())
	r := h.Run("init")
	h.ExpectExit(r, 0)
	r = h.Run("search", "func Withdraw", "--direct")
	h.ExpectExit(r, 0)
	if !strings.Contains(r.Stdout, "wallet/service.go\n  12  func Withdraw") {
		t.Errorf("search after init:\n%s", h.Transcript(r))
	}
}
