package e2e

import (
	"encoding/json"
	"os"
	"testing"
)

// A direct-mode edit in a root without index.db builds the index and is
// recorded in history, also after the index is deleted.
func TestHistoryWithoutIndex(t *testing.T) {
	h := NewInit(t).Write("a.txt", "one\ntwo\n").Write("b.txt", "bee\n")
	h.Env["LINO_BY"] = "agent-x"
	edit := func(line, text string) {
		h.must(0, Cmd{Args: []string{"edit", "a.txt", line, line, "--v", h.fileV("a.txt"), "--direct"}, Stdin: text + "\n"})
	}
	edit("1", "ONE")
	if got := directHistory(h); len(got) != 1 || got[0].Author != "agent-x" || got[0].Op != "edit" || got[0].Path != "a.txt" {
		t.Fatalf("history after first edit: %+v", got)
	}
	if r := h.Run("search", "bee", "--direct"); r.Exit != 0 {
		t.Errorf("index not built:\n%s", h.Transcript(r))
	}

	for _, suf := range []string{"", "-wal", "-shm"} {
		os.Remove(h.Path(".lino/index.db" + suf))
	}
	edit("2", "TWO")
	got := directHistory(h)
	if len(got) != 2 || got[0].ID <= got[1].ID || got[0].Summary == "" {
		t.Fatalf("history after deleting the index: %+v", got)
	}
	h.must(0, Cmd{Args: []string{"rollback", "--direct"}})
	if s := h.Read("a.txt"); s != "ONE\ntwo\n" {
		t.Errorf("after rollback: %q", s)
	}
}

func directHistory(h *Harness) []m4Change {
	h.t.Helper()
	r := h.must(0, Cmd{Args: []string{"history", "--json", "--direct"}})
	var env struct {
		Data struct{ Changes []m4Change } `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &env); err != nil {
		h.t.Fatalf("history: %v\n%s", err, r.Stdout)
	}
	return env.Data.Changes
}
