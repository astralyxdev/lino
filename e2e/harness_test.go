// Package e2e runs the built lino binary against temporary roots.
//
// The binary is built once per test run (CGO_ENABLED=0); set LINO_E2E_BIN to
// test a prebuilt binary instead. Golden files live in testdata/golden and
// are rewritten with `go test ./e2e/... -update`.
package e2e

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

// linoBin is the path of the binary under test; lino-core sits next to it.
var linoBin string

// coreBin is the lino-core next to linoBin: the binary live processes run.
func coreBin() string { return filepath.Join(filepath.Dir(linoBin), "lino-core") }

func TestMain(m *testing.M) {
	flag.Parse()
	code, err := setup()
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
	if code == 0 {
		code = m.Run()
	}
	os.Exit(code)
}

func setup() (int, error) {
	if b := os.Getenv("LINO_E2E_BIN"); b != "" {
		linoBin = b
		return 0, nil
	}
	dir, err := os.MkdirTemp("", "lino-e2e-bin-")
	if err != nil {
		return 1, err
	}
	name := "lino"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	linoBin = filepath.Join(dir, name)
	// The thin client execs lino-core from its own directory.
	cmd := exec.Command("go", "build", "-o", dir+string(filepath.Separator),
		"github.com/astralyx/lino/cmd/lino", "github.com/astralyx/lino/cmd/lino-core")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return 1, fmt.Errorf("build lino: %v\n%s", err, out)
	}
	return 0, nil
}

// Harness is one temporary lino root plus a private HOME.
type Harness struct {
	t    *testing.T
	Root string // canonical root directory
	Home string
	Env  map[string]string // extra environment for every Run
}

// New creates an empty, uninitialised root.
func New(t *testing.T) *Harness {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Harness{t: t, Root: root, Home: home, Env: map[string]string{}}
}

// NewInit creates a root with a .lino/ directory, as `lino init` would.
func NewInit(t *testing.T) *Harness {
	h := New(t)
	h.Mkdir(".lino")
	return h
}

// Fixture copies testdata/fixtures/<name> into the root.
func (h *Harness) Fixture(name string) *Harness {
	h.t.Helper()
	src := filepath.Join("testdata", "fixtures", name)
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		dst := filepath.Join(h.Root, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		h.t.Fatalf("fixture %s: %v", name, err)
	}
	return h
}

// Write creates rel (slash-separated) with content, making parent dirs.
func (h *Harness) Write(rel, content string) *Harness {
	h.t.Helper()
	p := h.Path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
	return h
}

// Mkdir creates directory rel in the root.
func (h *Harness) Mkdir(rel string) *Harness {
	h.t.Helper()
	if err := os.MkdirAll(h.Path(rel), 0o755); err != nil {
		h.t.Fatal(err)
	}
	return h
}

// Read returns the content of rel.
func (h *Harness) Read(rel string) string {
	h.t.Helper()
	b, err := os.ReadFile(h.Path(rel))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

// Path is the absolute path of rel.
func (h *Harness) Path(rel string) string {
	return filepath.Join(h.Root, filepath.FromSlash(rel))
}

// Cmd describes one invocation.
type Cmd struct {
	Args  []string
	Stdin string
	Dir   string            // cwd relative to the root; "" = root
	Env   map[string]string // on top of Harness.Env
}

// Result is what a run produced.
type Result struct {
	Args   []string
	Stdout string
	Stderr string
	Exit   int
}

// Run runs lino with args from the root, no stdin.
func (h *Harness) Run(args ...string) Result {
	return h.Exec(Cmd{Args: args})
}

// Exec runs one command. The environment is minimal: PATH, a private HOME,
// no LINO_* variables from the caller, plus h.Env and c.Env.
func (h *Harness) Exec(c Cmd) Result {
	h.t.Helper()
	cmd := exec.Command(linoBin, c.Args...)
	cmd.Dir = filepath.Join(h.Root, filepath.FromSlash(c.Dir))
	cmd.Stdin = strings.NewReader(c.Stdin)
	env := map[string]string{
		"PATH":   os.Getenv("PATH"),
		"HOME":   h.Home,
		"TMPDIR": os.Getenv("TMPDIR"),
		"LANG":   "C",
	}
	for k, v := range h.Env {
		env[k] = v
	}
	for k, v := range c.Env {
		env[k] = v
	}
	for k, v := range env {
		if v != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r := Result{Args: c.Args, Stdout: stdout.String(), Stderr: stderr.String()}
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		r.Exit = ee.ExitCode()
	case err != nil:
		h.t.Fatalf("run lino %v: %v", c.Args, err)
	}
	return r
}

// normalize replaces machine-specific paths with $ROOT and $HOME.
func (h *Harness) normalize(s string) string {
	s = strings.ReplaceAll(s, h.Root, "$ROOT")
	return strings.ReplaceAll(s, h.Home, "$HOME")
}

// Transcript renders a result in golden-file form.
func (h *Harness) Transcript(r Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "$ lino %s\n", strings.Join(quoteArgs(r.Args), " "))
	fmt.Fprintf(&b, "exit %d\n", r.Exit)
	b.WriteString("-- stdout --\n")
	b.WriteString(h.normalize(r.Stdout))
	b.WriteString("-- stderr --\n")
	b.WriteString(h.normalize(r.Stderr))
	return b.String()
}

// Golden compares the transcript of r with testdata/golden/<name>.golden,
// or rewrites it under -update.
func (h *Harness) Golden(name string, r Result) {
	h.t.Helper()
	got := h.Transcript(r)
	p := filepath.Join("testdata", "golden", name+".golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			h.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			h.t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		h.t.Fatalf("%v (run with -update to create)", err)
	}
	if got != string(want) {
		h.t.Errorf("%s mismatch (run with -update to accept)\n--- got\n%s--- want\n%s", p, got, want)
	}
}

// ExpectExit fails the test if r did not exit with code.
func (h *Harness) ExpectExit(r Result, code int) {
	h.t.Helper()
	if r.Exit != code {
		h.t.Errorf("lino %v: exit %d, want %d\n%s", r.Args, r.Exit, code, h.Transcript(r))
	}
}

// Tree lists every file under the root except .lino/, for asserting side effects.
func (h *Harness) Tree() []string {
	h.t.Helper()
	var out []string
	_ = filepath.WalkDir(h.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(h.Root, p)
		rel = filepath.ToSlash(rel)
		if rel == ".lino" {
			return fs.SkipDir
		}
		if !d.IsDir() && rel != ".linoignore" { // written by lino run
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func quoteArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\n'\"$*?") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		out[i] = a
	}
	return out
}
