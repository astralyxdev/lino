package changescmd

import (
	"context"
	"sync"

	"github.com/astralyx/lino/internal/changelog"
	"github.com/astralyx/lino/internal/live"
)

// starts holds the change-log position of each live process at startup,
// after the startup reconcile recorded the edits made while it was down.
var starts sync.Map // *live.Process -> int64

func init() {
	live.OnStart = append(live.OnStart, recordStart)
	live.OnStop = append(live.OnStop, func(p *live.Process) { starts.Delete(p) })
}

func recordStart(p *live.Process) {
	ctx := context.Background()
	log, err := changelog.Open(ctx, p.DB)
	if err != nil {
		return
	}
	if n, err := log.Latest(ctx); err == nil {
		starts.Store(p, n)
	}
}

// StartSeq is the change-log position recorded when p started: `changes`
// without --since lists everything after it.
func StartSeq(p *live.Process) (int64, bool) {
	v, ok := starts.Load(p)
	if !ok {
		return 0, false
	}
	return v.(int64), true
}
