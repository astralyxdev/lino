// Package watchsync turns raw watcher events into index updates. Events are
// debounced and batched; each batch is reconciled against disk by hash, not
// replayed. Rescan events and bulk batches trigger a full reconcile.
package watchsync

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/astralyx/lino/internal/ignore"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/watch"
)

// Options tune batching. Zero values take the defaults.
type Options struct {
	Quiet    time.Duration // flush after this long without events (default 150ms)
	MaxDelay time.Duration // flush at most this long after a batch's first event (default 500ms)
	Bulk     int           // more paths than this in one batch => full reconcile (default 1000)
	MaxSize  int64         // files over this are indexed by hash only; <= 0 = no limit

	// OnUpdate receives every index change a batch made, in path order.
	OnUpdate func(ctx context.Context, ups []index.Update)
	// OnError receives batch failures; the paths are retried with the next batch.
	OnError func(err error)
}

// Stats describe the syncer for `lino status`.
type Stats struct {
	Pending    int       `json:"pending"`         // paths queued or being re-indexed
	Batches    int64     `json:"batches"`         // path batches reconciled
	Full       int64     `json:"full_reconciles"` // full reconciles run
	LastReason string    `json:"last_full_reason,omitempty"`
	LastSync   time.Time `json:"last_sync,omitzero"`
	LastError  string    `json:"last_error,omitempty"`
}

// Syncer applies watcher events to an index.
type Syncer struct {
	db    *index.DB
	rules *ignore.Rules
	opt   Options

	mu       sync.Mutex
	queued   map[string]bool
	rescan   string
	inflight int
	stats    Stats
	idle     chan struct{} // closed while nothing is pending
}

// New returns a syncer for db whose files live under rules.Root().
func New(db *index.DB, rules *ignore.Rules, opt Options) *Syncer {
	if opt.Quiet <= 0 {
		opt.Quiet = 150 * time.Millisecond
	}
	if opt.MaxDelay <= 0 {
		opt.MaxDelay = 500 * time.Millisecond
	}
	if opt.Bulk <= 0 {
		opt.Bulk = 1000
	}
	idle := make(chan struct{})
	close(idle)
	return &Syncer{db: db, rules: rules, opt: opt, queued: map[string]bool{}, idle: idle}
}

// Pending returns the number of paths waiting to be re-indexed.
func (s *Syncer) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingLocked()
}

func (s *Syncer) pendingLocked() int {
	n := len(s.queued) + s.inflight
	if n == 0 && s.rescan != "" {
		n = 1
	}
	return n
}

// Stats returns a snapshot of the syncer state.
func (s *Syncer) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stats
	st.Pending = s.pendingLocked()
	return st
}

// Idle returns a channel that is closed once nothing is pending.
func (s *Syncer) Idle() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idle
}

// Run consumes events until the channel closes (flushing what is queued) or
// ctx is done.
func (s *Syncer) Run(ctx context.Context, events <-chan watch.Event) {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	var first time.Time
	armed := false
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				s.Flush(ctx)
				return
			}
			s.Add(ev)
			now := time.Now()
			if !armed {
				first, armed = now, true
			}
			wait := min(s.opt.Quiet, first.Add(s.opt.MaxDelay).Sub(now))
			timer.Reset(max(wait, 0))
		case <-timer.C:
			armed = false
			s.Flush(ctx)
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}

// Add queues one event without flushing.
func (s *Syncer) Add(ev watch.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case ev.Rescan:
		reason := ev.Reason
		if reason == "" {
			reason = "rescan requested"
		}
		s.rescan = reason
	case ev.Rel == "":
		return
	case ev.Dir && ev.Op&(watch.Remove|watch.Rename) == 0:
		// New directories: the watcher reports their files separately.
		return
	default:
		s.queued[ev.Rel] = true
	}
	s.markBusy()
}

func (s *Syncer) markBusy() {
	select {
	case <-s.idle:
		s.idle = make(chan struct{})
	default:
	}
}

