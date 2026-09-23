package index

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestConcurrentWriters: the live process re-indexes a file from the own-edit
// hook and from the watcher on different pool connections at the same time.
// A read-then-write transaction must wait for the other writer, not fail
// with SQLITE_BUSY.
func TestConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	root := newRoot(t)
	db := mustOpen(t, root)
	defer db.Close()
	other := mustOpen(t, root) // a second handle, like a direct-mode reader/writer
	defer other.Close()

	const writers, rounds = 4, 40
	var wg sync.WaitGroup
	errs := make(chan error, 2*writers*rounds)
	for w := range writers {
		d := db
		if w%2 == 1 {
			d = other
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range rounds {
				data := fmt.Appendf(nil, "package p\n// writer %d round %d\n", w, i)
				if _, err := d.IndexData(ctx, "a.go", data, Stat{Size: int64(len(data)), ModTime: time.Now()}); err != nil {
					errs <- err
				}
				if _, err := d.IndexData(ctx, fmt.Sprintf("w%d.go", w), data, Stat{Size: int64(len(data)), ModTime: time.Now()}); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM files`); n != writers+1 {
		t.Fatalf("files = %d, want %d", n, writers+1)
	}
}
