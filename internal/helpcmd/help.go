// Package helpcmd implements `lino help`: the command list, one command's
// synopsis, or with --agent the instruction block for an agent's prompt.
package helpcmd

import (
	"bytes"
	"context"
	_ "embed"
	"flag"
	"io"
	"strconv"
	"strings"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
)

// Agent is the instruction block printed by `lino help --agent`. Its first
// line carries the block version; change AgentVersion with any edit.
//
//go:embed agent.txt
var Agent string

// AgentVersion is the version of the Agent block.
const AgentVersion = 1

func init() { cli.Register(Command) }

// Command is `lino help`.
var Command = &cli.Command{
	Name:    "help",
	Usage:   "[command] [--agent]",
	Summary: "list commands; --agent prints instructions for an agent's system prompt",
	Local:   true,
	MaxArgs: 1,
	Setup: func(fs *flag.FlagSet) cli.RunFunc {
		agent := fs.Bool("agent", false, "print the agent instruction block")
		return func(_ context.Context, c *cli.Call) (output.Result, error) {
			return Help(cli.Default, c.Arg(0), *agent)
		}
	},
}

// AgentData is the --agent block.
type AgentData struct {
	Version int    `json:"version"`
	Text    string `json:"text"`
}

func (d AgentData) WriteText(w io.Writer) error {
	_, err := io.WriteString(w, d.Text)
	return err
}

// UsageData is the command list or one command's synopsis.
type UsageData struct {
	Text string `json:"text"`
}

func (d UsageData) WriteText(w io.Writer) error {
	_, err := io.WriteString(w, d.Text)
	return err
}

// Help returns the agent block, the synopsis of command name, or the usage
// of every command in r.
func Help(r *cli.Registry, name string, agent bool) (output.Result, error) {
	switch {
	case agent && name != "":
		return output.Result{}, outcome.New(outcome.Usage, "help: --agent takes no command").WithHint("lino help --agent")
	case agent:
		return output.Result{Data: AgentData{Version: AgentVersion, Text: Agent}}, nil
	case name != "":
		cmd, ok := r.Lookup(name)
		if !ok {
			return output.Result{}, outcome.New(outcome.Usage, "unknown command %q", name).WithHint("lino help")
		}
		return output.Result{Data: UsageData{Text: "usage: " + cmd.Synopsis() + "\n"}}, nil
	}
	var b bytes.Buffer
	r.Usage(&b)
	return output.Result{Data: UsageData{Text: b.String()}}, nil
}

// Header returns the version line the block must start with.
func Header() string {
	return "# lino agent instructions v" + strconv.Itoa(AgentVersion)
}

// Valid reports whether the embedded block starts with Header.
func Valid() bool { return strings.HasPrefix(Agent, Header()+"\n") }
