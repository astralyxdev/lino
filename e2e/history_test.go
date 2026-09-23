package e2e

import (
	"regexp"
	"testing"
)

var (
	clock     = regexp.MustCompile(`\b\d\d:\d\d:\d\d\b`)
	clockJSON = regexp.MustCompile(`"time":"[^"]+"`)
)

func maskClock(r Result) Result {
	r.Stdout = clockJSON.ReplaceAllString(r.Stdout, `"time":"<time>"`)
	r.Stdout = clock.ReplaceAllString(r.Stdout, "<time>")
	return r
}

// historyRoot records: 1 edit by agent-2, 2 write (created) by agent-1,
// 3 mv, 4 external edit, 5 edit by agent-1 in docs/, 6 rm of a whole dir's file.
func historyRoot(t *testing.T) *Harness {
	h := NewInit(t).
		Write("wallet/service.go", "a\nb\nc\nd\n").
		Write("docs/notes.md", "one\ntwo\n").
		Write("old/gone.txt", "bye\n")
	h.ExpectExit(h.Run("index", "--direct"), 0)
	steps := []Cmd{
		{Args: []string{"edit", "wallet/service.go", "2", "3", "--v", h.ver("wallet/service.go"), "--direct", "--by", "agent-2"}, Stdin: "X\nY\n"},
		{Args: []string{"write", "wallet/validate.go", "--direct", "--by", "agent-1"}, Stdin: "new\n"},
		{Args: []string{"mv", "wallet/validate.go", "wallet/check.go", "--direct"}},
	}
	for _, c := range steps {
		h.ExpectExit(h.Exec(c), 0)
	}
	h.Write("wallet/service.go", "a\nX\nY\nd\next\n")
	h.ExpectExit(h.Run("index", "--direct"), 0)
	h.ExpectExit(h.Exec(Cmd{Args: []string{"delete", "docs/notes.md", "1", "1", "--v", h.ver("docs/notes.md"), "--direct", "--by", "agent-1"}}), 0)
	h.ExpectExit(h.Run("rm", "old/gone.txt", "--force", "--direct"), 0)
	return h
}

func TestHistory(t *testing.T) {
	h := historyRoot(t)
	cases := []struct {
		name string
		args []string
		dir  string
		exit int
	}{
		{name: "all", args: []string{"history"}},
		{name: "json", args: []string{"history", "--json", "-k", "2"}},
		{name: "file", args: []string{"history", "wallet/service.go"}},
		{name: "file_from_subdir", args: []string{"history", "service.go"}, dir: "wallet"},
		{name: "dir", args: []string{"history", "wallet"}},
		{name: "dir_slash", args: []string{"history", "wallet/"}},
		{name: "mv_source", args: []string{"history", "wallet/validate.go"}},
		{name: "deleted_dir", args: []string{"history", "old"}},
		{name: "by", args: []string{"history", "--by", "agent-1"}},
		{name: "since", args: []string{"history", "--since", "3"}},
		{name: "combined", args: []string{"history", "wallet", "--by", "agent-1", "--since", "1"}},
		{name: "k", args: []string{"history", "-k", "2"}},
		{name: "k_exact", args: []string{"history", "-k", "6"}},
		{name: "empty", args: []string{"history", "--since", "6"}},
		{name: "no_match", args: []string{"history", "nothing.go"}},
		{name: "bad_k", args: []string{"history", "-k", "0"}, exit: 2},
		{name: "bad_since", args: []string{"history", "--since", "-1"}, exit: 2},
		{name: "two_paths", args: []string{"history", "a", "b"}, exit: 2},
		{name: "outside", args: []string{"history", "../../x"}, dir: "wallet", exit: 7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := h.Exec(Cmd{Args: append(c.args, "--direct"), Dir: c.dir})
			h.ExpectExit(r, c.exit)
			h.Golden("history/"+c.name, maskClock(r))
		})
	}
}

func TestHistoryLive(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	h.Write("a.txt", "one\n")
	h.ExpectExit(h.Run("init"), 0)
	startLive(t, h)
	h.ExpectExit(h.Exec(Cmd{Args: []string{"write", "b.txt", "--by", "me"}, Stdin: "b\n"}), 0)
	r := h.Run("history")
	h.ExpectExit(r, 0)
	h.Golden("history/live", maskClock(r))
}
