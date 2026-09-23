// Command tokentask drives the token benchmark task set.
//
//	tokentask list [-json]                 tasks, one per line (or JSON with prompts)
//	tokentask prompt ID                    the prompt to give the agent
//	tokentask prepare [-corpus A] DIR      copy the pinned tree into DIR; manifest in DIR.manifest.json
//	tokentask check [-manifest M] ID DIR   check DIR after an attempt; JSON result, exit 1 on failure
//	tokentask solve ID DIR                 apply the reference solution
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/astralyx/lino/bench/tokens"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tokentask:", err)
		os.Exit(2)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: tokentask list|prompt|prepare|check|solve")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "list as JSON")
	corpus := fs.String("corpus", defaultCorpus(), "pinned archive or checkout (bench/corpus.sh)")
	manifest := fs.String("manifest", "", "manifest from prepare (default DIR.manifest.json)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	rest := fs.Args()
	need := func(n int) error {
		if len(rest) != n {
			return fmt.Errorf("%s: want %d arguments, got %d", args[0], n, len(rest))
		}
		return nil
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	switch args[0] {
	case "list":
		if *asJSON {
			return enc.Encode(tokens.Tasks)
		}
		for _, t := range tokens.Tasks {
			fmt.Printf("%s  %-10s  %s\n", t.ID, t.Category, t.Title)
		}
	case "prompt":
		if err := need(1); err != nil {
			return err
		}
		t, err := tokens.Get(rest[0])
		if err != nil {
			return err
		}
		fmt.Println(t.Prompt)
	case "prepare":
		if err := need(1); err != nil {
			return err
		}
		dir := filepath.Clean(rest[0])
		m, err := tokens.Prepare(*corpus, dir)
		if err != nil {
			return err
		}
		return m.Save(dir + ".manifest.json")
	case "check", "solve":
		if err := need(2); err != nil {
			return err
		}
		t, err := tokens.Get(rest[0])
		if err != nil {
			return err
		}
		dir := filepath.Clean(rest[1])
		if args[0] == "solve" {
			return t.Solve(dir)
		}
		if *manifest == "" {
			*manifest = dir + ".manifest.json"
		}
		m, err := tokens.LoadManifest(*manifest)
		if err != nil {
			return err
		}
		res, err := t.Check(dir, m)
		if err != nil {
			return err
		}
		if err := enc.Encode(res); err != nil {
			return err
		}
		if !res.OK {
			os.Exit(1)
		}
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	return nil
}

func defaultCorpus() string {
	return filepath.Join("bench", ".corpus", "go-"+strings.TrimPrefix(tokens.Pin, "golang/go@")+".tar.gz")
}
