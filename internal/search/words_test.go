package search

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSearchWordsCommand(t *testing.T) {
	root := newRoot(t, map[string]string{
		".lino/config":      "",
		"docs/wallet.md":    "# Wallet\n\nA withdraw request is checked.\nEvery withdraw is logged.\n",
		"wallet/service.go": "package wallet\n\nfunc Withdraw(amt int64) error {\n\treturn withdrawFunds(amt)\n}\n",
		"misc/cats.txt":     "cats only\n",
	})
	tests := []struct {
		name   string
		args   []string
		code   int
		stdout string
	}{
		{"ranked", []string{"search", "--words", "withdraw"}, 0,
			"docs/wallet.md\n  3   A withdraw request is checked.\n  4   Every withdraw is logged.\n" +
				"wallet/service.go\n  3   func Withdraw(amt int64) error {\n  4   return withdrawFunds(amt)\n" +
				"4 hits in 2 files (words)\n"},
		// Only service.go has "funds" (inside withdrawFunds), so it ranks first.
		{"camel parts", []string{"search", "withdraw funds", "--words"}, 0, "wallet/service.go\n"},
		{"no hits", []string{"search", "--words", "zebra"}, 0, "0 hits in 0 files (words)\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errOut, code := run(t, root, tt.args...)
			if code != tt.code {
				t.Fatalf("code %d, stderr %q", code, errOut)
			}
			if tt.name == "camel parts" && strings.HasPrefix(out, tt.stdout) {
				return
			}
			if out != tt.stdout {
				t.Errorf("stdout\n%s\nwant\n%s", out, tt.stdout)
			}
		})
	}
}

func TestSearchWordsJSON(t *testing.T) {
	root := newRoot(t, map[string]string{".lino/config": "", "a.txt": "zebra\n"})
	out, _, code := run(t, root, "search", "--words", "--json", "nothing")
	if code != 0 {
		t.Fatalf("code %d", code)
	}
	var env struct {
		Outcome string `json:"outcome"`
		Data    Data   `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env.Outcome != "empty" || env.Data.Source != FromWords || len(env.Data.Hits) != 0 {
		t.Errorf("got %s", out)
	}
}

func TestSearchWordsPathAndContext(t *testing.T) {
	root := newRoot(t, map[string]string{
		".lino/config": "",
		"a/one.txt":    "alpha\nwithdraw here\nomega\n",
		"b/two.txt":    "withdraw there\n",
	})
	out, errOut, code := run(t, root, "search", "--words", "withdraw", "--path", "a/**", "-C", "1")
	if code != 0 {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	if strings.Contains(out, "b/two.txt") || !strings.Contains(out, "alpha") || !strings.Contains(out, "omega") {
		t.Errorf("stdout %q", out)
	}
}
