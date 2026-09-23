package history

import (
	"context"
	"sync"
	"testing"
)

// TestConcurrentAdd: two handles (live process and a second writer) adding
// changes at once must wait for each other, not fail with SQLITE_BUSY.
func TestConcurrentAdd(t *testing.T) {
	ctx := context.Background()
	s, root := newStore(t)
	other, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	const writers, rounds = 4, 30
	var wg sync.WaitGroup
	errs := make(chan error, writers*rounds)
	for w := range writers {
		st := s
		if w%2 == 1 {
			st = other
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				if _, err := st.Add(ctx, Change{Source: SourceLino, Path: "a", Op: OpEdit}); err != nil {
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
	var n int
	if err := s.SQL.QueryRow(`SELECT count(*) FROM changes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != writers*rounds {
		t.Fatalf("changes = %d, want %d", n, writers*rounds)
	}
}
