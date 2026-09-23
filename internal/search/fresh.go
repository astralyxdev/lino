package search

import (
	"context"

	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/index"
)

// Fresh runs a query. Outside a watched live process it first stats every
// indexed file and re-indexes those that changed on disk, so stale hits are
// dropped and text edited outside lino is found. New files are only picked
// up by a reconcile (lino index) or the live watcher.
func Fresh(ctx context.Context, db *index.DB, ws *filecmd.Workspace, run func() (Result, error)) (Result, error) {
	if !index.Watched(ctx) {
		if _, err := db.RefreshAll(ctx, ws.Root.Path(), ws.Config.MaxFileSize); err != nil {
			return Result{}, err
		}
	}
	return run()
}
