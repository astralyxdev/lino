// Command linobench runs the lino benchmark scenarios on a corpus and writes
// results.md and results.json. See bench/run.sh.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"regexp"

	"github.com/astralyx/lino/bench/harness"
	_ "github.com/astralyx/lino/bench/scenarios"
)

func main() {
	var (
		cfg  harness.Config
		out  = flag.String("out", "bench/results", "directory for results.md and results.json")
		run  = flag.String("run", "", "regexp selecting scenarios (default all)")
		list = flag.Bool("list", false, "list scenarios and exit")
	)
	flag.StringVar(&cfg.Corpus, "corpus", "", "source tree or .tar.gz archive to benchmark on (bench/corpus.sh prints one)")
	flag.StringVar(&cfg.Sub, "sub", "", "directory inside the corpus to use, e.g. src")
	flag.IntVar(&cfg.MaxLines, "maxlines", 1_000_000, "copy at most this many corpus lines (0: all)")
	flag.StringVar(&cfg.Bin, "lino", "", "lino binary (default: build ./cmd/lino)")
	flag.StringVar(&cfg.WorkDir, "work", "", "work directory (default: a temp dir)")
	flag.BoolVar(&cfg.Keep, "keep", false, "keep the temp work directory")
	flag.IntVar(&cfg.Iters, "iters", 50, "timed iterations per measurement")
	flag.IntVar(&cfg.Warmup, "warmup", 3, "untimed iterations before them")
	flag.Parse()

	if *list {
		for _, s := range harness.Scenarios() {
			fmt.Printf("%-24s %s\n", s.Name, s.Doc)
		}
		return
	}
	if cfg.Corpus == "" {
		fatal(fmt.Errorf("-corpus is required"))
	}
	if *run != "" {
		re, err := regexp.Compile(*run)
		if err != nil {
			fatal(err)
		}
		cfg.Match = re
	}
	cfg.Log = os.Stderr

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	rep, err := harness.Run(ctx, cfg)
	if rep != nil {
		if werr := rep.Write(*out); werr != nil {
			fatal(werr)
		}
		if merr := rep.Markdown(os.Stdout); merr != nil {
			fatal(merr)
		}
	}
	if err != nil {
		fatal(err)
	}
	if len(rep.Errors) > 0 {
		os.Exit(1)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "linobench:", err)
	os.Exit(2)
}
