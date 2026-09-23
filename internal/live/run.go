package live

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/config"
	"github.com/astralyx/lino/internal/ignore"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/registry"
)

func init() { cli.Register(RunCommand) }

// Stderr receives the readiness line of a foreground run.
var Stderr io.Writer = os.Stderr

// ShutdownGrace bounds a graceful stop after a signal.
var ShutdownGrace = 10 * time.Second

// RunCommand is `lino run`.
var RunCommand = &cli.Command{
	Name:    "run",
	Usage:   "[dir] [--name N] [--idle D] [--foreground]",
	Summary: "start (or find) the live process; prints its id",
	MaxArgs: 1,
	Local:   true,
	Setup: func(fs *flag.FlagSet) cli.RunFunc {
		name := fs.String("name", "", "human alias for the process id")
		idle := fs.String("idle", "", "exit after this long without calls (0 = never)")
		fg := fs.Bool("foreground", false, "stay attached until stopped")
		return func(ctx context.Context, c *cli.Call) (output.Result, error) {
			dir := c.Arg(0)
			if !filepath.IsAbs(dir) && c.Cwd != "" {
				dir = filepath.Join(c.Cwd, dir)
			}
			return Run(ctx, RunRequest{Dir: dir, Name: *name, Idle: *idle, Foreground: *fg})
		}
	},
}

// RunRequest is one run call.
type RunRequest struct {
	Dir        string
	Name       string
	Idle       string // duration; "" = config default
	Foreground bool
	Registry   *registry.Registry // nil = default
}

// RunData describes the process run started or found.
type RunData struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Root     string `json:"root"`
	PID      int    `json:"pid"`
	Existing bool   `json:"existing"`
	// CreatedLinoignore is set when the start wrote the default .linoignore.
	CreatedLinoignore bool `json:"created_linoignore,omitempty"`
}

// CreatedLinoignoreNote is printed on stderr when a start writes .linoignore.
const CreatedLinoignoreNote = "created .linoignore with defaults"

// WriteText prints the id.
func (d RunData) WriteText(w io.Writer) error {
	_, err := fmt.Fprintln(w, d.ID)
	return err
}

// Run starts a live process for the root containing req.Dir. When one already
// runs it reports that one. In the foreground it blocks until SIGINT/SIGTERM.
func Run(ctx context.Context, req RunRequest) (output.Result, error) {
	reg := req.Registry
	if reg == nil {
		var err error
		if reg, err = registry.Default(); err != nil {
			return output.Result{}, outcome.Wrap(outcome.Internal, err, "registry: "+err.Error())
		}
	}
	var ready *os.File
	if req.Foreground {
		ready = readyReporter()
	}
	if root, ok, err := reg.FindRoot(dirOrCwd(req.Dir)); err == nil && ok {
		if d, ok := existing(reg, root); ok {
			reportReady(ready, RunData{}, "", outcome.New(outcome.LiveExists, "already running"))
			return output.Result{Data: d, Message: "already running"}, nil
		}
	}
	var idle time.Duration
	if req.Idle != "" {
		d, err := config.ParseDuration(req.Idle)
		if err != nil || d < 0 {
			err := outcome.New(outcome.Usage, "--idle %q: not a duration", req.Idle)
			reportReady(ready, RunData{}, "", err)
			return output.Result{}, err
		}
		idle = d
	}
	if !req.Foreground {
		return startDetached(ctx, reg, req)
	}

	created, err := ensureLinoignore(reg, req.Dir)
	if err != nil {
		reportReady(ready, RunData{}, "", err)
		return output.Result{}, err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	opt := Options{Dir: req.Dir, Name: req.Name, Idle: idle, Registry: reg}
	if created {
		opt.Created, opt.By = []string{ignore.LinoignoreFile}, os.Getenv("LINO_BY")
	}
	p, err := Start(ctx, opt)
	if err != nil {
		reportReady(ready, RunData{}, "", err)
		return output.Result{}, err
	}
	if req.Idle == "" {
		idle = p.Config.Idle
	}
	p.WatchIdle(idle)
	if created {
		fmt.Fprintln(Stderr, CreatedLinoignoreNote)
	}
	s := p.Startup
	readyLine := fmt.Sprintf("lino %s serving %s (reconciled %d files: %d added, %d modified, %d removed)",
		p.ID, p.Root, s.Files, s.Added, s.Modified, s.Removed)
	fmt.Fprintln(Stderr, readyLine)
	d := RunData{ID: p.ID, Name: p.Name, Root: p.Root, PID: os.Getpid(), CreatedLinoignore: created}
	reportReady(ready, d, readyLine, nil)
	if err := p.Wait(ctx, ShutdownGrace); err != nil {
		return output.Result{Outcome: outcome.Internal, Data: d, Message: "stopped: " + err.Error()}, nil
	}
	if p.IdleExited() {
		return output.Result{Data: d, Message: fmt.Sprintf("stopped after %s idle", p.Idle)}, nil
	}
	return output.Result{Data: d, Message: "stopped"}, nil
}

// ensureLinoignore writes the default .linoignore into the root of dir before
// the startup reconcile, so the defaults already apply. lino init creates it
// too; this covers roots initialised before it did. Outside an initialised
// root it does nothing and leaves the error to Start.
func ensureLinoignore(reg *registry.Registry, dir string) (bool, error) {
	root, ok, err := reg.FindRoot(dirOrCwd(dir))
	if err != nil || !ok {
		return false, nil
	}
	created, err := ignore.EnsureLinoignore(root)
	if err != nil {
		return false, outcome.Wrap(outcome.Internal, err, "create .linoignore: "+err.Error())
	}
	return created, nil
}

func dirOrCwd(d string) string {
	if d != "" {
		return d
	}
	wd, _ := os.Getwd()
	return wd
}
