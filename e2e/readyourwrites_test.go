package e2e

import (
	"fmt"
	"strings"
	"testing"
)

// A live process re-indexes after an edit returns; the next request must
// still see the edit.
func TestEditThenSearchLive(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	h.Write("a.txt", "head\nmarker0\ntail\n")
	h.Write("b.txt", "other\n")
	h.must(0, Cmd{Args: []string{"init"}})
	h.must(0, Cmd{Args: []string{"run"}})
	stopOnCleanup(t, h)

	for i := 1; i <= 100; i++ {
		word := fmt.Sprintf("marker%d", i)
		h.must(0, Cmd{Args: []string{"edit", "a.txt", "2", "2", "--v", h.fileV("a.txt")}, Stdin: word + "\n"})
		r := h.Run("search", word)
		if r.Exit != 0 || !strings.Contains(r.Stdout, "a.txt") {
			t.Fatalf("edit %d: search misses the edit:\n%s", i, h.Transcript(r))
		}
		if old := h.Run("search", fmt.Sprintf("marker%d", i-1)); strings.Contains(old.Stdout, "a.txt") {
			t.Fatalf("edit %d: search still finds the old line:\n%s", i, h.Transcript(old))
		}
	}
}
