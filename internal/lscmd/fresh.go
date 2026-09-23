package lscmd

import (
	"context"

	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/index"
)

// refresh re-indexes listed files whose size or mtime changed on disk (direct
// mode freshness) and lists again when anything changed.
func refresh(ctx context.Context, db *index.DB, ws *filecmd.Workspace, rel string, rows []File) ([]File, error) {
	paths := make([]string, len(rows))
	for i, f := range rows {
		paths[i] = f.Path
	}
	changed, err := db.Refresh(ctx, ws.Root.Path(), paths, ws.Config.MaxFileSize)
	if err != nil || len(changed) == 0 {
		return rows, err
	}
	return query(ctx, db.SQL, rel)
}
