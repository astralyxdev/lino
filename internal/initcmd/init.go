// Package initcmd implements "lino init": create .lino/ in a root and build
// its index.
package initcmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/config"
	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/fileio"
	"github.com/astralyx/lino/internal/ignore"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/paths"
	"github.com/astralyx/lino/internal/registry"
)

func init() { cli.Register(Command) }

// Command is `lino init [dir]`.
var Command = &cli.Command{
	Name:    "init",
	Usage:   "[dir]",
	Summary: "create .lino/ and build the index",
	Accept:  cli.AcceptBy,
	MinArgs: 0,
	MaxArgs: 1,
	Setup: func(fs *flag.FlagSet) cli.RunFunc {
		return func(ctx context.Context, c *cli.Call) (output.Result, error) {
			return Run(ctx, Request{Cwd: c.Cwd, Dir: c.Arg(0), Linoignore: true, By: c.By})
		}
	},
}

// Data is the result of an init.
type Data struct {
	Root        string `json:"root"`
	ID          string `json:"id"`
	Existed     bool   `json:"existed"`      // .lino/ was already there
	GitExcluded bool   `json:"git_excluded"` // .lino/ is listed in .git/info/exclude
	Files       int    `json:"files"`
	Added       int    `json:"added"`
	Modified    int    `json:"modified"`
	Removed     int    `json:"removed"`
	Moved       int    `json:"moved"`
	// CreatedLinoignore is set when init wrote the default .linoignore.
	CreatedLinoignore bool  `json:"created_linoignore,omitempty"`
	DurationMS        int64 `json:"duration_ms"`
}

// WriteText prints a short summary and the next step.
func (d Data) WriteText(w io.Writer) error {
	verb := "initialised"
	if d.Existed {
		verb = "already initialised"
	}
	if d.CreatedLinoignore {
		if _, err := fmt.Fprintln(w, "created .linoignore with defaults"); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "%s %s id=%s\nindexed %d files (%d added, %d modified, %d removed, %d moved) in %dms\nnext: lino run %s\n",
		verb, d.Root, d.ID, d.Files, d.Added, d.Modified, d.Removed, d.Moved, d.DurationMS, d.Root)
	return err
}

// DefaultConfig is written to a new .lino/config. Every key is commented out
// so the built-in defaults apply until the user sets one.
func DefaultConfig() string {
	c := config.Default()
	return fmt.Sprintf(`# lino config: key = value. Uncomment to override a default.
# read.lines = %d
# search.hits = %d
# search.max_hits = %d
# search.line_chars = %d
# ls.entries = %d
# changes.events = %d
# history.retention = 14d
# history.max_size = 1GB
# idle = 2h
# file.max_size = 16MB
`, c.ReadLines, c.SearchHits, c.SearchMaxHits, c.LineChars, c.LsEntries, c.ChangesEvents)
}

// Request is one init call.
type Request struct {
	Cwd string
	Dir string // relative to Cwd; "" means Cwd
	// Linoignore writes the default .linoignore when the root has none,
	// before the first reconcile so its patterns apply to the initial index,
	// and logs it as a lino write by By. An existing file is never touched.
	Linoignore bool
	By         string
}

// Init initialises dir (relative to cwd; "" means cwd) without creating a
// .linoignore; the command runs Run with Linoignore set. Re-running it on an
// initialised root is safe: it only reconciles the index again.
func Init(ctx context.Context, cwd, dir string) (output.Result, error) {
	return Run(ctx, Request{Cwd: cwd, Dir: dir})
}

