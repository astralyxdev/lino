package histrec

import (
	"context"
	"path/filepath"

	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/textfile"
	"github.com/astralyx/lino/internal/vcache"
	"github.com/astralyx/lino/internal/version"
)

// Reconstruct returns path at version v, starting from the content the index
// holds now and undoing later changes from the root's history. In a live
// process, versions are served from its version cache when possible.
func Reconstruct(ctx context.Context, db *index.DB, path, v string) (history.Version, error) {
	root := filepath.Dir(filepath.Dir(db.Path))
	st, err := Store(ctx, root)
	if err != nil {
		return history.Version{}, err
	}
	vc := vcache.For(root)
	if vc == nil {
		return st.Reconstruct(ctx, Current(db), path, v)
	}
	if b, ok, err := Current(db)(ctx, path); err == nil && ok && version.Of(b) == v {
		return history.Version{Path: path, Content: b, Doc: textfile.Parse(b)}, nil
	}
	if ver, ok := cached(ctx, vc, st, path, v); ok {
		return ver, nil
	}
	asOf, err := st.Latest(ctx)
	if err != nil {
		return history.Version{}, err
	}
	ver, err := st.Reconstruct(ctx, Current(db), path, v)
	if err == nil {
		remember(vc, path, v, ver, asOf)
	}
	return ver, err
}

// Current is a history.CurrentFunc reading the indexed content of db.
func Current(db *index.DB) history.CurrentFunc {
	return func(ctx context.Context, path string) ([]byte, bool, error) {
		fi, ok, err := db.File(ctx, path)
		if err != nil || !ok {
			return nil, false, err
		}
		if fi.Binary {
			return nil, false, history.ErrNotFound
		}
		return []byte(fi.Content), true, nil
	}
}
