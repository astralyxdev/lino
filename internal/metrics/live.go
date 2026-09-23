package metrics

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/live"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/proto"
	"github.com/astralyx/lino/internal/reindex"
	"github.com/astralyx/lino/internal/server"
)

// SaveEvery is how often a live process writes its stats file.
var SaveEvery = 30 * time.Second

type running struct {
	c    *Collector
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
	live.Middleware = append(live.Middleware, middleware)
	reindex.Observers = append(reindex.Observers, observeReindex)
	prev := live.OnExternal
	live.OnExternal = func(ctx context.Context, p *live.Process, ups []index.Update) {
		if prev != nil {
			prev(ctx, p, ups)
		}
		if c := For(p); c != nil {
			c.AddExternal(len(ups))
		}
	}
}

// For returns the collector of a live process, or nil.
func For(p *live.Process) *Collector {
	mu.Lock()
	defer mu.Unlock()
	if r := byProc[p]; r != nil {
		return r.c
	}
	return nil
}

func start(p *live.Process) {
	r := &running{c: Load(Path(p.Root)), stop: make(chan struct{}), done: make(chan struct{})}
	r.c.AddStart()
	mu.Lock()
	byProc[p] = r
	mu.Unlock()

	go func() {
		defer close(r.done)
		tick := time.NewTicker(SaveEvery)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				r.c.Save()
			case <-r.stop:
				return
			}
		}
	}()
}

// observeReindex records an own-edit re-index with the live process of root.
func observeReindex(root string, d time.Duration) {
	mu.Lock()
	defer mu.Unlock()
	for p, r := range byProc {
		if p.Root == root {
			r.c.ObserveReindex(d)
		}
	}
}

func stop(p *live.Process) {
	mu.Lock()
	r := byProc[p]
	delete(byProc, p)
	mu.Unlock()
	if r == nil {
		return
	}
	close(r.stop)
	<-r.done
	r.c.Save()
}

func middleware(p *live.Process, h server.Handler) server.Handler {
	c := For(p)
	if c == nil {
		return h
	}
	return func(ctx context.Context, req *proto.Request) (output.Result, error) {
		t0 := time.Now()
		var waited atomic.Int64
		res, err := h(context.WithValue(ctx, waitKey{}, &waited), req)
		o := res.Outcome
		if err != nil {
			o = outcome.Of(err)
		}
		c.Observe(req.Command, o, max(time.Since(t0)-time.Duration(waited.Load()), 0))
		return res, err
	}
}

type waitKey struct{}

// Waited records time a call spent idle by request, such as `changes --wait`
// holding for events; it is left out of the call's latency.
func Waited(ctx context.Context, d time.Duration) {
	if w, _ := ctx.Value(waitKey{}).(*atomic.Int64); w != nil {
		w.Add(int64(d))
	}
}
