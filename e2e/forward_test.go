package e2e

import (
	"strings"
	"testing"
)

// forwardRoot is an initialised tree used by the live/direct parity cases.
func forwardRoot(t *testing.T, home string) *Harness {
	h := New(t)
	if home != "" {
		h.Home = home
	}
	h.Fixture("wallet").
		Write("wallet/service.go", serviceGo()).
		Write("docs/notes.md", "# Notes\nwallet withdraw limits\n").
		Write("crlf.txt", bom+"one\r\ntwo\r\nthree")
	h.ExpectExit(h.Run("init"), 0)
	return h
}

// TestForwardParity runs the same commands against a live process and in
// --direct mode on identical trees; stdout, stderr and exit must match.
func TestForwardParity(t *testing.T) {
	live := forwardRoot(t, shortHome(t))
	direct := forwardRoot(t, "")
	startLive(t, live)

	steps := []struct {
		name  string
		args  []string
		stdin string
		dir   string
		exit  int
	}{
		{name: "read", args: []string{"read", "wallet/service.go", "--lines", "10:16"}},
		{name: "read anchors", args: []string{"read", "wallet/service.go", "--lines", "12:15", "--anchors"}},
		{name: "read json", args: []string{"read", "crlf.txt", "--json"}},
		{name: "read from subdir", args: []string{"read", "service.go", "--lines", "14:14"}, dir: "wallet"},
		{name: "read up from subdir", args: []string{"read", "../docs/notes.md"}, dir: "wallet"},
		{name: "read missing", args: []string{"read", "nope.go"}, exit: 3},
		{name: "read escape", args: []string{"read", "../../etc/passwd"}, dir: "wallet", exit: 7},
		{name: "search", args: []string{"search", "ErrInvalidAmount"}},
		{name: "search words", args: []string{"search", "--words", "withdraw"}},
		{name: "search from subdir", args: []string{"search", "withdraw", "--path", "docs/**"}, dir: "docs"},
		{name: "edit", args: []string{"edit", "wallet/service.go", "14", "14", "--v", "$V"},
			stdin: "        return ErrAmountTooSmall\n"},
		{name: "edit stale v", args: []string{"edit", "wallet/service.go", "13", "13", "--v", "000000"},
			stdin: "    if amt < 0 {\n", exit: 6},
		{name: "edit from subdir", args: []string{"edit", "service.go", "13", "13", "--v", "$V"},
			stdin: "    if amt < 1 {\n", dir: "wallet"},
		{name: "edit crlf json", args: []string{"edit", "crlf.txt", "2", "2", "--v", "$C", "--json"}, stdin: "TWO\n"},
		{name: "read after edit", args: []string{"read", "wallet/service.go", "--lines", "12:15"}},
		{name: "search after edit", args: []string{"search", "ErrAmountTooSmall"}},
		{name: "search old text", args: []string{"search", "return ErrInvalidAmount"}},
	}
	for _, s := range steps {
		args := make([]string, len(s.args))
		for i, a := range s.args {
			switch a {
			case "$V":
				a = direct.ver("wallet/service.go")
			case "$C":
				a = direct.ver("crlf.txt")
			}
			args[i] = a
		}
		l := live.Exec(Cmd{Args: args, Stdin: s.stdin, Dir: s.dir})
		d := direct.Exec(Cmd{Args: append(args, "--direct"), Stdin: s.stdin, Dir: s.dir})
		live.ExpectExit(l, s.exit)
		direct.ExpectExit(d, s.exit)
		if l.Exit != d.Exit || live.normalize(l.Stdout) != direct.normalize(d.Stdout) ||
			live.normalize(l.Stderr) != direct.normalize(d.Stderr) {
			t.Errorf("%s: live and direct differ\n--- live\n%s--- direct\n%s", s.name, live.Transcript(l), direct.Transcript(d))
		}
	}
	if a, b := live.Read("wallet/service.go"), direct.Read("wallet/service.go"); a != b {
		t.Error("trees differ after edits")
	}
	if a, b := live.Read("crlf.txt"), direct.Read("crlf.txt"); a != b || a != bom+"one\r\nTWO\r\nthree" {
		t.Errorf("crlf.txt: live %q, direct %q", a, b)
	}
}

// TestForwardNeedsLive checks that without --direct a call never silently
// runs locally once the process is stopped: it fails with not_running, or
// (with auto-start) says on stderr that a process was started.
func TestForwardNeedsLive(t *testing.T) {
	h := forwardRoot(t, shortHome(t))
	t.Cleanup(func() { h.Run("stop") }) // an auto-started process
	_, done := startLive(t, h)
	h.ExpectExit(h.Run("read", "crlf.txt"), 0)
	h.ExpectExit(h.Run("stop"), 0)
	expectGone(t, h, done)

	r := h.Exec(Cmd{Args: []string{"read", "service.go"}, Dir: "wallet"})
	if r.Exit == 0 && r.Stderr == "" {
		t.Fatalf("read after stop silently ran without a live process:\n%s", h.Transcript(r))
	}
	if r.Exit == 8 && !strings.Contains(r.Stderr, "lino run") {
		t.Errorf("not_running without a run hint:\n%s", h.Transcript(r))
	}
}
