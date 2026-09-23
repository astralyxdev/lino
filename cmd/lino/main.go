// Command lino is the thin client: it forwards calls to a live process and
// execs lino-core for everything else. It must not link the index (SQLite);
// TestNoSQLite checks that.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/thin"
)

// Set with -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "none"
)

func main() {
	cwd, _ := os.Getwd()
	env := cli.Env{Stdin: os.Stdin, Cwd: cwd, Getenv: os.Getenv}
	os.Exit(run(os.Args[1:], env, os.Stdout, os.Stderr, thin.Exec))
}

func run(args []string, env cli.Env, stdout, stderr io.Writer, handoff func([]string) error) int {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		fmt.Fprintf(stdout, "lino %s (%s)\n", version, commit)
		return 0
	}
	return thin.Main(context.Background(), args, env, stdout, stderr, handoff)
}
