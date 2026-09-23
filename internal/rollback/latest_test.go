package rollback

import (
	"fmt"
	"strings"
	"testing"
)

func TestRollbackLatest(t *testing.T) {
	// Changes 1..4: agent-1 edits l1 and l5, agent-2 edits l3 and l6.
	type step struct {
		by    string // author for the rollback; "" = none
		id    string // explicit id ("r1" = the first rollback made here)
		undid string // change id, "rN" for a rollback made here, "" = nothing to undo
		code  int
	}
	tests := []struct {
		name  string
		steps []step
		want  string
	}{
		{"without author walks back", []step{{"", "", "4", 0}, {"", "", "3", 0}, {"", "", "2", 0}, {"", "", "1", 0}, {"", "", "", 0}},
			base},
		{"with author", []step{{"agent-1", "", "3", 0}, {"agent-1", "", "1", 0}, {"agent-1", "", "", 0}},
			"l1\nl2\nB3\nl4\nl5\nB6\n"},
		{"agents interleaved", []step{{"agent-2", "", "4", 0}, {"agent-1", "", "3", 0}, {"", "", "2", 0}, {"agent-2", "", "", 0}},
			"A1\nl2\nl3\nl4\nl5\nl6\n"},
		{"unknown author", []step{{"agent-3", "", "", 0}},
			"A1\nl2\nB3\nl4\nA5\nB6\n"},
		{"undone rollback puts change back in effect", []step{{"agent-2", "", "4", 0}, {"agent-2", "r1", "", 0}, {"agent-2", "", "r2", 0}, {"agent-2", "", "2", 0}},
			"A1\nl2\nl3\nl4\nA5\nl6\n"},
		{"redo by another author", []step{{"agent-2", "", "4", 0}, {"agent-1", "r1", "", 0}, {"agent-2", "", "4", 6}},
			"A1\nl2\nB3\nl4\nA5\nB6\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t, map[string]string{"f.txt": base})
			e.ok("A1\n", "edit", "f.txt", "1", "1", "--v", e.v("f.txt"), "--by", "agent-1")
			e.ok("B3\n", "edit", "f.txt", "3", "3", "--v", e.v("f.txt"), "--by", "agent-2")
			e.ok("A5\n", "edit", "f.txt", "5", "5", "--v", e.v("f.txt"), "--by", "agent-1")
			e.ok("B6\n", "edit", "f.txt", "6", "6", "--v", e.v("f.txt"), "--by", "agent-2")
			var rollbacks []int64
			for i, s := range tt.steps {
				args := []string{"rollback"}
				if s.id != "" {
					var n int
					fmt.Sscanf(s.id, "r%d", &n)
					args = append(args, id(rollbacks[n-1]))
				}
				if s.by != "" {
					args = append(args, "--by", s.by)
				}
				out, errOut, code := e.run("", args...)
				if s.code != 0 {
					if code != s.code || !strings.Contains(errOut, "cannot undo "+s.undid) {
						t.Fatalf("step %d: exit %d, want %d\n%s", i, code, s.code, errOut)
					}
					continue
				}
				if code != 0 {
					t.Fatalf("step %d: exit %d\n%s%s", i, code, out, errOut)
				}
				if s.id != "" {
					rollbacks = append(rollbacks, e.latest().ID)
					continue
				}
				if s.undid == "" {
					if out != "" || !strings.Contains(errOut, "nothing to undo") {
						t.Fatalf("step %d: want nothing to undo, got %q %q", i, out, errOut)
					}
					continue
				}
				want := s.undid
				if n := 0; strings.HasPrefix(want, "r") {
					fmt.Sscanf(want, "r%d", &n)
					want = id(rollbacks[n-1])
				}
				c := e.latest()
				if id(c.Extra.Undid) != want || !strings.Contains(out, "undid "+want+" ") {
					t.Fatalf("step %d: undid %d (%q), want %s", i, c.Extra.Undid, out, want)
				}
				rollbacks = append(rollbacks, c.ID)
			}
			if got := e.read("f.txt"); got != tt.want {
				t.Fatalf("content\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestRollbackLatestJSON(t *testing.T) {
	e := setup(t, map[string]string{"f.txt": base})
	out, _, code := e.run("", "rollback", "--json")
	if code != 0 || !strings.Contains(out, `"outcome":"empty"`) {
		t.Fatalf("exit %d %s", code, out)
	}
}
