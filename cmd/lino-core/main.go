// Command lino-core is the part of lino that links the index and history:
// the live process, --direct mode, init and every command the thin lino
// client does not forward itself. Users run lino, which execs lino-core.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/astralyx/lino/internal/autostart"
	"github.com/astralyx/lino/internal/cli"
	_ "github.com/astralyx/lino/internal/commands"
	"github.com/astralyx/lino/internal/live"
)

// Set with -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "none"
)

func main() {
	live.BuildVersion = version
	autostart.Install(cli.Default)
	cwd, _ := os.Getwd()
	env := cli.Env{Stdin: os.Stdin, Cwd: cwd, Getenv: os.Getenv}
	os.Exit(run(os.Args[1:], env, os.Stdout, os.Stderr))
}

func run(args []string, env cli.Env, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		fmt.Fprintf(stdout, "lino %s (%s)\n", version, commit)
		return 0
	}
	return cli.Default.Main(context.Background(), args, env, stdout, stderr)
}
