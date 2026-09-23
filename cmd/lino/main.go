package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/astralyx/lino/internal/autostart"
	_ "github.com/astralyx/lino/internal/changelog"
	_ "github.com/astralyx/lino/internal/changescmd"
	"github.com/astralyx/lino/internal/cli"
	_ "github.com/astralyx/lino/internal/filecmd"
	_ "github.com/astralyx/lino/internal/helpcmd"
	_ "github.com/astralyx/lino/internal/historycmd"
	_ "github.com/astralyx/lino/internal/histprune"
	_ "github.com/astralyx/lino/internal/histrec"
	_ "github.com/astralyx/lino/internal/initcmd"
	"github.com/astralyx/lino/internal/live"
	_ "github.com/astralyx/lino/internal/lscmd"
	_ "github.com/astralyx/lino/internal/metrics"
	_ "github.com/astralyx/lino/internal/pscmd"
	_ "github.com/astralyx/lino/internal/reindex"
	_ "github.com/astralyx/lino/internal/rollback"
	_ "github.com/astralyx/lino/internal/search"
	_ "github.com/astralyx/lino/internal/showcmd"
	_ "github.com/astralyx/lino/internal/statscmd"
	_ "github.com/astralyx/lino/internal/statuscmd"
	_ "github.com/astralyx/lino/internal/stopcmd"
	_ "github.com/astralyx/lino/internal/vcheck"
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
