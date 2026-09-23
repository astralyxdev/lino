package history

import (
	"context"

	"github.com/astralyx/lino/internal/textfile"
)

// At returns the content path had right after change id, by undoing every
// later change on path newest first. ok is false when no file existed at
// path then (created later, or moved or removed before). A chain through a
// binary change or a broken fragment is ErrNotFound.
func (s *Store) At(ctx context.Context, cur CurrentFunc, path string, id int64) (doc *textfile.Doc, ok bool, err error) {
	st, err := s.at(ctx, cur, path, id)
	if err != nil {
		return nil, false, err
	}
	return st.doc, st.exists, nil
}
