package e2e

import "testing"

func TestChanges(t *testing.T) {
	h := NewInit(t).Fixture("wallet")
	steps := []struct {
		name string
		cmd  Cmd
		exit int
	}{
		{"changes_start", Cmd{Args: []string{"changes", "--direct"}}, 2},
		{"changes_write", Cmd{Args: []string{"write", "wallet/validate.go", "--direct"}, Stdin: "package wallet\n\nfunc validate() {}\n"}, 0},
		{"changes_mv", Cmd{Args: []string{"mv", "wallet/validate.go", "wallet/check.go", "--direct"}}, 0},
		{"changes_after_edits", Cmd{Args: []string{"changes", "--since", "0", "--direct"}}, 0},
		{"changes_filtered", Cmd{Args: []string{"changes", "--since", "0", "--path", "check.go", "--direct"}, Dir: "wallet"}, 0},
		{"changes_none_new", Cmd{Args: []string{"changes", "--since", "2", "--direct"}}, 0},
	}
	for _, st := range steps {
		r := h.Exec(st.cmd)
		h.ExpectExit(r, st.exit)
		h.Golden(st.name, r)
	}
}
