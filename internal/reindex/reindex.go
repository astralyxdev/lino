// Package reindex updates the index synchronously after lino's own writes, so
// a search right after an edit sees it. It registers itself as a file command
// hook on import.
package reindex

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/astralyx/lino/internal/config"
	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/fileio"
	"github.com/astralyx/lino/internal/ignore"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/mutate"
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
}

// Opener returns the index target for a canonical root, or nil when the root
// has no index. release is called when the hook is done with it.
type Opener func(ctx context.Context, root string) (t *Target, release func(), err error)

// Open is the opener the hook uses. The default opens the index per call
// (direct mode); a live process replaces it with one returning its open index.
var Open Opener = Direct

// Direct opens <root>/.lino/index.db if it exists, with fresh ignore rules and
// the root's configured size limit.
func Direct(ctx context.Context, root string) (*Target, func(), error) {
	if _, err := os.Stat(index.Path(root)); errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
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
	return &Target{DB: db, Rules: rules, MaxSize: cfg.MaxFileSize}, func() { db.Close() }, nil
}

// Observers are told how long each successful own-edit re-index took
// (opening the index included), e.g. for stats.
var Observers []func(root string, d time.Duration)

// Hook re-indexes the file a mutation touched, before the command returns.
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
	defer release()
	if err := Apply(ctx, t, root, c); err != nil {
		return err
	}
	for _, o := range Observers {
		o(root, time.Since(t0))
	}
	return nil
}

// Apply updates t for commit c in root.
func Apply(ctx context.Context, t *Target, root string, c *mutate.Commit) error {
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
		st, err := os.Stat(c.Real)
		if err != nil {
			_, err = t.DB.IndexFile(ctx, rel, c.Real, t.MaxSize)
			return err
		}
		_, err = t.DB.IndexData(ctx, rel, c.After, index.Stat{Size: st.Size(), ModTime: st.ModTime(), Mode: st.Mode().Perm()})
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
		if st, err := os.Stat(filepath.Join(dir, fileio.MetaDir)); err == nil && st.IsDir() {
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
	_, err = t.DB.Refresh(ctx, root, rels, t.MaxSize)
	return err
}
