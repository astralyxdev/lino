package harness

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/astralyx/lino/internal/initcmd"
	"github.com/astralyx/lino/internal/live"
	"github.com/astralyx/lino/internal/statscmd"
)

// Config is one harness run.
type Config struct {
	Bin      string         // lino binary; empty builds ./cmd/lino into the work dir
	Corpus   string         // source tree or .tar.gz archive to copy
	Sub      string         // directory inside Corpus to use ("" for all)
	MaxLines int            // corpus cap (0: whole tree)
	WorkDir  string         // empty: a new temp dir, removed afterwards unless Keep
	Keep     bool           // keep the work dir
	Match    *regexp.Regexp // scenarios to run; nil runs all
	Iters    int
	Warmup   int
	Log      io.Writer
}

// maxHome leaves room for "/.lino/run/<id>.sock" within the 104-byte
// sockaddr_un path limit (macOS; Linux allows 108).
const maxHome = 80

// Run prepares a root from the corpus, initialises lino, starts the live
// process, runs the matching scenarios and returns the report. A failing
// scenario is recorded in Report.Errors; the rest still run.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	work := cfg.WorkDir
	if work == "" {
		d, err := os.MkdirTemp("", "lb-")
		if err != nil {
			return nil, err
		}
		work = d
		if !cfg.Keep {
			defer os.RemoveAll(work)
		}
	}
	work, err := filepath.EvalSymlinks(work)
	if err != nil {
		return nil, err
	}
	e := &Env{
		Bin:    cfg.Bin,
		Root:   filepath.Join(work, "root"),
		Home:   filepath.Join(work, "h"),
		Iters:  max(cfg.Iters, 1),
		Warmup: max(cfg.Warmup, 0),
		Log:    cfg.Log,
	}
	if len(e.Home) > maxHome {
		// the live process's unix socket lives under HOME; keep it short
		d, err := os.MkdirTemp("/tmp", "lb-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(d)
		e.Home = d
	}
	for _, d := range []string{e.Root, e.Home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	if e.Bin == "" {
		e.Bin = filepath.Join(work, "lino")
		e.Logf("building lino -> %s", e.Bin)
		if err := buildLino(ctx, e.Bin); err != nil {
			return nil, err
		}
	}

	rep := &Report{Started: time.Now().UTC(), Machine: machine(), Lino: linoInfo(ctx, e)}
	e.Logf("copying corpus %s (max %d lines)", cfg.Corpus, cfg.MaxLines)
	if e.Corpus, err = CopyCorpus(cfg.Corpus, cfg.Sub, e.Root, cfg.MaxLines); err != nil {
		return nil, fmt.Errorf("corpus: %w", err)
	}
	rep.Corpus = e.Corpus
	e.Logf("corpus: %d files, %d lines, %.1f MB", e.Corpus.Files, e.Corpus.Lines, float64(e.Corpus.Bytes)/1e6)

	e.Logf("lino init")
	var in initcmd.Data
	start := time.Now()
	if _, err := e.JSON(ctx, &in, "init"); err != nil {
		return nil, err
	}
	rep.add(Metric{Scenario: "setup", Name: "init wall time", Value: time.Since(start).Seconds(), Unit: "s", Target: Under(30), // scope.md: first init on 1M lines < 30 s
		Note: fmt.Sprintf("%d files indexed", in.Files)})
	var rd live.RunData
	if _, err := e.JSON(ctx, &rd, "run"); err != nil {
		return nil, err
	}
	defer e.Run(context.WithoutCancel(ctx), "", "stop")

	for _, s := range Scenarios() {
		if cfg.Match != nil && !cfg.Match.MatchString(s.Name) {
			continue
		}
		e.Logf("scenario %s", s.Name)
		ms, err := s.Run(ctx, e)
		for _, m := range ms {
			if m.Scenario == "" {
				m.Scenario = s.Name
			}
			rep.add(m)
		}
		if err != nil {
			rep.Errors = append(rep.Errors, ScenarioError{Scenario: s.Name, Error: err.Error()})
			e.Logf("scenario %s failed: %v", s.Name, err)
		}
		if ctx.Err() != nil {
			return rep, ctx.Err()
		}
	}

	var st statscmd.Data
	if _, err := e.JSON(ctx, &st, "stats"); err != nil {
		rep.Errors = append(rep.Errors, ScenarioError{Scenario: "stats", Error: err.Error()})
	} else {
		rep.Stats = &st
	}
	rep.Finished = time.Now().UTC()
	return rep, nil
}

// buildLino builds lino at out and lino-core next to it.
func buildLino(ctx context.Context, out string) error {
	c := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", filepath.Dir(out)+string(filepath.Separator),
		"github.com/astralyx/lino/cmd/lino", "github.com/astralyx/lino/cmd/lino-core")
	c.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("build lino: %v\n%s", err, b)
	}
	return nil
}

func machine() Machine {
	m := Machine{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), GoVersion: runtime.Version()}
	if b, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
		m.CPU = strings.TrimSpace(string(b))
	} else if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "model name" {
				m.CPU = strings.TrimSpace(v)
				break
			}
		}
	}
	return m
}

func linoInfo(ctx context.Context, e *Env) Lino {
	var l Lino
	if r, err := e.Run(ctx, "", "--version"); err == nil {
		l.Version = strings.TrimSpace(string(r.Stdout))
	}
	if b, err := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD").Output(); err == nil {
		l.Commit = strings.TrimSpace(string(b))
	}
	return l
}
