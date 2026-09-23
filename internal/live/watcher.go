package live

import (
	"context"
	"fmt"

	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/watch"
	"github.com/astralyx/lino/internal/watchsync"
)

// OnExternal, when set, receives the index changes the watcher applied for
// edits made outside lino (for the change log and history).
var OnExternal func(ctx context.Context, p *Process, ups []index.Update)

type watcher struct {
	w      *watch.Watcher
	sync   *watchsync.Syncer
	cancel context.CancelFunc
	done   chan struct{}
	err    error // why the watcher could not start
}

// startWatcher watches the root and keeps the index fresh. Failure to start
// is not fatal: the process then serves like direct mode (stat refresh).
func (p *Process) startWatcher() {
	w, err := watch.New(p.Rules)
	if err != nil {
		p.watch = &watcher{err: err}
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	pw := &watcher{w: w, cancel: cancel, done: make(chan struct{})}
	pw.sync = watchsync.New(p.DB, p.Rules, watchsync.Options{
		MaxSize: p.Config.MaxFileSize,
		OnUpdate: func(ctx context.Context, ups []index.Update) {
			if OnExternal != nil {
				OnExternal(ctx, p, ups)
			}
		},
	})
	p.watch = pw
	go func() {
		defer close(pw.done)
		pw.sync.Run(ctx, w.Events())
	}()
	p.SetWatching(true)
}

// stopWatcher stops the watcher and waits for the syncer to finish.
func (p *Process) stopWatcher() {
	pw := p.watch
	if pw == nil || pw.w == nil {
		return
	}
	p.SetWatching(false)
	pw.w.Close()
	pw.cancel()
	<-pw.done
}

// WatchState describes the watcher for status and returns the number of
// paths waiting to be re-indexed.
func (p *Process) WatchState() (state string, pending int) {
	pw := p.watch
	switch {
	case pw == nil:
		return "not started", 0
	case pw.err != nil:
		return "failed: " + pw.err.Error(), 0
	}
	ws, ss := pw.w.Stats(), pw.sync.Stats()
	state = fmt.Sprintf("%s, %d dirs", ws.Backend, ws.Dirs)
	if ws.Failed > 0 {
		state += fmt.Sprintf(", %d dirs unwatched (limit %d): %s", ws.Failed, ws.Limit, ws.LastError)
	}
	if ss.LastError != "" {
		state += ", last error: " + ss.LastError
	}
	return state, ss.Pending
}

// WatchStats returns the raw watcher and syncer stats; ok is false when no
// watcher runs.
func (p *Process) WatchStats() (w watch.Stats, s watchsync.Stats, ok bool) {
	pw := p.watch
	if pw == nil || pw.w == nil {
		return w, s, false
	}
	return pw.w.Stats(), pw.sync.Stats(), true
}

// WaitSynced blocks until the watcher has nothing pending or ctx is done.
func (p *Process) WaitSynced(ctx context.Context) error {
	pw := p.watch
	if pw == nil || pw.sync == nil {
		return nil
	}
	select {
	case <-pw.sync.Idle():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
