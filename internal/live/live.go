// Package live runs the per-root process: it owns the index, serves commands
// over the socket and keeps recent file versions in memory.
package live

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/config"
	"github.com/astralyx/lino/internal/ignore"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/paths"
	"github.com/astralyx/lino/internal/proto"
	"github.com/astralyx/lino/internal/registry"
	"github.com/astralyx/lino/internal/reindex"
	"github.com/astralyx/lino/internal/server"
	"github.com/astralyx/lino/internal/vcache"
)

// BuildVersion is recorded in registry entries; main may set it.
var BuildVersion = "dev"

// Options configure Start.
type Options struct {
	Dir      string             // a directory inside the root; "" = cwd
	Name     string             // optional alias
	Idle     time.Duration      // idle exit after this long without calls; 0 = never (enforced by idle exit)
	Registry *registry.Registry // nil = registry.Default()
	Commands *cli.Registry      // nil = cli.Default
}

// Process is a started live process.
type Process struct {
	Root     string
	ID       string
	Name     string
	Idle     time.Duration
	Config   config.Config
	DB       *index.DB
	Rules    *ignore.Rules
	Versions *vcache.Cache
	Started  time.Time
	Startup  index.Summary // result of the startup reconcile

	reg      *registry.Registry
	cmds     *cli.Registry
	srv      *server.Server
	ln       net.Listener
	serveErr chan error
	stopOnce sync.Once
	watching atomic.Bool
	watch    *watcher
	idleExit atomic.Bool
}

// SetWatching records whether the file watcher is running. While it is,
// requests are served with index.WithWatched so commands skip the stat
// refresh of direct mode.
func (p *Process) SetWatching(on bool) { p.watching.Store(on) }

// Watching reports whether the file watcher is marked as running.
func (p *Process) Watching() bool { return p.watching.Load() }

// OnStart hooks run once a process has its index and watcher, before it
// serves; OnStop hooks run at shutdown before the index closes.
var OnStart, OnStop []func(p *Process)

// Middleware wraps the request handler of every process; later entries wrap
// earlier ones.
var Middleware []func(p *Process, h server.Handler) server.Handler

type servingKey struct{}

// FromContext returns the live process handling the request of ctx, or nil
// outside it. Client forwarding must not forward such requests again.
func FromContext(ctx context.Context) *Process {
	p, _ := ctx.Value(servingKey{}).(*Process)
	return p
}

// Start opens the index, reconciles it against disk, listens on the socket
// and writes the registry entry. The process serves until Stop.
func Start(ctx context.Context, opt Options) (*Process, error) {
	reg := opt.Registry
	if reg == nil {
		var err error
		if reg, err = registry.Default(); err != nil {
			return nil, outcome.Wrap(outcome.Internal, err, "registry: "+err.Error())
		}
	}
	cmds := opt.Commands
	if cmds == nil {
		cmds = cli.Default
	}
	dir := opt.Dir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		dir = wd
	}
	if opt.Name != "" && !registry.ValidName(opt.Name) {
		return nil, outcome.New(outcome.Usage, "invalid name %q: use letters, digits, '.', '_' or '-'", opt.Name)
	}
	root, ok, err := reg.FindRoot(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, outcome.New(outcome.NotFound, "no such directory: %s", dir)
		}
		return nil, err
	}
	if !ok {
		return nil, outcome.New(outcome.NotRunning, "%s is not inside an initialised lino root", dir).
			WithHint("lino init " + dir)
	}
	p := &Process{
		Root: root, ID: registry.IDFor(root), Name: opt.Name, Idle: opt.Idle,
		Versions: vcache.New(0), reg: reg, cmds: cmds, serveErr: make(chan error, 1),
	}
	if err := p.open(ctx); err != nil {
		return nil, err
	}
	reindex.Open = p.reindexTarget
	vcache.Register(p.Root, p.Versions)
	// Changes made while no process ran are external edits; a freshly built
	// index has nothing to compare against.
	if OnExternal != nil && !p.DB.Rebuilt && len(p.Startup.Changes) > 0 {
		OnExternal(ctx, p, p.Startup.Changes)
	}
	p.startWatcher()
	for _, f := range OnStart {
		f(p)
	}
	var h server.Handler = p.handle
	for _, m := range Middleware {
		h = m(p, h)
	}
	p.srv = &server.Server{Handler: h, OnShutdown: p.cleanup}
	go func() { p.serveErr <- p.srv.Serve(p.ln) }()
	return p, nil
}

