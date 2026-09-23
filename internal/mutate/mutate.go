package mutate

import (
	"context"
	"io/fs"
	"sync"

	"github.com/astralyx/lino/internal/anchor"
	"github.com/astralyx/lino/internal/fileio"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/paths"
	"github.com/astralyx/lino/internal/textfile"
	"github.com/astralyx/lino/internal/version"
)

// Range is a 1-based inclusive line range. An empty range (End == Start-1)
// is a position between lines: before line Start.
type Range struct {
	Start, End int
}

// Len returns the number of lines in r.
func (r Range) Len() int { return r.End - r.Start + 1 }

// Empty reports whether r covers no lines.
func (r Range) Empty() bool { return r.End < r.Start }

// Op is one line operation on a loaded file.
type Op interface {
	// Name is the history operation name, e.g. "edit".
	Name() string
	// Target resolves the op's anchors against the current lines and returns
	// the region it touches (empty for a pure insertion).
	Target(lines []string) (Range, error)
	// Apply changes doc at target and returns the changed range in the new
	// lines (empty when lines were only removed).
	Apply(doc *textfile.Doc, target Range) (Range, error)
}

// Addresser is implemented by ops addressed by line numbers of the version
// the caller read. Addressed returns that range (empty for a position) and
// false when the op is not addressed by lines.
type Addresser interface {
	Addressed() (Range, bool)
}

// VersionCheck decides whether op, based on version v, may touch target
// (resolved in the current file f). It returns a conflict error otherwise.
type VersionCheck func(ctx context.Context, f *fileio.File, v string, op Op, target Range) error

// Hook runs after a successful write, e.g. to re-index and record history.
// An error is reported to the caller but the write stays.
type Hook func(ctx context.Context, c *Commit) error

// Request is one mutation of one file.
type Request struct {
	Cwd  string // base for a relative Path; empty means the root
	Path string
	V    string // version the caller last read; required
	By   string // optional author
	Op   Op
}

// Result describes an applied mutation.
type Result struct {
	Path      string `json:"path"`
	From      string `json:"from,omitempty"` // mv: the source path
	Op        string `json:"op"`
	By        string `json:"by,omitempty"`
	OldV      string `json:"old_version"`
	NewV      string `json:"version"`
	Target    Range  `json:"-"` // region in the old file
	Changed   Range  `json:"-"` // region in the new file
	Total     int    `json:"total"`
	Unchanged bool   `json:"unchanged,omitempty"`
}

// Commit is what post-write hooks see.
type Commit struct {
	Result
	Real   string
	Before []byte
	After  []byte
	Doc    *textfile.Doc // new content
	Mode   fs.FileMode   // rm: permission bits of the removed file
	Undid  int64         // rollback: the change it undoes
	// Removed is set when the change removed the file (rm, or a rollback
	// that removes a file); Before holds its content.
	Removed bool
	// Seq is the change id the change log assigned; set by its hook.
	Seq int64
}

// Pipeline runs mutations for one root. Mutations are serialised.
type Pipeline struct {
	Root    *paths.Root
	MaxSize int64        // refuse files larger than this; <= 0 = no limit
	Check   VersionCheck // nil = Exact
	Hooks   []Hook
	// Lock, when set, is held instead of the pipeline's own lock, so other
	// writers of the root (the live process's watcher) can share it; see
	// RootLock.
	Lock sync.Locker

	mu sync.Mutex
}

func (p *Pipeline) locker() sync.Locker {
	if p.Lock != nil {
		return p.Lock
	}
	return &p.mu
}

var rootLocks sync.Map // canonical root path -> *Lock

// RootLock returns the process-wide lock serialising writes to root: every
// mutation (file write, re-index, history) and every watcher batch.
func RootLock(root string) *Lock {
	l, _ := rootLocks.LoadOrStore(root, &Lock{})
	return l.(*Lock)
}

// Lock is a mutex with deferred work: jobs queued with Defer while holding it
// run, in order, under the lock before the next holder proceeds, or soon after
// Unlock in the background. Readers call Sync to see their effects.
type Lock struct {
	mu      sync.Mutex
	pmu     sync.Mutex
	pending []func()
	unrun   int // jobs queued and not yet finished
}

// Lock acquires l and runs the jobs deferred by earlier holders.
func (l *Lock) Lock() {
	l.mu.Lock()
	for {
		l.pmu.Lock()
		jobs := l.pending
		l.pending = nil
		l.pmu.Unlock()
		if len(jobs) == 0 {
			return
		}
		for _, j := range jobs {
			j()
			l.pmu.Lock()
			l.unrun--
			l.pmu.Unlock()
		}
	}
}

// Unlock releases l; deferred jobs then run in the background.
func (l *Lock) Unlock() {
	l.mu.Unlock()
	if l.Pending() {
		go l.Sync()
	}
}

