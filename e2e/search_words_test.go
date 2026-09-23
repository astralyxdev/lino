package e2e

import (
	"testing"
)

func TestSearchWords(t *testing.T) {
	tests := []struct {
		name string
		cmd  Cmd
		exit int
	}{
		{"search_words", Cmd{Args: []string{"search", "--words", "invalid amount", "--direct"}}, 0},
		{"search_words_path", Cmd{Args: []string{"search", "--words", "withdraw", "--path", "wallet/**", "--direct"}}, 0},
		{"search_words_context", Cmd{Args: []string{"search", "--words", "withdraw", "-C", "1", "--path", "docs/**", "--direct"}}, 0},
		{"search_words_none", Cmd{Args: []string{"search", "--words", "zebra", "--direct"}}, 0},
		{"search_words_unquoted", Cmd{Args: []string{"search", "--words", "invalid", "amount", "--direct"}}, 0},
		{"search_literal_two_args", Cmd{Args: []string{"search", "invalid", "amount", "--direct"}}, 2},
		{"search_words_regex", Cmd{Args: []string{"search", "--words", "--regex", "x", "--direct"}}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewInit(t).Fixture("wallet").
				Write("wallet/service.go", "package wallet\n\n// Withdraw moves funds.\nfunc Withdraw(amt int64) error {\n\tif amt <= 0 {\n\t\treturn ErrInvalidAmount\n\t}\n\treturn withdrawFunds(amt)\n}\n").
				Write("docs/wallet.md", "# Wallet\n\nA withdraw request is checked.\nThen it is queued.\nEvery withdraw is logged.\n")
			r := h.Exec(tt.cmd)
			h.ExpectExit(r, tt.exit)
			h.Golden(tt.name, r)
		})
	}
}

func TestSearchWordsUnquotedMatchesQuoted(t *testing.T) {
	h := NewInit(t).Fixture("wallet")
	quoted := h.Run("search", "--words", "invalid amount", "--direct")
	unquoted := h.Run("search", "--words", "invalid", "amount", "--direct")
	h.ExpectExit(unquoted, 0)
	if quoted.Stdout != unquoted.Stdout || quoted.Stderr != unquoted.Stderr {
		t.Errorf("unquoted differs from quoted\n--- quoted\n%s--- unquoted\n%s", h.Transcript(quoted), h.Transcript(unquoted))
	}
}
