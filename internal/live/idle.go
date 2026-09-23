package live

import (
	"context"
	"time"
)

// WatchIdle stops the process once it has served no request for d and none
// is in flight; a request that is still running, such as changes --wait,
// keeps it alive. d <= 0 never exits. It sets p.Idle and returns at once.
func (p *Process) WatchIdle(d time.Duration) {
	p.Idle = d
	if d <= 0 {
		return
	}
	go p.watchIdle(d)
}

func (p *Process) watchIdle(d time.Duration) {
	t := time.NewTicker(idleTick(d))
	defer t.Stop()
	srv := p.Server()
	for {
		select {
		case <-p.Done():
			return
		case <-t.C:
		}
		if srv.InFlight() > 0 || time.Since(srv.LastActivity()) < d {
			continue
		}
		p.idleExit.Store(true)
		ctx, cancel := context.WithTimeout(context.Background(), ShutdownGrace)
		p.Stop(ctx)
		cancel()
		return
	}
}

// IdleExited reports whether the process stopped because it was idle.
func (p *Process) IdleExited() bool { return p.idleExit.Load() }

// idleTick checks often enough that the exit lands within ~10% of d.
func idleTick(d time.Duration) time.Duration {
	return min(max(d/10, 20*time.Millisecond), 30*time.Second)
}
