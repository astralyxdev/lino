package rollback

import (
	"context"

	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
)

// latestPage is how many changes Latest reads per query.
const latestPage = 256

// Latest returns the change `lino rollback` with no id undoes: the newest
// change by author (any author when empty) that is still in effect.
// Repeated calls walk back like an undo stack: a rollback is skipped
// together with the change it undid, unless that rollback was itself undone.
// Undoing a rollback (a redo), or a rollback that names no single change
// (Extra.Undid == 0), counts as an ordinary change, so the next call undoes
// the redo rather than conflicting with it. It returns 0 when
// nothing is left to undo.
func Latest(ctx context.Context, st *history.Store, author string) (int64, error) {
	undone := map[int64]bool{}
	kinds := map[int64]kind{}
	before := int64(0)
	for {
		cs, err := st.List(ctx, history.Filter{Before: before, Limit: latestPage})
		if err != nil {
			return 0, outcome.Wrap(outcome.Internal, err, "")
		}
		for _, c := range cs {
			before = c.ID
			if undone[c.ID] {
				continue
			}
			if c.Op == history.OpRollback && c.Extra.Undid > 0 {
				k, err := kindOf(ctx, st, kinds, c)
				if err != nil {
					return 0, err
				}
				undone[c.Extra.Undid] = true
				if k == undoKind {
					continue
				}
			}
			if author == "" || c.Author == author {
				return c.ID, nil
			}
		}
		if len(cs) < latestPage {
			break
		}
	}
	return 0, nil
}

// nothing is the result of `lino rollback` with nothing left to undo.
func nothing(author string) output.Result {
	msg, hint := "nothing to undo: no change in history is still in effect", "lino history"
	if author != "" {
		msg = "nothing to undo: no change by " + author + " in history is still in effect"
		hint += " --by " + author
	}
	return output.Result{Outcome: outcome.Empty, Message: msg, Hint: hint}
}

type kind uint8

const (
	ordinary kind = iota // an edit, or a redo: undoing it is a real undo
	undoKind             // a rollback that undid an ordinary change
)

// kindOf classifies c: a rollback of an ordinary change (or of a redo) is an
// undo; a rollback of an undo is a redo, which counts as ordinary. A pruned
// target counts as ordinary.
func kindOf(ctx context.Context, st *history.Store, memo map[int64]kind, c history.Change) (kind, error) {
	var chain []int64
	k := ordinary
	for c.Op == history.OpRollback && c.Extra.Undid > 0 {
		if m, ok := memo[c.ID]; ok {
			k = m
			break
		}
		chain = append(chain, c.ID)
		cs, err := st.List(ctx, history.Filter{Since: c.Extra.Undid - 1, Before: c.Extra.Undid + 1})
		if err != nil {
			return 0, outcome.Wrap(outcome.Internal, err, "")
		}
		if len(cs) == 0 {
			break
		}
		c = cs[0]
	}
	// k is the kind of the chain's end; walk back to the start flipping.
	for i := len(chain) - 1; i >= 0; i-- {
		if k == ordinary {
			k = undoKind
		} else {
			k = ordinary
		}
		memo[chain[i]] = k
	}
	return k, nil
}
