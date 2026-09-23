package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
)

// MoveFile records that rel from was renamed to rel to, now at abs. The row
// keeps its content and FTS entries. When from is not indexed, to is indexed
// from disk instead.
func (d *DB) MoveFile(ctx context.Context, from, to, abs string, maxSize int64) (Update, error) {
	st, err := os.Stat(abs)
	if err != nil || !st.Mode().IsRegular() {
		d.RemoveFile(ctx, from)
		return d.IndexFile(ctx, to, abs, maxSize)
	}
	s := Stat{Size: st.Size(), ModTime: st.ModTime(), Mode: st.Mode().Perm()}
	var u Update
	err = inTx(ctx, d.SQL, func(tx *sql.Tx) error {
		if _, err := removeTx(ctx, tx, to); err != nil {
			return err
		}
		var err error
		u, err = moveTx(ctx, tx, from, to, s)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return d.IndexFile(ctx, to, abs, maxSize)
	}
	if err != nil {
		return Update{}, fmt.Errorf("move %s: %w", from, err)
	}
	return u, nil
}
