// Command tokenbench runs the token benchmark: every task of bench/tokens in
// three arms (grep+cat+sed, ripgrep, lino with its help --agent block), with
// an agent that works only through a bash tool, and writes results.md and
// results.json. See bench/tokens/run.sh.
//
// The default agent is Claude Code in print mode (claude -p), which calls a
// paid API. -dry replaces it with a scripted agent that applies the reference
// solutions, to validate the harness offline.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/astralyx/lino/bench/tokens"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "fake-agent" {
		if err := fakeAgent(); err != nil {
			fmt.Fprintln(os.Stderr, "fake-agent:", err)
			os.Exit(1)
		}
		return
	}
	var (
		corpus  = flag.String("corpus", filepath.Join("bench", ".corpus", "go-"+strings.TrimPrefix(tokens.Pin, "golang/go@")+".tar.gz"), "pinned archive (bench/corpus.sh)")
		work    = flag.String("work", "", "work directory (default: a temp dir)")
		out     = flag.String("out", "bench/results/tokens", "directory for results.md and results.json")
		taskIDs = flag.String("tasks", "", "comma-separated task ids (default all)")
		armList = flag.String("arms", "grep,rg,lino", "comma-separated arms")
		reps    = flag.Int("reps", 1, "repetitions per task and arm")
		agent   = flag.String("agent", "claude", "Claude Code binary")
		model   = flag.String("model", "", "model passed to the agent (default: the agent's)")
		lino    = flag.String("lino", "", "lino binary (default: build ./cmd/lino)")
		timeout = flag.Duration("timeout", 20*time.Minute, "per-run agent timeout")
		keep    = flag.Bool("keep", false, "keep each run's root")
		dry     = flag.Bool("dry", false, "scripted agent applying reference solutions (no API calls)")
	)
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, *corpus, *work, *out, *taskIDs, *armList, *reps, *agent, *model, *lino, *timeout, *keep, *dry); err != nil {
		fmt.Fprintln(os.Stderr, "tokenbench:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, corpus, work, out, taskIDs, armList string, reps int, agent, model, lino string,
	timeout time.Duration, keep, dry bool) error {
	var tasks []*tokens.Task
	if taskIDs == "" {
		for i := range tokens.Tasks {
			tasks = append(tasks, &tokens.Tasks[i])
		}
	}
	for _, id := range split(taskIDs) {
		t, err := tokens.Get(id)
		if err != nil {
			return err
		}
		tasks = append(tasks, t)
	}
	var arms []tokens.Arm
	for _, name := range split(armList) {
		a, ok := tokens.GetArm(name)
		if !ok {
			return fmt.Errorf("unknown arm %q", name)
		}
		arms = append(arms, a)
	}
	if work == "" {
		d, err := os.MkdirTemp("", "lt-")
		if err != nil {
			return err
		}
		work = d
		fmt.Fprintln(os.Stderr, "work dir:", work)
	}
	if lino == "" {
		lino = filepath.Join(work, "lino")
		c := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", filepath.Dir(lino)+string(filepath.Separator),
			"github.com/astralyx/lino/cmd/lino", "github.com/astralyx/lino/cmd/lino-core")
		c.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("build lino: %v\n%s", err, b)
		}
	}
	lino, _ = filepath.Abs(lino)
	cfg := tokens.RunConfig{Corpus: corpus, Work: work, Lino: lino, Timeout: timeout, Keep: keep, Log: os.Stderr,
		Agent: tokens.ClaudeCode(agent, model)}
	rep := &tokens.Report{Agent: "Claude Code (" + agent + ")", Model: model, Pin: tokens.Pin, Dry: dry}
	if dry {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		cfg.Agent = func(system, prompt string) []string { return []string{self, "fake-agent"} }
		rep.Agent, rep.Model = "scripted (reference solutions)", ""
	}
	for _, a := range arms {
		rep.Arms = append(rep.Arms, a.Name)
	}
	for r := range reps {
		for _, t := range tasks {
			for _, a := range arms {
				res := tokens.RunOne(ctx, cfg, t, a, r)
				fmt.Fprintf(os.Stderr, "%s %s #%d: ok=%v tokens=%d commands=%d %s\n", t.ID, a.Name, r, res.OK,
					res.Transcript.Usage.Total(), res.Transcript.Ops.Commands, res.Error)
				rep.Results = append(rep.Results, res)
				if err := rep.Write(out); err != nil { // keep partial results
					return err
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
			}
		}
	}
	return rep.Markdown(os.Stdout)
}

func split(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// fakeAgent stands in for the agent under -dry: it reports one search and
// one edit command in the arm's style, applies the task's reference
// solution to the working directory, and emits stream-json like claude -p.
func fakeAgent() error {
	t, err := tokens.Get(os.Getenv("LINO_BENCH_TASK"))
	if err != nil {
		return err
	}
	cmds := map[string][]string{
		"grep": {"grep -rn TODO . | head", "sed -n 1,20p " + t.Allowed[0]},
		"rg":   {"rg -n TODO | head", "sed -n 1,20p " + t.Allowed[0]},
		"lino": {"lino search TODO", "lino read " + t.Allowed[0] + " --lines 1:20 --anchors"},
	}[os.Getenv("LINO_BENCH_ARM")]
	enc := json.NewEncoder(os.Stdout)
	for i, c := range cmds {
		id := fmt.Sprintf("toolu_%d", i)
		enc.Encode(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]string{"command": c}}}}})
		enc.Encode(map[string]any{"type": "user", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": id, "content": "(not run)"}}}})
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := t.Solve(wd); err != nil {
		return err
	}
	return enc.Encode(map[string]any{"type": "result", "num_turns": len(cmds) + 1, "is_error": false, "result": "done",
		"usage": map[string]int{"input_tokens": 0, "output_tokens": 0}})
}