// Flush reconciles everything queued now.
func (s *Syncer) Flush(ctx context.Context) {
	s.mu.Lock()
	paths := make([]string, 0, len(s.queued))
	for p := range s.queued {
		paths = append(paths, p)
	}
	s.queued = map[string]bool{}
	rescan := s.rescan
	s.rescan = ""
	if rescan == "" && len(paths) > s.opt.Bulk {
		rescan = "bulk change"
	}
	s.inflight = len(paths)
	if rescan != "" && s.inflight == 0 {
		s.inflight = 1
	}
	s.mu.Unlock()

	var (
		ups []index.Update
		err error
	)
	if rescan != "" {
		var sum index.Summary
		sum, err = s.db.Reconcile(ctx, s.rules, s.opt.MaxSize)
		ups = sum.Changes
	} else if len(paths) > 0 {
		ups, err = s.syncPaths(ctx, paths)
	}

	s.mu.Lock()
	s.inflight = 0
	now := time.Now()
	switch {
	case err != nil:
		s.stats.LastError = err.Error()
		if ctx.Err() == nil {
			// Retry with the next batch.
			if rescan != "" {
				s.rescan = rescan
			}
			for _, p := range paths {
				s.queued[p] = true
			}
		}
	case rescan != "":
		s.stats.Full++
		s.stats.LastReason = rescan
		s.stats.LastSync = now
	case len(paths) > 0:
		s.stats.Batches++
		s.stats.LastSync = now
	}
	if s.pendingLocked() == 0 {
		select {
		case <-s.idle:
		default:
			close(s.idle)
		}
	}
	s.mu.Unlock()

	if err != nil && s.opt.OnError != nil {
		s.opt.OnError(err)
	}
	if len(ups) > 0 && s.opt.OnUpdate != nil {
		s.opt.OnUpdate(ctx, ups)
	}
}

// onDisk is what a queued path is now.
type onDisk struct {
	rel     string
	abs     string // "" when the path is not an indexable file
	indexed bool
	hash    string // indexed hash
}

// syncPaths re-indexes paths by hash. A vanished indexed file and a new file
// with the same content in one batch are recorded as a move.
func (s *Syncer) syncPaths(ctx context.Context, paths []string) ([]index.Update, error) {
	root := s.rules.Root()
	seen := map[string]bool{}
	var items []onDisk
	var addPath func(rel string) error
	addPath = func(rel string) error {
		if seen[rel] {
			return nil
		}
		seen[rel] = true
		fi, ok, err := s.db.File(ctx, rel)
		if err != nil {
			return err
		}
		it := onDisk{rel: rel, abs: s.resolve(root, rel), indexed: ok, hash: fi.Hash}
		items = append(items, it)
		if it.abs == "" {
			// A removed or renamed directory takes its indexed files with it.
			under, err := s.db.PathsUnder(ctx, rel)
			if err != nil {
				return err
			}
			for _, u := range under {
				if err := addPath(u); err != nil {
					return err
				}
			}
		}
		return nil
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := addPath(p); err != nil {
			return nil, err
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].rel < items[j].rel })

	gone := map[string][]string{} // indexed hash -> vanished paths
	for _, it := range items {
		if it.abs == "" && it.indexed {
			gone[it.hash] = append(gone[it.hash], it.rel)
		}
	}
	moved := map[string]bool{}
	var ups []index.Update
	record := func(u index.Update, err error) error {
		if err != nil {
			return err
		}
		if u.Op != index.Unchanged {
			ups = append(ups, u)
		}
		return nil
	}
	for _, it := range items {
		if it.abs == "" {
			continue
		}
		if !it.indexed && len(gone) > 0 {
			if h, err := hashFile(it.abs); err == nil && len(gone[h]) > 0 {
				from := gone[h][0]
				gone[h] = gone[h][1:]
				moved[from] = true
				if err := record(s.db.MoveFile(ctx, from, it.rel, it.abs, s.opt.MaxSize)); err != nil {
					return ups, err
				}
				continue
			}
		}
		if err := record(s.db.IndexFile(ctx, it.rel, it.abs, s.opt.MaxSize)); err != nil {
			return ups, err
		}
	}
	for _, it := range items {
		if it.abs == "" && it.indexed && !moved[it.rel] {
			if err := record(s.db.RemoveFile(ctx, it.rel)); err != nil {
				return ups, err
			}
		}
	}
	sort.SliceStable(ups, func(i, j int) bool { return ups[i].Path < ups[j].Path })
	return ups, nil
}

// resolve returns the file to read for rel, following the same rules as the
// full walk, or "" when rel is not an indexable file.
func (s *Syncer) resolve(root, rel string) string {
	if rel == "" || rel == ".lino" || strings.HasPrefix(rel, ".lino/") || s.rules.Ignored(rel, false) {
		return ""
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	st, err := os.Lstat(abs)
	if err != nil {
		return ""
	}
	switch {
	case st.Mode().IsRegular():
		return abs
	case st.Mode()&fs.ModeSymlink != 0:
		target, err := filepath.EvalSymlinks(abs)
		if err != nil || !inside(root, target) {
			return ""
		}
		if trel, err := filepath.Rel(root, target); err != nil || s.rules.Ignored(filepath.ToSlash(trel), false) {
			return ""
		}
		if ts, err := os.Stat(target); err != nil || !ts.Mode().IsRegular() {
			return ""
		}
		return target
	}
	return ""
}

func inside(root, p string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}

func hashFile(abs string) (string, error) {
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return index.Hash(b), nil
}
