package helpcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/cli"
	_ "github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/outcome"
)

func run(args ...string) (string, string, int) {
	var o, e bytes.Buffer
	code := cli.Default.Main(context.Background(), args, cli.Env{Cwd: "/", Stdin: strings.NewReader("")}, &o, &e)
	return o.String(), e.String(), code
}

func TestHelp(t *testing.T) {
	tests := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"help"}, 0, "  lino read <file>"},
		{[]string{"-h"}, 0, "  lino help [command] [--agent]"},
		{[]string{"--help"}, 0, "usage: lino <command>"},
		{[]string{"help", "edit"}, 0, "usage: lino edit <file> <start> [<end>] --v V\n"},
		{[]string{"help", "--agent"}, 0, Header() + "\n"},
		{[]string{"help", "nope"}, 2, ""},
		{[]string{"help", "edit", "--agent"}, 2, ""},
		{[]string{"help", "a", "b"}, 2, ""},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			out, errOut, code := run(tt.args...)
			if code != tt.code || !strings.Contains(out, tt.want) {
				t.Fatalf("exit %d (want %d)\nstdout:\n%s\nstderr:\n%s", code, tt.code, out, errOut)
			}
		})
	}
}

func TestAgentJSON(t *testing.T) {
	out, _, code := run("help", "--agent", "--json")
	var env struct {
		Outcome string    `json:"outcome"`
		Data    AgentData `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || code != 0 {
		t.Fatalf("exit %d: %v\n%s", code, err, out)
	}
	if env.Outcome != "ok" || env.Data.Version != AgentVersion || env.Data.Text != Agent {
		t.Fatalf("%+v", env)
	}
}

func TestAgentBlock(t *testing.T) {
	if !Valid() {
		t.Fatalf("block does not start with %q", Header())
	}
	if n := len(Agent); n > 3500 {
		t.Errorf("block is %d bytes; keep it small", n)
	}
	// Every outcome is explained, with its exit code.
	for _, o := range []outcome.Outcome{
		outcome.OK, outcome.Updated, outcome.Created, outcome.Empty, outcome.Truncated, outcome.Usage,
		outcome.NotFound, outcome.AnchorMismatch, outcome.Ambiguous, outcome.Conflict, outcome.Refused,
		outcome.NotRunning, outcome.LiveExists, outcome.Internal,
	} {
		re := regexp.MustCompile(`(?m)^  [a-z_, ]*\b` + string(o) + `\b[a-z_, ]*\(` + strconv.Itoa(o.ExitCode()) + `\)`)
		if !re.MatchString(Agent) {
			t.Errorf("outcome %s (exit %d) not in the block's table", o, o.ExitCode())
		}
	}
	// Every command the block names exists.
	for _, m := range regexp.MustCompile(`(?m)(?:^\s*|->\s+|: |, |\()lino ([a-z]+)`).FindAllStringSubmatch(Agent, -1) {
		if _, ok := cli.Default.Lookup(m[1]); !ok && !commandsElsewhere[m[1]] {
			t.Errorf("block names unknown command %q", m[1])
		}
	}
}

// commandsElsewhere are registered by packages this test does not import.
var commandsElsewhere = map[string]bool{
	"search": true, "ls": true, "init": true, "changes": true, "history": true, "show": true, "rollback": true,
}
