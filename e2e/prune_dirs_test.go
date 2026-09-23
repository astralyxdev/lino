package e2e

import (
	"os"
	"testing"
)

func TestRmMvPruneEmptyDirs(t *testing.T) {
	h := New(t)
	h.Write("top.txt", "top\n").
		Write("src/util/math.go", "package util\n").
		Write("keep/a.txt", "a\n").
		Write("keep/debug.log", "ignored\n").
		Write("move/only.txt", "m\n").
		Write(".linoignore", "*.log\n")
	h.ExpectExit(h.Run("init"), 0)

	isDir := func(rel string) bool {
		st, err := os.Stat(h.Path(rel))
		return err == nil && st.IsDir()
	}

	h.must(0, Cmd{Args: []string{"rm", "src/util/math.go", "--force", "--direct"}})
	if isDir("src/util") || isDir("src") {
		t.Error("empty src/util and src not removed after rm")
	}

	h.must(0, Cmd{Args: []string{"rm", "keep/a.txt", "--force", "--direct"}})
	if !isDir("keep") {
		t.Error("keep/ removed although it holds an ignored file")
	}

	h.must(0, Cmd{Args: []string{"rm", "top.txt", "--force", "--direct"}})
	if !isDir(".") || !isDir(".lino") {
		t.Error("root or .lino removed")
	}

	h.must(0, Cmd{Args: []string{"mv", "move/only.txt", "moved/only.txt", "--direct"}})
	if isDir("move") {
		t.Error("empty move/ not removed after mv")
	}

	// Undo mv, rm top.txt and rm keep/a.txt, then rm src/util/math.go.
	for i := 0; i < 4; i++ {
		h.must(0, Cmd{Args: []string{"rollback", "--direct"}})
	}
	if s := h.Read("src/util/math.go"); s != "package util\n" {
		t.Errorf("rollback did not restore src/util/math.go: %q", s)
	}
	if s := h.Read("move/only.txt"); s != "m\n" {
		t.Errorf("rollback did not restore move/only.txt: %q", s)
	}
	if isDir("moved") {
		t.Error("empty moved/ not removed after undoing mv")
	}
}