// Run initialises the root described by req; see Request and Init.
func Run(ctx context.Context, req Request) (output.Result, error) {
	cwd, dir := req.Cwd, req.Dir
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
		}
		cwd = wd
	}
	if dir == "" {
		dir = cwd
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	st, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return output.Result{}, outcome.New(outcome.NotFound, "%s: no such directory", dir)
	case err != nil:
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
	case !st.IsDir():
		return output.Result{}, outcome.New(outcome.Refused, "%s: not a directory", dir)
	}
	root, err := paths.NewRoot(dir)
	if err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
	}
	rp := root.Path()
	if err := checkNesting(rp); err != nil {
		return output.Result{}, err
	}

	meta := filepath.Join(rp, fileio.MetaDir)
	d := Data{Root: rp, ID: registry.IDFor(rp)}
	if _, err := os.Stat(meta); err == nil {
		d.Existed = true
	} else if err := os.Mkdir(meta, 0o755); err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
	}
	cfgPath := filepath.Join(meta, config.FileName)
	if _, err := os.Stat(cfgPath); errors.Is(err, fs.ErrNotExist) {
		if err := fileio.WriteAtomic(cfgPath, []byte(DefaultConfig()), 0o644); err != nil {
			return output.Result{}, err
		}
	}
	cfg, err := config.Load(rp)
	if err != nil {
		return output.Result{}, err
	}
	if d.GitExcluded, err = excludeFromGit(rp); err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "git exclude: "+err.Error())
	}

	if req.Linoignore {
		if d.CreatedLinoignore, err = ignore.EnsureLinoignore(rp); err != nil {
			return output.Result{}, outcome.Wrap(outcome.Internal, err, "create .linoignore: "+err.Error())
		}
	}

	start := time.Now()
	sum, err := reconcile(ctx, rp, cfg.MaxFileSize)
	if err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "index: "+err.Error())
	}
	if d.CreatedLinoignore {
		if err := filecmd.RecordCreated(ctx, rp, ignore.LinoignoreFile, req.By); err != nil {
			return output.Result{}, outcome.Wrap(outcome.Internal, err, "record .linoignore: "+err.Error())
		}
	}
	d.Files, d.Added, d.Modified, d.Removed, d.Moved = sum.Files, sum.Added, sum.Modified, sum.Removed, sum.Moved
	d.DurationMS = time.Since(start).Milliseconds()

	res := output.Result{Outcome: outcome.Created, Data: d}
	if d.Existed {
		res.Outcome = outcome.OK
	}
	return res, nil
}

func reconcile(ctx context.Context, root string, maxSize int64) (index.Summary, error) {
	db, err := index.Open(ctx, root)
	if err != nil {
		return index.Summary{}, err
	}
	defer db.Close()
	rules, err := ignore.NewRules(root)
	if err != nil {
		return index.Summary{}, err
	}
	return db.Reconcile(ctx, rules, maxSize)
}

// checkNesting refuses a root inside another initialised root or containing one.
func checkNesting(root string) error {
	for dir := filepath.Dir(root); ; dir = filepath.Dir(dir) {
		if paths.IsRoot(dir) {
			return outcome.New(outcome.Refused, "%s is inside the lino root %s", root, dir).
				WithHint("use the existing root, or remove " + filepath.Join(dir, fileio.MetaDir))
		}
		if parent := filepath.Dir(dir); parent == dir {
			break
		}
	}
	var nested string
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil // unreadable subtree: nothing lino could index there either
		}
		if !e.IsDir() || p == root {
			return nil
		}
		switch e.Name() {
		case ignore.GitDir:
			return fs.SkipDir
		case fileio.MetaDir:
			if filepath.Dir(p) != root && paths.IsRoot(filepath.Dir(p)) {
				nested = filepath.Dir(p)
				return fs.SkipAll
			}
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return outcome.Wrap(outcome.Internal, err, "")
	}
	if nested != "" {
		return outcome.New(outcome.Refused, "%s contains the lino root %s", root, nested).
			WithHint("initialise " + nested + " on its own, or remove " + filepath.Join(nested, fileio.MetaDir))
	}
	return nil
}

func isMeta(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// excludeLine is what init adds to .git/info/exclude.
const excludeLine = "/" + fileio.MetaDir + "/"

// excludeFromGit adds .lino/ to .git/info/exclude when root is a git work
// tree with a .git directory. It reports whether the entry is present.
func excludeFromGit(root string) (bool, error) {
	gitDir := filepath.Join(root, ignore.GitDir)
	if !isMeta(gitDir) {
		return false, nil
	}
	p := filepath.Join(gitDir, "info", "exclude")
	old, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	sc := bufio.NewScanner(bytes.NewReader(old))
	for sc.Scan() {
		switch strings.TrimSpace(sc.Text()) {
		case excludeLine, fileio.MetaDir + "/", fileio.MetaDir, "/" + fileio.MetaDir:
			return true, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return false, err
	}
	buf := append([]byte(nil), old...)
	if len(buf) > 0 && buf[len(buf)-1] != '\n' {
		buf = append(buf, '\n')
	}
	buf = append(buf, excludeLine+"\n"...)
	mode := fs.FileMode(0o644)
	if st, err := os.Stat(p); err == nil {
		mode = st.Mode().Perm()
	}
	return true, fileio.WriteAtomic(p, buf, mode)
}