// Defer queues job to run under the lock after the current holder, who
// must hold l, releases it.
func (l *Lock) Defer(job func()) {
	l.pmu.Lock()
	l.pending = append(l.pending, job)
	l.unrun++
	l.pmu.Unlock()
}

// Pending reports whether deferred jobs are queued or still running.
func (l *Lock) Pending() bool {
	l.pmu.Lock()
	defer l.pmu.Unlock()
	return l.unrun > 0
}

// Sync waits until the jobs deferred so far have run. It is free when none
// are pending.
func (l *Lock) Sync() {
	if l.Pending() {
		l.Lock()
		l.mu.Unlock()
	}
}

// Run loads the file, checks --v, resolves the op's target, applies it,
// saves atomically and runs the hooks.
func (p *Pipeline) Run(ctx context.Context, req Request) (*Result, error) {
	if err := ValidateV(req.Path, req.V); err != nil {
		return nil, err
	}
	l := p.locker()
	l.Lock()
	defer l.Unlock()

	f, err := fileio.LoadFile(p.Root, req.Cwd, req.Path, p.MaxSize)
	if err != nil {
		return nil, err
	}
	target, err := req.Op.Target(f.Lines())
	if err != nil {
		return nil, withPath(err, f.Path)
	}
	check := p.Check
	if check == nil {
		check = Exact
	}
	if err := check(ctx, f, req.V, req.Op, target); err != nil {
		return nil, withPath(err, f.Path)
	}

	doc := f.Doc
	if len(doc.Lines) == 0 {
		doc.Format.FinalNewline = true // an empty file has no style yet
	}
	changed, err := req.Op.Apply(doc, target)
	if err != nil {
		return nil, withPath(err, f.Path)
	}
	after := doc.Bytes()
	res := Result{
		Path:    f.Path,
		Op:      req.Op.Name(),
		By:      req.By,
		OldV:    version.Of(f.Data),
		NewV:    version.Of(after),
		Target:  target,
		Changed: changed,
		Total:   len(doc.Lines),
	}
	if string(after) == string(f.Data) {
		res.Unchanged = true
		return &res, nil
	}
	if err := fileio.WriteAtomic(f.Real, after, f.Mode); err != nil {
		return nil, err
	}
	c := &Commit{Result: res, Real: f.Real, Before: f.Data, After: after, Doc: doc}
	for _, h := range p.Hooks {
		if err := h(ctx, c); err != nil {
			return &res, err
		}
	}
	return &res, nil
}

// ValidateV checks that --v was given and is well formed: missing is a
// conflict, malformed is usage.
func ValidateV(path, v string) error {
	if v == "" {
		return outcome.New(outcome.Conflict, "%s: --v is required", path).
			WithHint("read the file first: lino read " + path + " --anchors")
	}
	if !version.Valid(v) {
		return outcome.New(outcome.Usage, "invalid --v %q: want %d hex characters", v, version.Len)
	}
	return nil
}

// Exact accepts only when the file is still at version v. On a mismatch the
// conflict carries the current lines around target.
func Exact(_ context.Context, f *fileio.File, v string, _ Op, target Range) error {
	cur := version.Of(f.Data)
	if cur == v {
		return nil
	}
	return Conflict(f, v, target)
}

// Conflict returns the conflict error for f read at stale version v.
func Conflict(f *fileio.File, v string, target Range) *outcome.Error {
	cur := version.Of(f.Data)
	lo, hi := target.Start, target.End
	if hi < lo {
		hi = lo
	}
	e := outcome.New(outcome.Conflict, "%s changed since v=%s (now v=%s)", f.Path, v, cur).
		WithHint("re-read the lines below and retry with --v " + cur)
	return e.WithLines(f.Path, anchor.Region(f.Lines(), lo, hi))
}

// Content parses stdin for a content-taking op. Empty input is a usage error:
// deletion is always an explicit command.
func Content(b []byte) ([]string, error) {
	if len(b) == 0 {
		return nil, outcome.New(outcome.Usage, "no content on stdin").
			WithHint("pass new lines with a heredoc; use lino delete to remove lines")
	}
	return textfile.SplitInput(b), nil
}

func withPath(err error, path string) error {
	if e, ok := outcome.As(err); ok && e.Path == "" && len(e.Lines) > 0 {
		e.Path = path
	}
	return err
}

// Exclusive runs fn holding the pipeline's lock, for mutations that do not go
// through Run or Move (write, rm). fn may call RunHooks.
func (p *Pipeline) Exclusive(fn func() error) error {
	l := p.locker()
	l.Lock()
	defer l.Unlock()
	return fn()
}

// RunHooks runs the pipeline's hooks for c, stopping at the first error.
func (p *Pipeline) RunHooks(ctx context.Context, c *Commit) error {
	for _, h := range p.Hooks {
		if err := h(ctx, c); err != nil {
			return err
		}
	}
	return nil
}
