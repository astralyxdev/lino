package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
)

// Accept says which global flags a command takes besides --json.
type Accept uint8

const (
	AcceptID     Accept = 1 << iota // -i / --id
	AcceptDirect                    // --direct
	AcceptBy                        // --by (fallback LINO_BY)

	// File is the set for commands that read files: -i, --direct.
	File = AcceptID | AcceptDirect
	// Mutating is the set for commands that change files: -i, --direct, --by.
	Mutating = AcceptID | AcceptDirect | AcceptBy
)

// Globals are the flags shared by commands.
type Globals struct {
	ID     string
	JSON   bool
	Direct bool
	By     string
}

// RunFunc executes a parsed call.
type RunFunc func(ctx context.Context, c *Call) (output.Result, error)

// Command is one subcommand.
type Command struct {
	Name    string
	Usage   string // synopsis after the name, e.g. "<file> [--lines A:B] [--anchors]"
	Summary string
	// Details are extra lines `lino help <name>` prints after the synopsis.
	Details string
	Accept  Accept
	MinArgs int
	MaxArgs int // -1 = unlimited
	// Stdin marks commands that read content from stdin; the client forwards
	// it to the live process. Other commands never touch stdin when forwarded.
	Stdin bool
	// Local commands always run in the calling process, never forwarded.
	Local bool
	// Setup defines the command's own flags on fs and returns the function
	// that runs it. It is called once per invocation, so the returned closure
	// may capture the flag pointers safely.
	Setup func(fs *flag.FlagSet) RunFunc
}

// Env is what a command sees of the outside world.
type Env struct {
	Stdin  io.Reader
	Cwd    string
	Getenv func(string) string
}

// Call is one parsed invocation of a command.
type Call struct {
	Command *Command
	Globals
	Args  []string            // positional arguments
	Flags map[string][]string // command-specific flags that were set
	Raw   []string            // argv after the command name, as given
	Stdin io.Reader
	Cwd   string
	Env   func(string) string

	run RunFunc
}

// Run executes the call.
func (c *Call) Run(ctx context.Context) (output.Result, error) {
	return c.run(ctx, c)
}

// Arg returns positional argument i, or "".
func (c *Call) Arg(i int) string {
	if i < len(c.Args) {
		return c.Args[i]
	}
	return ""
}

// Registry holds the known commands.
type Registry struct {
	cmds map[string]*Command
	// Forward, when set, is offered every parsed call before it runs locally.
	// It returns handled=false to let the call run in this process.
	Forward func(ctx context.Context, c *Call, stdout, stderr io.Writer) (exit int, handled bool)
}

// Default is the registry the lino binary uses.
var Default = NewRegistry()

// Register adds c to Default.
func Register(c *Command) { Default.Register(c) }

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{cmds: map[string]*Command{}} }

// Register adds c; it panics on a duplicate or unnamed command.
func (r *Registry) Register(c *Command) {
	if c.Name == "" || c.Setup == nil {
		panic("cli: command needs a name and Setup")
	}
	if _, dup := r.cmds[c.Name]; dup {
		panic("cli: duplicate command " + c.Name)
	}
	r.cmds[c.Name] = c
}

// Lookup returns the named command.
func (r *Registry) Lookup(name string) (*Command, bool) {
	c, ok := r.cmds[name]
	return c, ok
}

