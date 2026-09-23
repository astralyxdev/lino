// Package thin is the lino client binary: it forwards calls to a live process
// that already serves the target root and hands everything else (auto-start,
// --direct, init, help, errors) to lino-core, which links the index. Keeping
// SQLite out of this binary keeps every forwarded call's start-up cheap.
package thin

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"syscall"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/client"
	"github.com/astralyx/lino/internal/output"
)

// CoreName is the binary the client execs for work it does not forward.
const CoreName = "lino-core"

// Spec is what the client must know of a command to parse and forward it.
type Spec struct {
	Name    string
	Accept  cli.Accept
	Local   bool
	Stdin   bool
	MinArgs int
	MaxArgs int
	// Flags maps each command flag to whether it is boolean.
	Flags map[string]bool
}

// SpecsOf describes every command of r.
func SpecsOf(r *cli.Registry) []Spec {
	var out []Spec
	for _, c := range r.Commands() {
		fs := flag.NewFlagSet(c.Name, flag.ContinueOnError)
		c.Setup(fs)
		s := Spec{Name: c.Name, Accept: c.Accept, Local: c.Local, Stdin: c.Stdin,
			MinArgs: c.MinArgs, MaxArgs: c.MaxArgs, Flags: map[string]bool{}}
		fs.VisitAll(func(f *flag.Flag) {
			b, ok := f.Value.(interface{ IsBoolFlag() bool })
			s.Flags[f.Name] = ok && b.IsBoolFlag()
		})
		out = append(out, s)
	}
	return out
}

var errLocal = errors.New("thin: command runs in lino-core")

// Registry builds a parse-only registry from specs. Its commands never run:
// they are forwarded, or the call goes to lino-core.
func Registry(specs []Spec) *cli.Registry {
	r := cli.NewRegistry()
	for _, s := range specs {
		flags := s.Flags
		r.Register(&cli.Command{Name: s.Name, Accept: s.Accept, Local: s.Local, Stdin: s.Stdin,
			MinArgs: s.MinArgs, MaxArgs: s.MaxArgs,
			Setup: func(fs *flag.FlagSet) cli.RunFunc {
				names := make([]string, 0, len(flags))
				for n := range flags {
					names = append(names, n)
				}
				sort.Strings(names)
				for _, n := range names {
					if flags[n] {
						fs.Bool(n, false, "")
					} else {
						fs.Var(new(cli.StringList), n, "")
					}
				}
				return func(context.Context, *cli.Call) (output.Result, error) { return output.Result{}, errLocal }
			}})
	}
	return r
}

// Main forwards argv when a live process serves its target; otherwise it
// execs lino-core with the same argv. exec returns only on failure.
func Main(ctx context.Context, argv []string, env cli.Env, stdout, stderr io.Writer, handoff func([]string) error) int {
	if c := forwardable(argv, env); c != nil {
		resp, ok, err := client.CallLive(ctx, c)
		if ok {
			if err != nil {
				return (&output.Printer{Stdout: stdout, Stderr: stderr, JSON: c.JSON}).Error(err)
			}
			return resp.Print(stdout, stderr)
		}
	}
	err := handoff(argv)
	fmt.Fprintf(stderr, "lino: %v\n", err)
	return 1
}

var parser = Registry(Specs)

// forwardable parses argv and returns the call when it goes to a live
// process: a known command that parses cleanly, takes -i, is not Local and
// has no --direct. Anything else (help, usage errors) is left to lino-core
// so its output stays exactly the full binary's.
func forwardable(argv []string, env cli.Env) *cli.Call {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "-h" || argv[0] == "--help" {
		return nil
	}
	c, err := parser.Parse(argv, env)
	if err != nil || !client.Forwarded(c) {
		return nil
	}
	return c
}

// Exec replaces this process with lino-core running argv.
func Exec(argv []string) error {
	core, err := FindCore()
	if err != nil {
		return err
	}
	return syscall.Exec(core, append([]string{core}, argv...), os.Environ())
}

// FindCore locates lino-core next to this executable (also through a
// symlink to it), then on PATH.
func FindCore() (string, error) {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
		if real, err := filepath.EvalSymlinks(exe); err == nil && filepath.Dir(real) != dirs[0] {
			dirs = append(dirs, filepath.Dir(real))
		}
	}
	for _, d := range dirs {
		p := filepath.Join(d, CoreName)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	if p, err := exec.LookPath(CoreName); err == nil {
		return p, nil
	}
	where := "on PATH"
	if len(dirs) > 0 {
		where = "in " + dirs[0] + " or on PATH"
	}
	return "", fmt.Errorf("cannot find %s %s; reinstall lino (scripts/install.sh installs both binaries)", CoreName, where)
}
