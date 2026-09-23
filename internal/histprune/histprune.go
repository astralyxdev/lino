// Package histprune applies the history retention limits of .lino/config:
// periodically in the live process, and opportunistically (at most once per
// DirectEvery) after a mutation in direct mode.
package histprune

import (
	"context"
	"sync"
	"time"

	"github.com/astralyx/lino/internal/changelog"
	"github.com/astralyx/lino/internal/config"
	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/histrec"
	"github.com/astralyx/lino/internal/live"
	"github.com/astralyx/lino/internal/mutate"
)

var (
	// Every is how often a live process prunes, starting right after startup.
	Every = time.Hour
	// DirectEvery is the minimum interval between prunes run by direct-mode
	// mutations, tracked in history.db so separate processes share it.
	DirectEvery = time.Hour
)

type running struct {
	stop chan struct{}
	done chan struct{}
}

var (
	mu     sync.Mutex
	byProc = map[*live.Process]*running{}
)

func init() {
	live.OnStart = append(live.OnStart, start)
	live.OnStop = append(live.OnStop, stop)
	changelog.Recorders = append(changelog.Recorders, direct)
}

// Retention returns the history limits of c.
func Retention(c config.Config) history.Retention {
	return history.Retention{MaxAge: c.Retention, MaxSize: c.HistoryMaxSize}
}

// Run prunes the history of root under c now, then vacuums if worthwhile.
func Run(ctx context.Context, root string, c config.Config) (history.PruneResult, error) {
	st, err := histrec.Store(ctx, root)
	if err != nil {
		return history.PruneResult{}, err
	}
	res, _, err := st.MaybePrune(ctx, Retention(c), 0)
	if err != nil {
		return res, err
	}
	res.Vacuumed, err = st.Vacuum(ctx)
	return res, err
}

func start(p *live.Process) {
	r := &running{stop: make(chan struct{}), done: make(chan struct{})}
	mu.Lock()
	byProc[p] = r
	mu.Unlock()
	go func() {
		defer close(r.done)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			select {
			case <-r.stop:
				cancel()
			case <-ctx.Done():
			}
		}()
		tick := time.NewTicker(Every)
		defer tick.Stop()
		for {
			Run(ctx, p.Root, p.Config)
			select {
			case <-tick.C:
			case <-r.stop:
				return
			}
		}
	}()
}

func stop(p *live.Process) {
	mu.Lock()
	r := byProc[p]
	delete(byProc, p)
	mu.Unlock()
	if r != nil {
		close(r.stop)
		<-r.done
	}
}

// direct is a changelog.Recorder: in direct mode it prunes when the last
// prune is at least DirectEvery old. It never fails the mutation.
func direct(ctx context.Context, root string, _ int64, _ changelog.Entry, _ *mutate.Commit) (func(), error) {
	if live.FromContext(ctx) != nil {
		return nil, nil
	}
	c, err := config.Load(root)
	if err != nil {
		return nil, nil
	}
	st, err := histrec.Store(ctx, root)
	if err != nil {
		return nil, nil
	}
	st.MaybePrune(ctx, Retention(c), DirectEvery)
	return nil, nil
}
