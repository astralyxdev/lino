package mutate

import (
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLockDeferred(t *testing.T) {
	tests := []struct {
		name string
		next func(l *Lock) // what follows the holder's Unlock
	}{
		{"next holder", func(l *Lock) { l.Lock(); l.Unlock() }},
		{"sync", func(l *Lock) { l.Sync() }},
		{"background", func(l *Lock) {
			for deadline := time.Now().Add(5 * time.Second); l.Pending() && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				l   Lock
				mu  sync.Mutex
				got []int
			)
			add := func(i int) func() {
				return func() { mu.Lock(); got = append(got, i); mu.Unlock() }
			}
			l.Lock()
			l.Defer(add(1))
			l.Defer(add(2))
			l.Unlock()
			tt.next(&l)
			l.Lock()
			l.Defer(add(3))
			l.Unlock()
			l.Sync()
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(got, []int{1, 2, 3}) {
				t.Errorf("jobs ran %v, want [1 2 3]", got)
			}
			if l.Pending() {
				t.Error("jobs still pending")
			}
		})
	}
}

func TestLockJobRunsBeforeNextHolder(t *testing.T) {
	var l Lock
	done := false
	l.Lock()
	l.Defer(func() { done = true })
	l.Unlock()
	l.Lock()
	ok := done
	l.Unlock()
	if !ok {
		t.Fatal("next holder ran before the deferred job")
	}
}

func TestLockSyncWaitsForRunningJob(t *testing.T) {
	var l Lock
	started, release := make(chan struct{}), make(chan struct{})
	var done atomic.Bool
	l.Lock()
	l.Defer(func() { close(started); <-release; done.Store(true) })
	l.Unlock() // the job starts in the background
	<-started
	synced := make(chan struct{})
	go func() { l.Sync(); close(synced) }()
	select {
	case <-synced:
		t.Fatal("Sync returned while the deferred job was running")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-synced
	if !done.Load() {
		t.Fatal("job not finished after Sync")
	}
}
