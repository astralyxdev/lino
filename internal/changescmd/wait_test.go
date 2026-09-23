package changescmd

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/changelog"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/outcome"
)

func entry(path string) changelog.Entry {
	return changelog.Entry{Source: changelog.SourceExternal, Op: "external", Path: path,
		Kind: changelog.Modified, VBefore: "aaaaaa", VAfter: "bbbbbb", Ranges: []changelog.Range{rng(1, 1)}}
}

// appendLater appends entries one by one, gap apart, through its own Log
// handle (sharing the process notifier, as the live process does).
func appendLater(t *testing.T, root string, gap time.Duration, paths ...string) {
	t.Helper()
	ctx := context.Background()
	// The notifier is keyed by index path; Run opens the canonical root.
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := index.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	l, err := changelog.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer db.Close()
		for _, p := range paths {
			time.Sleep(gap)
			if _, err := l.Append(ctx, entry(p)); err != nil {
				t.Error(err)
				return
			}
		}
	}()
}

func TestWait(t *testing.T) {
	old := PollInterval
	PollInterval = time.Hour // prove the notifier wakes waiters, not the poll
	defer func() { PollInterval = old }()

	tests := []struct {
		name     string
		appends  []string
		paths    []string
		wait     time.Duration
		outcome  outcome.Outcome
		wantSeqs []int64
		next     int64
		minTime  time.Duration
		maxTime  time.Duration
	}{
		{"wakes on change", []string{"a/x.go"}, nil, 5 * time.Second, outcome.OK, []int64{2}, 2, 0, 2 * time.Second},
		{"ignores non-matching", []string{"b/y.go", "b/z.go", "a/x.go"}, []string{"a/**"}, 5 * time.Second, outcome.OK, []int64{4}, 4, 0, 2 * time.Second},
		{"times out", nil, nil, 150 * time.Millisecond, outcome.Empty, nil, 1, 150 * time.Millisecond, 2 * time.Second},
		{"times out on non-matching", []string{"b/y.go"}, []string{"a/**"}, 300 * time.Millisecond, outcome.Empty, nil, 2, 300 * time.Millisecond, 2 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRoot(t, "", []changelog.Entry{entry("seed.txt")})
			appendLater(t, root, 50*time.Millisecond, tt.appends...)
			start := time.Now()
			res, err := Run(context.Background(), root, Request{Since: 1, Paths: tt.paths, Wait: tt.wait})
			took := time.Since(start)
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome == "" {
				res.Outcome = outcome.OK
			}
			if res.Outcome != tt.outcome {
				t.Fatalf("outcome %q, want %q", res.Outcome, tt.outcome)
			}
			d := res.Data.(Data)
			var seqs []int64
			for _, e := range d.Events {
				seqs = append(seqs, e.Seq)
			}
			if len(seqs) != len(tt.wantSeqs) || (len(seqs) > 0 && seqs[0] != tt.wantSeqs[0]) {
				t.Fatalf("events %v, want %v", seqs, tt.wantSeqs)
			}
			if d.Next != tt.next {
				t.Fatalf("next %d, want %d", d.Next, tt.next)
			}
			if took < tt.minTime || took > tt.maxTime {
				t.Fatalf("took %v, want %v..%v", took, tt.minTime, tt.maxTime)
			}
		})
	}
}

func TestWaitPoll(t *testing.T) {
	old := PollInterval
	PollInterval = 20 * time.Millisecond
	defer func() { PollInterval = old }()
	root := newRoot(t, "", []changelog.Entry{entry("seed.txt")})
	ctx := context.Background()
	db, err := index.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	l, err := changelog.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	// An append in another process does not notify; simulate it with AppendTx
	// and no Notify call.
	go func() {
		time.Sleep(50 * time.Millisecond)
		tx, err := db.SQL.BeginTx(ctx, nil)
		if err != nil {
			t.Error(err)
			return
		}
		if _, err := l.AppendTx(ctx, tx, entry("x.go")); err != nil {
			t.Error(err)
		}
		if err := tx.Commit(); err != nil {
			t.Error(err)
		}
	}()
	res, err := Run(ctx, root, Request{Since: 1, Wait: 5 * time.Second})
	if err != nil || res.Outcome != "" || len(res.Data.(Data).Events) != 1 {
		t.Fatalf("got %+v, %v", res, err)
	}
}

func TestWaitAlreadyThere(t *testing.T) {
	root := newRoot(t, "", []changelog.Entry{entry("a.txt"), entry("b.txt")})
	start := time.Now()
	res, err := Run(context.Background(), root, Request{Since: 1, Wait: 10 * time.Second})
	if err != nil || len(res.Data.(Data).Events) != 1 || time.Since(start) > time.Second {
		t.Fatalf("got %+v, %v after %v", res, err, time.Since(start))
	}
}

func TestWaitCancelled(t *testing.T) {
	root := newRoot(t, "", nil)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := Run(ctx, root, Request{Since: 0, Wait: 10 * time.Second}); err == nil {
		t.Fatal("expected an error after cancel")
	}
}

func TestWaitFlag(t *testing.T) {
	root := newRoot(t, "", nil)
	tests := []struct {
		wait string
		code int
	}{
		{"bogus", 2},
		{"-1s", 2},
		{"100ms", 0},
		{"0", 0},
	}
	for _, tt := range tests {
		if _, errOut, code := run(root, "changes", "--since", "0", "--wait", tt.wait); code != tt.code {
			t.Errorf("--wait %s: exit %d, want %d (%s)", tt.wait, code, tt.code, errOut)
		}
	}
}
