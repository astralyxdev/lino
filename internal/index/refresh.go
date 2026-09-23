package index

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

type watchedKey struct{}

// WithWatched marks ctx as served by a live process whose watcher keeps the
// index fresh, so callers skip the stat checks of direct mode.
func WithWatched(ctx context.Context) context.Context {
	return context.WithValue(ctx, watchedKey{}, true)
}

// Watched reports whether ctx was marked by WithWatched.
func Watched(ctx context.Context) bool {
	w, _ := ctx.Value(watchedKey{}).(bool)
	return w
}

// Refresh stats each indexed path under root and re-indexes those whose size
// or mtime differ from the index; vanished files are removed. Paths that are
// not indexed are left alone. It returns the updates that changed something.
func (d *DB) Refresh(ctx context.Context, root string, rels []string, maxSize int64) (changed []Update, err error) {
	defer func() { d.ReportExternal(ctx, changed) }()
	seen := make(map[string]bool, len(rels))
	for _, rel := range rels {
		if seen[rel] {
			continue
		}
		seen[rel] = true
		var size, mtime int64
		err = d.SQL.QueryRowContext(ctx, `SELECT size, mtime FROM files WHERE path = ?`, rel).Scan(&size, &mtime)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return changed, err
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		st, err := os.Stat(abs)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return changed, err
		}
		if err == nil && st.Mode().IsRegular() && st.Size() == size && st.ModTime().UnixNano() == mtime {
			continue
		}
		u, err := d.IndexFile(ctx, rel, abs, maxSize)
		if err != nil {
			return changed, err
		}
		if u.Op != Unchanged {
			changed = append(changed, u)
		}
	}
	return changed, nil
}
