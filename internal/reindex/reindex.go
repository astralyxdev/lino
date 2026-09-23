// Package reindex updates the index after lino's own writes, so a search
// right after an edit sees it: synchronously in direct mode, deferred until
// the next reader in a live process. It registers itself as a file command
// hook on import.
package reindex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/astralyx/lino/internal/config"
	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/ignore"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/paths"
)

func init() {
	filecmd.Hooks = append(filecmd.Hooks, Hook)
	filecmd.PreHooks = append(filecmd.PreHooks, Refresh)
}

// Target is what the hook needs to update one root's index.
type Target struct {
	DB      *index.DB
	Rules   *ignore.Rules
	MaxSize int64 // files larger than this are indexed by hash only; <= 0 = no limit
	// Defer, when set, queues the re-index to run after the mutation returns
	// (see mutate.Lock); readers must sync with it first. nil re-indexes now.
	Defer func(job func())
	// Sync waits for the deferred re-indexes queued so far; nil when Defer is.
	Sync func()
	// OnError receives failures of deferred re-indexes.
	OnError func(err error)
}

// Opener returns the index target for a canonical root, or nil when the root
// has no index. release is called when the hook is done with it.
type Opener func(ctx context.Context, root string) (t *Target, release func(), err error)

// Open is the opener the hook uses. The default opens the index per call
// (direct mode); a live process replaces it with one returning its open index.
var Open Opener = Direct

// Direct opens <root>/.lino/index.db with fresh ignore rules and the root's
// configured size limit. An index that is missing (deleted, or never built)
// is built with a full reconcile first, as search and ls do, so the change
// log and history, which live beside it, record the mutation.
func Direct(ctx context.Context, root string) (*Target, func(), error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, nil, err
	}
	rules, err := ignore.NewRules(root)
	if err != nil {
		return nil, nil, err
	}
	db, err := index.Open(ctx, root)
	if err != nil {
		return nil, nil, err
	}
	if db.Rebuilt {
		if _, err := db.Reconcile(ctx, rules, cfg.MaxFileSize); err != nil {
			db.Close()
			return nil, nil, err
		}
	}
	return &Target{DB: db, Rules: rules, MaxSize: cfg.MaxFileSize}, func() { db.Close() }, nil
}

// Observers are told how long each successful own-edit re-index took
// (opening the index included), e.g. for stats.
var Observers []func(root string, d time.Duration)

// Hook re-indexes the file a mutation touched, before the command returns or,
// for a target with Defer, right after it.
func Hook(ctx context.Context, c *mutate.Commit) error {
	root, ok := findRoot(c.Real)
	if !ok {
		return nil
	}
	t0 := time.Now()
	t, release, err := Open(ctx, root)
	if err != nil || t == nil {
		return err
	}
	if t.Defer != nil {
		// The job must not read the disk: by the time it runs, an external
		// edit may have landed there and would be indexed as this one.
		if st, ok := snapshot(c); ok {
			ctx = context.WithoutCancel(ctx)
			t.Defer(func() {
				defer release()
				if err := apply(ctx, t, root, c, st, time.Now()); err != nil && t.OnError != nil {
					t.OnError(err)
				}
			})
			return nil
		}
	}
	defer release()
	return apply(ctx, t, root, c, nil, t0)
}

// snapshot returns what a deferred re-index of c needs from disk, taken now;
// ok is false when the re-index has to read the file.
func snapshot(c *mutate.Commit) (st *index.Stat, ok bool) {
	switch {
	case c.Op == "mv" || c.From != "":
		return nil, false
	case c.Op == "rm" || c.Removed:
		return nil, true
	case c.After != nil:
		fi, err := os.Stat(c.Real)
		if err != nil {
			return nil, false
		}
		return &index.Stat{Size: fi.Size(), ModTime: fi.ModTime(), Mode: fi.Mode().Perm()}, true
	}
	return nil, false
}

func apply(ctx context.Context, t *Target, root string, c *mutate.Commit, st *index.Stat, t0 time.Time) error {
	if err := applyStat(ctx, t, root, c, st); err != nil {
		return err
	}
	for _, o := range Observers {
		o(root, time.Since(t0))
	}
	return nil
}

// Apply updates t for commit c in root.
func Apply(ctx context.Context, t *Target, root string, c *mutate.Commit) error {
	return applyStat(ctx, t, root, c, nil)
}

// applyStat is Apply with the stat of c.After's file taken earlier, or nil
// to stat it now.
func applyStat(ctx context.Context, t *Target, root string, c *mutate.Commit, st *index.Stat) error {
	rel, err := filepath.Rel(root, c.Real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	rel = filepath.ToSlash(rel)
	ignored := t.Rules != nil && t.Rules.Ignored(rel, false)

	switch {
	case c.Op == "mv" || c.From != "":
		if ignored {
			_, err = t.DB.RemoveFile(ctx, c.From)
			return err
		}
		u, err := t.DB.MoveFile(ctx, c.From, rel, c.Real, t.MaxSize)
		if err != nil {
			return err
		}
		// The row moved as indexed; if it was stale, read the file.
		if c.OldV != "" && !strings.HasPrefix(u.Hash, c.OldV) {
			_, err = t.DB.IndexFile(ctx, rel, c.Real, t.MaxSize)
		}
		return err
	case c.Op == "rm" || c.Removed || ignored:
		_, err = t.DB.RemoveFile(ctx, rel)
		return err
	case c.After != nil:
		if st == nil {
			fi, err := os.Stat(c.Real)
			if err != nil {
				_, err = t.DB.IndexFile(ctx, rel, c.Real, t.MaxSize)
				return err
			}
			st = &index.Stat{Size: fi.Size(), ModTime: fi.ModTime(), Mode: fi.Mode().Perm()}
		}
		_, err = t.DB.IndexData(ctx, rel, c.After, *st)
		return err
	default:
		_, err = t.DB.IndexFile(ctx, rel, c.Real, t.MaxSize)
		return err
	}
}

// findRoot returns the nearest ancestor of p holding a .lino directory.
// Nested roots are refused at init, so the nearest one is the root.
func findRoot(p string) (string, bool) {
	for dir := filepath.Dir(p); ; {
		if paths.IsRoot(dir) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// Refresh re-indexes rels whose size or mtime changed on disk, so an external
// edit made before a direct-mode mutation is recorded as its own change first.
// A live process's watcher already does this, so watched requests skip it.
func Refresh(ctx context.Context, root string, rels []string) error {
	if index.Watched(ctx) {
		return nil
	}
	t, release, err := Open(ctx, root)
	if err != nil || t == nil {
		return err
	}
	defer release()
	if t.Sync != nil {
		// A pending own edit must not look like an external one.
		t.Sync()
	}
	_, err = t.DB.Refresh(ctx, root, rels, t.MaxSize)
	return err
}
