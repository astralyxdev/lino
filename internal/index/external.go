package index

import "context"

// External, when set, is told about index changes caused by edits made
// outside lino and found by a stat refresh or an explicit reconcile (for the
// change log). Whoever applies an update reports it, so nothing is logged twice.
var External func(ctx context.Context, d *DB, ups []Update)

// ReportExternal passes the updates that changed something to External.
func (d *DB) ReportExternal(ctx context.Context, ups []Update) {
	if External == nil {
		return
	}
	var changed []Update
	for _, u := range ups {
		if u.Op != Unchanged {
			changed = append(changed, u)
		}
	}
	if len(changed) > 0 {
		External(ctx, d, changed)
	}
}
