package histrec

import (
	"context"
	"path/filepath"

	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/index"
)

// MapRange carries lines start..end of path at version v to the current
// indexed content; see history.Store.MapRange.
func MapRange(ctx context.Context, db *index.DB, path, v string, start, end int) (history.Mapped, error) {
	st, err := Store(ctx, filepath.Dir(filepath.Dir(db.Path)))
	if err != nil {
		return history.Mapped{}, err
	}
	return st.MapRange(ctx, Current(db), path, v, start, end)
}