// Commands returns all commands sorted by name.
func (r *Registry) Commands() []*Command {
	out := make([]*Command, 0, len(r.cmds))
	for _, c := range r.cmds {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Usage writes the command list.
func (r *Registry) Usage(w io.Writer) {
	fmt.Fprintln(w, "usage: lino <command> [args] [--json]")
	for _, c := range r.Commands() {
		line := "  lino " + c.Name
		if c.Usage != "" {
			line += " " + c.Usage
		}
		if c.Summary != "" {
			line = fmt.Sprintf("%-50s # %s", line, c.Summary)
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintln(w, "global flags: -i/--id ID|NAME, --json; --direct (file commands), --by (mutating); lino help <command> for details")
}

// Synopsis is "lino <name> <usage>".
func (c *Command) Synopsis() string {
	if c.Usage == "" {
		return "lino " + c.Name
	}
	return "lino " + c.Name + " " + c.Usage
}

// Help is the synopsis followed by the global flags the command accepts.
func (c *Command) Help() string {
	var b strings.Builder
	b.WriteString("usage: " + c.Synopsis() + "\n")
	if c.Details != "" {
		b.WriteString(strings.TrimRight(c.Details, "\n") + "\n")
	}
	b.WriteString("global flags:\n")
	if c.Accept&AcceptID != 0 {
		b.WriteString("  -i, --id ID|NAME  target the live process with this id or name\n")
	}
	if c.Accept&AcceptDirect != 0 {
		b.WriteString("  --direct          run in this process, without the live process\n")
	}
	if c.Accept&AcceptBy != 0 {
		b.WriteString("  --by LABEL        author label for history (default $LINO_BY)\n")
	}
	b.WriteString("  --json            JSON output\n")
	return b.String()
}

// Parse resolves argv (without the program name) into a Call.
// Errors carry outcome usage.
func (r *Registry) Parse(argv []string, env Env) (*Call, error) {
	if len(argv) == 0 {
		return nil, outcome.New(outcome.Usage, "missing command").WithHint("lino help")
	}
	name := argv[0]
	cmd, ok := r.cmds[name]
	if !ok {
		return nil, outcome.New(outcome.Usage, "unknown command %q", name).WithHint("lino help")
	}
	return r.parseCommand(cmd, argv[1:], env)
}

func (r *Registry) parseCommand(cmd *Command, raw []string, env Env) (*Call, error) {
	getenv := env.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	c := &Call{Command: cmd, Raw: raw, Stdin: env.Stdin, Cwd: env.Cwd, Env: getenv}
	if c.Stdin == nil {
		c.Stdin = strings.NewReader("")
	}

	fs := flag.NewFlagSet(cmd.Name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	c.run = cmd.Setup(fs)
	own := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) { own[f.Name] = true })

	fs.BoolVar(&c.JSON, "json", false, "JSON output")
	if cmd.Accept&AcceptID != 0 {
		fs.StringVar(&c.ID, "i", "", "process id or name")
		fs.StringVar(&c.ID, "id", "", "process id or name")
	}
	if cmd.Accept&AcceptDirect != 0 {
		fs.BoolVar(&c.Direct, "direct", false, "run without the live process")
	}
	if cmd.Accept&AcceptBy != 0 {
		fs.StringVar(&c.By, "by", "", "author label for history")
	}

	args, err := parseInterspersed(fs, raw)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, errHelp{cmd}
		}
		return nil, outcome.New(outcome.Usage, "%s: %v", cmd.Name, err).WithHint(cmd.Synopsis())
	}
	c.Args = args
	if cmd.Accept&AcceptBy != 0 && c.By == "" {
		c.By = getenv("LINO_BY")
	}

	c.Flags = map[string][]string{}
	fs.Visit(func(f *flag.Flag) {
		if !own[f.Name] {
			return
		}
		if m, ok := f.Value.(interface{ Values() []string }); ok {
			c.Flags[f.Name] = append([]string(nil), m.Values()...)
		} else {
			c.Flags[f.Name] = []string{f.Value.String()}
		}
	})

	if n := len(args); n < cmd.MinArgs || (cmd.MaxArgs >= 0 && n > cmd.MaxArgs) {
		msg := "too many arguments"
		if n < cmd.MinArgs {
			msg = "missing arguments"
		}
		return nil, outcome.New(outcome.Usage, "%s: %s", cmd.Name, msg).WithHint(cmd.Synopsis())
	}
	return c, nil
}

// parseInterspersed lets flags appear before and after positional arguments.
// Everything after "--" is positional.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		consumed := len(args) - len(rest)
		if consumed > 0 && args[consumed-1] == "--" && !takesValue(fs, args, consumed-2) {
			return append(pos, rest...), nil
		}
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// takesValue reports whether args[i] is a flag that consumed args[i+1] as its value.
func takesValue(fs *flag.FlagSet, args []string, i int) bool {
	if i < 0 {
		return false
	}
	a := args[i]
	if !strings.HasPrefix(a, "-") || strings.Contains(a, "=") {
		return false
	}
	f := fs.Lookup(strings.TrimLeft(a, "-"))
	if f == nil {
		return false
	}
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return !ok || !b.IsBoolFlag()
}

// Argv rebuilds an argv (after the command name) from positional args and
// command flags, e.g. from a socket request. Globals are not included.
func Argv(args []string, flags map[string][]string) []string {
	names := make([]string, 0, len(flags))
	for n := range flags {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		for _, v := range flags[n] {
			out = append(out, "--"+n+"="+v)
		}
	}
	out = append(out, "--")
	return append(out, args...)
}

// ParseArgs builds a Call for the named command from positional args and
// flags, as the live server receives them. Globals are set by the caller.
func (r *Registry) ParseArgs(name string, args []string, flags map[string][]string, env Env) (*Call, error) {
	cmd, ok := r.cmds[name]
	if !ok {
		return nil, outcome.New(outcome.Usage, "unknown command %q", name).WithHint("lino help")
	}
	return r.parseCommand(cmd, Argv(args, flags), env)
}

type errHelp struct{ cmd *Command }

func (errHelp) Error() string { return "help requested" }

// Main parses argv, runs the command and prints its result. It returns the
// exit code. "-h" and "--help" print usage; so does "help" unless a "help"
// command is registered.
func (r *Registry) Main(ctx context.Context, argv []string, env Env, stdout, stderr io.Writer) int {
	p := &output.Printer{Stdout: stdout, Stderr: stderr, JSON: wantsJSON(argv)}
	if len(argv) > 0 {
		_, custom := r.cmds["help"]
		if argv[0] == "-h" || argv[0] == "--help" || (argv[0] == "help" && !custom) {
			if len(argv) > 1 {
				if cmd, ok := r.cmds[argv[1]]; ok {
					fmt.Fprint(stdout, cmd.Help())
					return 0
				}
			}
			r.Usage(stdout)
			return 0
		}
	}
	c, err := r.Parse(argv, env)
	var h errHelp
	if errors.As(err, &h) {
		fmt.Fprint(stdout, h.cmd.Help())
		return 0
	}
	if err != nil {
		if len(argv) == 0 {
			r.Usage(stderr)
		}
		return p.Error(err)
	}
	p.JSON = c.JSON
	if r.Forward != nil {
		if exit, ok := r.Forward(ctx, c, stdout, stderr); ok {
			return exit
		}
	}
	res, err := c.Run(ctx)
	if err != nil {
		return p.Error(err)
	}
	return p.Result(res)
}

func wantsJSON(argv []string) bool {
	for _, a := range argv {
		if a == "--" {
			return false
		}
		if a == "--json" || a == "-json" || a == "--json=true" {
			return true
		}
	}
	return false
}
