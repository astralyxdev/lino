// Package tokens is the fixed task set for the token benchmark (scope.md,
// Targets > Tokens): realistic agent coding tasks on a pinned repository,
// each with an automatic success check and a reference solution.
//
// The repository is the src/ tree of golang/go at go1.24.0, the same pinned
// archive bench/corpus.sh fetches for the performance benchmarks, copied in
// full (about 2.4M lines). A runner prepares a fresh copy per task and arm,
// hands the agent Task.Prompt, and afterwards runs Check against the tree.
// Checks are syntactic and semantic where possible (Go parsing, constant
// evaluation, JSON decoding) and always verify that nothing outside the
// task's files changed. The go1.24 tree cannot be compiled by the local
// toolchain, so checks do not build or test the code.
//
// bench/tokens/cmd/tokentask lists, prepares, checks and solves tasks.
package tokens

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/astralyx/lino/bench/harness"
)

// Corpus pin and the directory of the archive copied into a task root.
const (
	Pin = "golang/go@3901409b5d0fb7c85a3e6730a59943cc93b2835c" // go1.24.0
	Sub = "src"
)

// Category groups tasks by the kind of work they exercise.
type Category string

const (
	FindEdit  Category = "find-edit"  // locate the code from a description, then edit it
	LargeFile Category = "large-file" // edit inside a file of several thousand lines
	Rename    Category = "rename"     // rename an identifier across files
	MultiFile Category = "multi-file" // coordinated edits in several files
	Insert    Category = "insert"     // add code at a precise place
	NewFile   Category = "new-file"   // create a file
	Move      Category = "move"       // move files
	Delete    Category = "delete"     // remove code
	Config    Category = "config"     // non-Go configuration files
)

// Task is one benchmark task.
type Task struct {
	ID       string   `json:"id"`
	Category Category `json:"category"`
	Title    string   `json:"title"`
	Prompt   string   `json:"prompt"`  // what the agent is told; paths are relative to the root
	Allowed  []string `json:"allowed"` // paths that may change: a file, or a directory ending in /
	check    func(c *C)
	solve    func(root string) error
}

// Manifest maps every slash path in a pristine root to its sha256.
type Manifest map[string]string

// Prepare copies the pinned tree from corpus (the archive or a checkout)
// into dir and returns its manifest.
func Prepare(corpus, dir string) (Manifest, error) {
	if _, err := harness.CopyCorpus(corpus, Sub, dir, 0); err != nil {
		return nil, err
	}
	return Scan(dir)
}

// ignored are paths lino and the runner may create in a root.
func ignored(rel string) bool {
	return rel == ".lino" || strings.HasPrefix(rel, ".lino/") || rel == ".linoignore"
}

// Scan hashes every file under root.
func Scan(root string) (Manifest, error) {
	m := Manifest{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if ignored(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		m[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	return m, err
}

// Diff lists the paths whose content differs between m and now, sorted.
func (m Manifest) Diff(now Manifest) []string {
	var out []string
	for p, h := range now {
		if m[p] != h {
			out = append(out, p)
		}
	}
	for p := range m {
		if _, ok := now[p]; !ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// Save writes m as JSON.
func (m Manifest) Save(name string) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(name, b, 0o644)
}

// LoadManifest reads a manifest written by Save.
func LoadManifest(name string) (Manifest, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var m Manifest
	return m, json.Unmarshal(b, &m)
}

// Result is the outcome of checking one task.
type Result struct {
	Task    string   `json:"task"`
	OK      bool     `json:"ok"`
	Changed []string `json:"changed"`
	Errors  []string `json:"errors,omitempty"`
}

// Check verifies root after an attempt at t, against the pristine manifest.
func (t *Task) Check(root string, pristine Manifest) (Result, error) {
	now, err := Scan(root)
	if err != nil {
		return Result{}, err
	}
	c := &C{root: root, pristine: pristine}
	res := Result{Task: t.ID, Changed: pristine.Diff(now)}
	for _, p := range res.Changed {
		if !t.allowed(p) {
			c.Errorf("%s changed, but the task does not touch it", p)
		}
	}
	if len(res.Changed) == 0 {
		c.Errorf("nothing changed")
	}
	c.parseChanged(res.Changed)
	t.check(c)
	res.Errors, res.OK = c.errs, len(c.errs) == 0
	return res, nil
}

func (t *Task) allowed(p string) bool {
	for _, a := range t.Allowed {
		if p == a || strings.HasSuffix(a, "/") && strings.HasPrefix(p, a) {
			return true
		}
	}
	return false
}

// Solve applies the reference solution to a pristine root.
func (t *Task) Solve(root string) error { return t.solve(root) }

// Lookup returns the task with id.
func Lookup(id string) (*Task, bool) {
	for i := range Tasks {
		if Tasks[i].ID == id {
			return &Tasks[i], true
		}
	}
	return nil, false
}

var errNoTask = errors.New("no such task")

// Get is Lookup with an error.
func Get(id string) (*Task, error) {
	if t, ok := Lookup(id); ok {
		return t, nil
	}
	return nil, fmt.Errorf("%w: %s", errNoTask, id)
}
