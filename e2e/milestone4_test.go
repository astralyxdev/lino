package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/version"
)

// Milestone 4 in live mode: two agents, external edits, changes --wait, the
// range-aware version check, history/show, every rollback form, pruning, and
// losing history.db.

func m4Lines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

// fileV is the version of rel on disk.
func (h *Harness) fileV(rel string) string {
	h.t.Helper()
	b, err := os.ReadFile(h.Path(rel))
	if err != nil {
		h.t.Fatal(err)
	}
	return version.Of(b)
}

// must runs c and fails the test unless it exits with code.
func (h *Harness) must(code int, c Cmd) Result {
	h.t.Helper()
	r := h.Exec(c)
	if r.Exit != code {
		h.t.Fatalf("lino %v: exit %d, want %d\n%s", c.Args, r.Exit, code, h.Transcript(r))
	}
	return r
}

type m4Change struct {
	ID      int64  `json:"id"`
	Source  string `json:"source"`
	Author  string `json:"author"`
	Op      string `json:"op"`
	Path    string `json:"path"`
	Undid   int64  `json:"undid"`
	Summary string `json:"summary"`
}

// changesOf lists history (newest first), optionally for one path.
func (h *Harness) changesOf(path string) []m4Change {
	h.t.Helper()
	args := []string{"history", "--json", "-k", "200"}
	if path != "" {
		args = append(args, path)
	}
	r := h.Run(args...)
	var env struct {
		Data struct {
			Changes []m4Change `json:"changes"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &env); err != nil {
		h.t.Fatalf("history: %v\n%s", err, h.Transcript(r))
	}
	return env.Data.Changes
}

func (h *Harness) latestChange() m4Change {
	h.t.Helper()
	cs := h.changesOf("")
	if len(cs) == 0 {
		h.t.Fatal("history is empty")
	}
	return cs[0]
}

// await fails the test unless cond holds within 5 s.
func await(t *testing.T, what string, cond func() bool) {
	t.Helper()
	if !waitFor(5*time.Second, cond) {
		t.Fatalf("timed out waiting for %s", what)
	}
}

func contains(t *testing.T, r Result, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(r.Stdout, s) {
			t.Fatalf("lino %v: stdout lacks %q\n%s", r.Args, s, r.Stdout)
		}
	}
}

func id64(n int64) string { return strconv.FormatInt(n, 10) }

func TestMilestone4(t *testing.T) {
	h := New(t)
	h.Home = shortHome(t)
	const svc = "wallet/service.go"
	h.Write(svc, m4Lines(20)).Write("docs/notes.md", "one\ntwo\n")
	h.ExpectExit(h.Run("init"), 0)
	startLive(t, h)
	a1 := map[string]string{"LINO_BY": "agent-1"}
	a2 := map[string]string{"LINO_BY": "agent-2"}

	// Two agents read the same version; agent-1 edits line 3.
	v0 := h.fileV(svc)
	h.must(0, Cmd{Args: []string{"edit", svc, "3", "3", "--v", v0}, Stdin: "A1 three\n", Env: a1})
	e1 := h.latestChange()
	if e1.Author != "agent-1" || e1.Op != "edit" {
		t.Fatalf("first change %+v", e1)
	}

	// changes --wait sees agent-2's edit, made with the stale version: it
	// targets line 15 only, so the range-aware check accepts it.
	since := id64(e1.ID)
	waited := make(chan Result, 1)
	go func() {
		waited <- h.Run("changes", "--since", since, "--path", "wallet/**", "--wait", "10s")
	}()
	time.Sleep(200 * time.Millisecond)
	h.must(0, Cmd{Args: []string{"edit", svc, "15", "15", "--v", v0}, Stdin: "A2 fifteen\n", Env: a2})
	e2 := h.latestChange()
	select {
	case r := <-waited:
		h.ExpectExit(r, 0)
		contains(t, r, svc, "next "+id64(e2.ID))
	case <-time.After(15 * time.Second):
		t.Fatal("changes --wait did not return")
	}

	// agent-2 with the same stale version on agent-1's line: conflict.
	r := h.must(6, Cmd{Args: []string{"edit", svc, "3", "3", "--v", v0}, Stdin: "A2 three\n", Env: a2})
	if !strings.Contains(r.Stdout+r.Stderr, "A1 three") {
		t.Fatalf("conflict does not show the current lines\n%s", h.Transcript(r))
	}

	// An external edit is picked up by the watcher and recorded.
	h.Write(svc, h.Read(svc)+"external tail\n")
	var ext m4Change
	await(t, "external change in history", func() bool {
		ext = h.latestChange()
		return ext.Source == "external" && ext.Path == svc
	})
	contains(t, h.Run("changes", "--since", id64(e2.ID)), svc)

	// history and show.
	r = h.must(0, Cmd{Args: []string{"history", svc}})
	contains(t, r, "agent-1", "agent-2", "external")
	r = h.must(0, Cmd{Args: []string{"show", id64(e1.ID)}})
	contains(t, r, "- 3: line 3", "+ 3: A1 three")
	h.must(3, Cmd{Args: []string{"show", "999"}})

	// rollback <id> --dry-run writes nothing.
	before := h.Read(svc)
	r = h.must(0, Cmd{Args: []string{"rollback", id64(e1.ID), "--dry-run"}})
	contains(t, r, fmt.Sprintf("would undo %d (edit %s lines 3 by agent-1)", e1.ID, svc), "- 3: A1 three", "+ 3: line 3", "no conflicts")
	if h.Read(svc) != before || h.latestChange().ID != ext.ID {
		t.Fatal("dry run changed something")
	}

	// rollback <id> undoes agent-1's edit and keeps everything else.
	r = h.must(0, Cmd{Args: []string{"rollback", id64(e1.ID)}, Env: a1})
	rb1 := h.latestChange()
	contains(t, r, fmt.Sprintf("%d  rollback  %s  undid %d", rb1.ID, svc, e1.ID))
	want := strings.Replace(m4Lines(20), "line 15\n", "A2 fifteen\n", 1) + "external tail\n"
	if h.Read(svc) != want {
		t.Fatalf("after rollback %d:\n%s", e1.ID, h.Read(svc))
	}

	// rollback with no id and an author undoes that author's latest change.
	r = h.must(0, Cmd{Args: []string{"rollback"}, Env: a2})
	contains(t, r, fmt.Sprintf("undid %d", e2.ID))
	want = m4Lines(20) + "external tail\n"
	if h.Read(svc) != want {
		t.Fatalf("after rollback --by agent-2:\n%s", h.Read(svc))
	}

	// Rolling back a rollback redoes agent-1's edit.
	h.must(0, Cmd{Args: []string{"rollback", id64(rb1.ID)}})
	if !strings.Contains(h.Read(svc), "A1 three\n") {
		t.Fatalf("rollback of rollback:\n%s", h.Read(svc))
	}

	// The external edit can be undone like any other.
	h.must(0, Cmd{Args: []string{"rollback", id64(ext.ID)}})
	if strings.Contains(h.Read(svc), "external tail") {
		t.Fatalf("external edit still there:\n%s", h.Read(svc))
	}

	// A change whose lines were touched later conflicts, and names the change.
	h.must(0, Cmd{Args: []string{"edit", svc, "3", "3", "--v", h.fileV(svc)}, Stdin: "again\n", Env: a2})
	later := h.latestChange()
	r = h.must(6, Cmd{Args: []string{"rollback", id64(e1.ID), "--dry-run"}})
	contains(t, r, "conflict:")
	r = h.must(6, Cmd{Args: []string{"rollback", id64(e1.ID)}})
	if !strings.Contains(r.Stderr, id64(later.ID)) && !strings.Contains(r.Stdout, id64(later.ID)) {
		t.Fatalf("conflict does not name %d\n%s", later.ID, h.Transcript(r))
	}

	// File-level changes: write of a new file, mv, rm.
	h.must(0, Cmd{Args: []string{"write", "wallet/new.go"}, Stdin: "package wallet\n", Env: a1})
	created := h.latestChange()
	h.must(0, Cmd{Args: []string{"mv", "docs/notes.md", "docs/moved.md"}, Env: a1})
	moved := h.latestChange()
	h.must(0, Cmd{Args: []string{"rm", "docs/moved.md", "--force"}, Env: a1})
	removed := h.latestChange()
	point := removed.ID

	h.must(0, Cmd{Args: []string{"rollback", id64(removed.ID)}})
	if h.Read("docs/moved.md") != "one\ntwo\n" {
		t.Fatal("rm not undone")
	}
	h.must(0, Cmd{Args: []string{"rollback", id64(moved.ID)}})
	if h.Read("docs/notes.md") != "one\ntwo\n" {
		t.Fatal("mv not undone")
	}
	h.must(0, Cmd{Args: []string{"rollback", id64(created.ID)}})
	if _, err := os.Stat(h.Path("wallet/new.go")); !os.IsNotExist(err) {
		t.Fatal("new file not removed")
	}

	// rollback --to returns every file to its state right after point.
	r = h.must(0, Cmd{Args: []string{"rollback", "--to", id64(point), "--dry-run"}})
	contains(t, r, "would restore", "wallet/new.go", "docs/notes.md", "no conflicts")
	if h.Read("docs/notes.md") != "one\ntwo\n" {
		t.Fatal("dry run --to changed files")
	}
	h.must(0, Cmd{Args: []string{"rollback", "--to", id64(point)}})
	if h.Read("wallet/new.go") != "package wallet\n" {
		t.Fatal("--to did not recreate wallet/new.go")
	}
	for _, p := range []string{"docs/notes.md", "docs/moved.md"} {
		if _, err := os.Stat(h.Path(p)); !os.IsNotExist(err) {
			t.Fatalf("--to left %s", p)
		}
	}
	// --path limits the restore.
	h.must(0, Cmd{Args: []string{"rollback", "--to", id64(created.ID - 1), "--path", "wallet/**"}})
	if _, err := os.Stat(h.Path("wallet/new.go")); !os.IsNotExist(err) {
		t.Fatal("--to --path did not remove wallet/new.go")
	}
	if _, err := os.Stat(h.Path("docs/notes.md")); !os.IsNotExist(err) {
		t.Fatal("--to --path touched docs/")
	}

	// Pruning: with a short retention, the startup prune of a new process
	// drops old changes; newer ones stay usable.
	oldest := h.changesOf("")
	first := oldest[len(oldest)-1].ID
	h.must(0, Cmd{Args: []string{"stop"}})
	h.Write(".lino/config", "history.retention = 3s\n")
	time.Sleep(3200 * time.Millisecond)
	h.Write("fresh.txt", "fresh\n")
	startLive(t, h)
	h.must(0, Cmd{Args: []string{"edit", "fresh.txt", "1", "1", "--v", h.fileV("fresh.txt")}, Stdin: "FRESH\n"})
	fresh := h.latestChange()
	await(t, "prune of old changes", func() bool { return h.Run("show", id64(first)).Exit == 3 })
	h.must(3, Cmd{Args: []string{"rollback", id64(e1.ID)}})
	h.must(0, Cmd{Args: []string{"show", id64(fresh.ID)}})
	h.must(0, Cmd{Args: []string{"rollback", id64(fresh.ID)}})
	if h.Read("fresh.txt") != "fresh\n" {
		t.Fatal("retained change not undone after prune")
	}

	// Deleting history.db loses undo, nothing else.
	h.Write(".lino/config", "")
	for _, s := range []string{"", "-wal", "-shm"} {
		os.Remove(h.Path(".lino/history.db" + s))
	}
	h.must(3, Cmd{Args: []string{"rollback", id64(fresh.ID)}})
	contains(t, h.must(0, Cmd{Args: []string{"search", "fresh"}}), "fresh.txt")
	contains(t, h.must(0, Cmd{Args: []string{"read", svc, "--lines", "3:3"}}), "again")
	contains(t, h.must(0, Cmd{Args: []string{"changes", "--since", "0"}}), "fresh.txt")
	h.must(0, Cmd{Args: []string{"edit", svc, "1", "1", "--v", h.fileV(svc)}, Stdin: "after loss\n", Env: a1})
	cs := h.changesOf("")
	if len(cs) != 1 || cs[0].Author != "agent-1" {
		t.Fatalf("history after deleting history.db: %+v", cs)
	}
	h.must(0, Cmd{Args: []string{"rollback"}, Env: a1})
	if !strings.HasPrefix(h.Read(svc), "line 1\n") {
		t.Fatalf("new history not usable:\n%s", h.Read(svc))
	}
}