// open takes the socket first, so a second process for the root fails with
// live_exists before touching the index; then reconciles and registers.
func (p *Process) open(ctx context.Context) (err error) {
	sock, err := p.reg.PrepareSocket(p.ID)
	if err != nil {
		return err
	}
	if p.ln, err = server.Listen(sock); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			p.ln.Close()
			if p.DB != nil {
				p.DB.Close()
			}
		}
	}()
	if p.Config, err = config.Load(p.Root); err != nil {
		return err
	}
	if p.Rules, err = ignore.NewRules(p.Root); err != nil {
		return err
	}
	if p.DB, err = index.Open(ctx, p.Root); err != nil {
		return err
	}
	if p.Startup, err = p.DB.Reconcile(ctx, p.Rules, p.Config.MaxFileSize); err != nil {
		return err
	}
	p.Started = time.Now()
	return p.reg.Write(registry.Entry{
		ID: p.ID, PID: os.Getpid(), Root: p.Root, Socket: sock,
		Started: p.Started, Version: BuildVersion, Name: p.Name,
	})
}

// Server returns the socket server (for activity and shutdown hooks).
func (p *Process) Server() *server.Server { return p.srv }

// Done is closed once the process has stopped.
func (p *Process) Done() <-chan struct{} { return p.srv.Done() }

// Stop shuts the server down gracefully, closes the index and removes the
// socket and registry entry. Safe to call more than once.
func (p *Process) Stop(ctx context.Context) error {
	return p.srv.Shutdown(ctx)
}

// Wait blocks until ctx is done or the server fails, then stops the process.
func (p *Process) Wait(ctx context.Context, grace time.Duration) error {
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-p.serveErr:
	case <-p.Done():
		return nil
	}
	sctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	return errors.Join(serveErr, p.Stop(sctx))
}

func (p *Process) cleanup() {
	p.stopOnce.Do(func() {
		p.stopWatcher()
		vcache.Register(p.Root, nil)
		for _, f := range OnStop {
			f(p)
		}
		p.DB.Close()
		if e, err := p.reg.Read(p.ID); err == nil && e.PID == os.Getpid() {
			p.reg.Remove(p.ID)
		}
	})
}

func (p *Process) reindexTarget(ctx context.Context, root string) (*reindex.Target, func(), error) {
	if root != p.Root {
		return reindex.Direct(ctx, root)
	}
	return &reindex.Target{DB: p.DB, Rules: p.Rules, MaxSize: p.Config.MaxFileSize}, func() {}, nil
}

func inRoot(root, dir string) bool {
	if dir == "" {
		return false
	}
	if c, err := paths.Canonical(dir); err == nil {
		dir = c
	}
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// notServed are commands that make no sense inside the live process.
var notServed = map[string]bool{"run": true, "init": true, "ps": true}

func (p *Process) handle(ctx context.Context, req *proto.Request) (output.Result, error) {
	if notServed[req.Command] {
		return output.Result{}, outcome.New(outcome.Usage, "%s is not served by the live process", req.Command)
	}
	cwd := req.Cwd
	if !inRoot(p.Root, cwd) {
		// A client addressing this process by id from elsewhere: paths are
		// relative to the root, never to another root the cwd may be in.
		cwd = p.Root
	}
	env := cli.Env{Stdin: bytes.NewReader(req.Stdin), Cwd: cwd}
	c, err := p.cmds.ParseArgs(req.Command, req.Args, req.Flags, env)
	if err != nil {
		return output.Result{}, err
	}
	c.JSON = req.JSON
	c.By = req.By
	c.Direct = false
	ctx = context.WithValue(ctx, servingKey{}, p)
	if p.Watching() {
		ctx = index.WithWatched(ctx)
	}
	return c.Run(ctx)
}
