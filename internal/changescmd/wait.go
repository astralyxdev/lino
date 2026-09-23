package changescmd

import (
	"context"
	"errors"
	"time"

	"github.com/astralyx/lino/internal/changelog"
	"github.com/astralyx/lino/internal/outcome"
)

// PollInterval is how often a waiting call re-reads the log head besides
// being woken by the in-process notifier. Appends made in this process (the
// live process: lino edits and the watcher) wake waiters at once; the poll
// covers writers in other processes, which is how direct mode waits.
var PollInterval = 200 * time.Millisecond

// wait blocks until an entry after pos matches globs, d passes, or ctx ends.
// changed must have been taken from log before pos was read. It returns the
// matching entries (none on timeout) and the log head it last saw.
func wait(ctx context.Context, log *changelog.Log, changed <-chan struct{}, pos int64, globs []string, limit int, d time.Duration) (evs []changelog.Entry, more bool, head int64, err error) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	tick := time.NewTicker(PollInterval)
	defer tick.Stop()
	head = pos
	for {
		select {
		case <-changed:
		case <-tick.C:
		case <-timer.C:
			return []changelog.Entry{}, false, head, nil
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return []changelog.Entry{}, false, head, nil
			}
			return nil, false, 0, outcome.Wrap(outcome.Internal, ctx.Err(), "changes --wait: "+ctx.Err().Error())
		}
		changed = log.Changed()
		latest, err := log.Latest(ctx)
		if err != nil {
			return nil, false, 0, outcome.Wrap(outcome.Internal, err, "")
		}
		if latest <= head {
			continue
		}
		evs, more, err := collect(ctx, log, head, globs, limit)
		if err != nil {
			return nil, false, 0, outcome.Wrap(outcome.Internal, err, "")
		}
		// collect read at least up to latest, so nothing before it matches.
		head = max(latest, lastSeq(evs, latest))
		if len(evs) > 0 {
			return evs, more, head, nil
		}
	}
}

func lastSeq(evs []changelog.Entry, def int64) int64 {
	if len(evs) == 0 {
		return def
	}
	return evs[len(evs)-1].Seq
}
