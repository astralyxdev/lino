package tokens

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// AgentFunc builds the agent command for one run: the arm's system prompt
// addition and the task prompt. The command runs in the task root and must
// write Claude Code stream-json events to stdout.
type AgentFunc func(system, prompt string) []string

// RunConfig configures RunOne.
type RunConfig struct {
	Corpus  string    // pinned archive (bench/corpus.sh)
	Work    string    // directory for per-run roots and transcripts
	Lino    string    // lino binary for the lino arm
	Agent   AgentFunc // agent command
	Timeout time.Duration
	Keep    bool // keep each run's root
	Log     io.Writer
}

// RunResult is one task attempted in one arm.
type RunResult struct {
	Task       string     `json:"task"`
	Arm        string     `json:"arm"`
	Rep        int        `json:"rep"`
	OK         bool       `json:"ok"`
	Check      Result     `json:"check"`
	Transcript Transcript `json:"transcript"`
	Seconds    float64    `json:"seconds"`
	Error      string     `json:"error,omitempty"` // the run itself failed (not the task)
}

func (c *RunConfig) logf(format string, args ...any) {
	if c.Log != nil {
		fmt.Fprintf(c.Log, format+"\n", args...)
	}
}

// RunOne prepares a fresh root, runs the agent on task in arm, and checks the
// result. Everything the run produced stays in Work/<task>-<arm>-<rep>/:
// transcript.jsonl, agent.stderr and result.json (and root/ with Keep).
func RunOne(ctx context.Context, cfg RunConfig, task *Task, arm Arm, rep int) RunResult {
	res := RunResult{Task: task.ID, Arm: arm.Name, Rep: rep}
	dir := filepath.Join(cfg.Work, fmt.Sprintf("%s-%s-%d", task.ID, arm.Name, rep))
	if err := runOne(ctx, cfg, task, arm, dir, &res); err != nil {
		res.Error = err.Error()
	}
	if b, err := json.MarshalIndent(res, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "result.json"), b, 0o644)
	}
	return res
}

func runOne(ctx context.Context, cfg RunConfig, task *Task, arm Arm, dir string, res *RunResult) error {
	root, bin := filepath.Join(dir, "root"), filepath.Join(dir, "bin")
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	if !cfg.Keep {
		defer os.RemoveAll(root)
	}
	cfg.logf("%s %s: preparing root", task.ID, arm.Name)
	pristine, err := Prepare(cfg.Corpus, root)
	if err != nil {
		return err
	}
	for _, name := range arm.Deny {
		stub := "#!/bin/sh\necho \"" + name + ": not available in this benchmark arm\" >&2\nexit 127\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(stub), 0o755); err != nil {
			return err
		}
	}
	env := append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"LINO_BY=agent", "LINO_BENCH_TASK="+task.ID, "LINO_BENCH_ARM="+arm.Name)
	system := arm.Prompt
	if arm.Lino {
		if cfg.Lino == "" {
			return fmt.Errorf("lino arm needs a lino binary")
		}
		if err := os.Symlink(cfg.Lino, filepath.Join(bin, "lino")); err != nil {
			return err
		}
		if _, err := command(ctx, root, env, "", cfg.Lino, "init"); err != nil {
			return err
		}
		defer command(context.WithoutCancel(ctx), root, env, "", cfg.Lino, "stop")
		block, err := command(ctx, root, env, "", cfg.Lino, "help", "--agent")
		if err != nil {
			return err
		}
		system += "\n\n" + block
	}

	args := cfg.Agent(system, task.Prompt)
	actx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	out, err := os.Create(filepath.Join(dir, "transcript.jsonl"))
	if err != nil {
		return err
	}
	defer out.Close()
	var stderr bytes.Buffer
	c := exec.CommandContext(actx, args[0], args[1:]...)
	c.Dir, c.Env, c.Stdout, c.Stderr = root, env, out, &stderr
	cfg.logf("%s %s: running agent", task.ID, arm.Name)
	start := time.Now()
	runErr := c.Run()
	res.Seconds = time.Since(start).Seconds()
	_ = os.WriteFile(filepath.Join(dir, "agent.stderr"), stderr.Bytes(), 0o644)

	f, err := os.Open(out.Name())
	if err != nil {
		return err
	}
	defer f.Close()
	if res.Transcript, err = ParseStream(f); err != nil {
		return err
	}
	if res.Check, err = task.Check(root, pristine); err != nil {
		return err
	}
	res.OK = res.Check.OK
	if runErr != nil {
		return fmt.Errorf("agent: %v: %s", runErr, lastLine(stderr.String()))
	}
	return nil
}

func command(ctx context.Context, dir string, env []string, stdin, name string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir, c.Env, c.Stdin = dir, env, strings.NewReader(stdin)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %v: %s", filepath.Base(name), strings.Join(args, " "), err, lastLine(errb.String()))
	}
	return out.String(), nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	return s[strings.LastIndexByte(s, '\n')+1:]
}

// ClaudeCode is the default agent: Claude Code in print mode with only its
// Bash tool, so every file operation shows up as a shell command.
func ClaudeCode(bin, model string) AgentFunc {
	return func(system, prompt string) []string {
		args := []string{bin, "-p", "--output-format", "stream-json", "--verbose",
			"--permission-mode", "bypassPermissions",
			"--allowedTools", "Bash",
			"--disallowedTools", "Read,Edit,MultiEdit,Write,NotebookEdit,Glob,Grep,LS,WebFetch,WebSearch,Task,TodoWrite",
			"--append-system-prompt", system}
		if model != "" {
			args = append(args, "--model", model)
		}
		return append(args, prompt)
	}
}
