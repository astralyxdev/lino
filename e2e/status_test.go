package e2e

import (
	"bufio"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/registry"
)

var volatile = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`\d{4}-\d\d-\d\d \d\d:\d\d:\d\d`), "<time>"},
	{regexp.MustCompile(`"(at|started)":"[^"]+"`), `"$1":"<time>"`},
	{regexp.MustCompile(`\d+ms`), "<n>ms"},
	{regexp.MustCompile(`"(duration_ms|uptime_s|pid)":\d+`), `"$1":<n>`},
	{regexp.MustCompile(`\(\d+[hms0-9.]* ago`), "(<age> ago"},
	{regexp.MustCompile(`pid \d+, up [0-9hms.]+`), "pid <n>, up <age>"},
	{regexp.MustCompile(`\(pid \d+\)`), "(pid <n>)"},
	{regexp.MustCompile(`\b(kqueue|inotify)\b`), "<backend>"},
}

func mask(r Result) Result {
	for _, v := range volatile {
		r.Stdout = v.re.ReplaceAllString(r.Stdout, v.repl)
		r.Stderr = v.re.ReplaceAllString(r.Stderr, v.repl)
	}
	return r
}

func TestStatusAndIndex(t *testing.T) {
	h := New(t)
	h.Write("a.txt", "one\ntwo\n").Write("src/b.go", "package b\n").Write("bin.dat", "a\x00b")
	h.ExpectExit(h.Run("init"), 0)

	h.Write("c.txt", "new\n")
	os.Remove(h.Path("src/b.go"))
	steps := []struct {
		name string
		args []string
		exit int
	}{
		{"index_changes", []string{"index", "--direct"}, 0},
		{"index_unchanged_json", []string{"index", "--direct", "--json"}, 0},
		{"status_not_running", []string{"status"}, 8},
		{"status_bad_arg", []string{"status", "extra"}, 2},
	}
	for _, s := range steps {
		r := h.Run(s.args...)
		h.ExpectExit(r, s.exit)
		h.Golden("status/"+s.name, mask(r))
	}
}

func TestStatusUninitialised(t *testing.T) {
	h := New(t)
	r := h.Run("status")
	h.ExpectExit(r, 8)
	h.Golden("status/uninitialised", r)
}

func TestStatusLive(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	h.Write("a.txt", "one\ntwo\n")
	h.ExpectExit(h.Run("init"), 0)

	cmd := exec.Command(linoBin, "run", "--foreground", "--name", "st")
	cmd.Dir = h.Root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + h.Home, "LANG=C"}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if strings.Contains(sc.Text(), " serving ") {
				close(ready)
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(15 * time.Second):
		t.Fatal("process did not become ready")
	}

	id := registry.IDFor(h.Root)
	mask := func(r Result) Result {
		r = mask(r)
		r.Stdout = strings.ReplaceAll(r.Stdout, id, "$ID")
		r.Stderr = strings.ReplaceAll(r.Stderr, id, "$ID")
		return r
	}

	// Forwarded to the live process, which answers itself.
	r := h.Run("status")
	h.ExpectExit(r, 0)
	h.Golden("status/live", mask(r))
	r = h.Run("status", "--json")
	h.ExpectExit(r, 0)
	h.Golden("status/live_json", mask(r))
	h.Write("b.txt", "two\n")
	r = h.Run("index")
	h.ExpectExit(r, 0)
	h.Golden("status/live_index", mask(r))

	// A second writer next to the live process is refused.
	r = h.Run("index", "--direct")
	h.ExpectExit(r, 9)
	h.Golden("status/live_index_direct", mask(r))
	cmd.Process.Signal(syscall.SIGINT)
}
