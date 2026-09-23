package e2e

import (
	"regexp"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/registry"
)

var statsVolatile = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`"(pid|[a-z_]*bytes[a-z_0-9]*|[a-z0-9]+_us)":\d+`), `"$1":<n>`},
	{regexp.MustCompile(`"since":"[^"]+"`), `"since":"<time>"`},
	{regexp.MustCompile(`\d{4}-\d\d-\d\d \d\d:\d\d:\d\d`), "<time>"},
	{regexp.MustCompile(`pid \d+`), "pid <n>"},
	{regexp.MustCompile(`\b\d+(\.\d+)?(µs|ms|s)\b`), "<dur>"},
	{regexp.MustCompile(`\b\d+(\.\d+)?[BKMGT]\b`), "<size>"},
	{regexp.MustCompile(` {2,}`), "  "},
	// Current RSS is reported on Linux only; drop it so goldens match everywhere.
	{regexp.MustCompile(`"rss_bytes":<n>,`), ""},
	{regexp.MustCompile(`, rss <size>, peak`), ", peak"},
}

func maskStats(h *Harness, r Result) Result {
	id := registry.IDFor(h.Root)
	r.Stdout = strings.ReplaceAll(r.Stdout, id, "$ID")
	r.Stderr = strings.ReplaceAll(r.Stderr, id, "$ID")
	for _, v := range statsVolatile {
		r.Stdout = v.re.ReplaceAllString(r.Stdout, v.repl)
		r.Stderr = v.re.ReplaceAllString(r.Stderr, v.repl)
	}
	return r
}

func TestStats(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	h.Write("a.txt", "one\ntwo\nthree\n")
	h.ExpectExit(h.Run("init"), 0)

	golden := func(name string, exit int, args ...string) {
		t.Helper()
		r := h.Run(args...)
		h.ExpectExit(r, exit)
		h.Golden("stats/"+name, maskStats(h, r))
	}
	golden("direct_empty", 0, "stats", "--direct")
	golden("not_running", 8, "stats")
	golden("bad_arg", 2, "stats", "extra")

	v := h.ver("a.txt")
	_, done := startLive(t, h)
	for _, c := range []struct {
		args  []string
		stdin string
		exit  int
	}{
		{[]string{"write", "b.txt"}, "b\n", 0},
		{[]string{"edit", "a.txt", "1", "1", "--v", v}, "ONE\n", 0},
		{[]string{"delete", "a.txt", "1", "1", "--v", "000000"}, "", 6},
		{[]string{"edit", "a.txt", "1:zzz", "1:zzz", "--v", "000000"}, "y\n", 4},
		{[]string{"read", "a.txt"}, "", 0},
		{[]string{"search", "three"}, "", 0},
	} {
		h.ExpectExit(h.Exec(Cmd{Args: c.args, Stdin: c.stdin}), c.exit)
	}
	golden("live", 0, "stats")
	golden("live_json", 0, "stats", "--json")
	h.ExpectExit(h.Run("stop"), 0)
	expectGone(t, h, done)

	// Counters survive the process: direct mode reads the saved file.
	r := h.Run("stats", "--direct")
	h.ExpectExit(r, 0)
	h.Golden("stats/direct_after", maskStats(h, r))
	if !regexp.MustCompile(`(?m)^stop +1 `).MatchString(r.Stdout) {
		t.Errorf("stop call not counted:\n%s", r.Stdout)
	}
}
