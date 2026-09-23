package e2e

import (
	"strings"
	"testing"
)

func TestHelpRollbackUndoStack(t *testing.T) {
	h := New(t)
	r := h.Run("help", "rollback")
	h.ExpectExit(r, 0)
	for _, s := range []string{"newest change still in effect", "undo stack", "redo"} {
		if !strings.Contains(r.Stdout, s) {
			t.Errorf("help rollback lacks %q:\n%s", s, r.Stdout)
		}
	}
}
