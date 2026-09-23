package ignore

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Names of directories that are never indexed, watched or walked.
const (
	LinoDir = ".lino"
	GitDir  = ".git"
)

// Rules combines .gitignore (and .git/info/exclude) with .linoignore on top:
// when a .linoignore pattern matches a path, its verdict wins over .gitignore.
// .lino and .git are always ignored. Rules is safe for concurrent use.
type Rules struct {
	root   string
	mu     sync.Mutex
	git    *Matcher
	lino   *Matcher
	loaded map[string]bool
}

// NewRules loads .git/info/exclude and the root's ignore files. root is
// canonicalised; nested ignore files are loaded by Walk or on demand by Ignored.
func NewRules(root string) (*Rules, error) {
	root, err := canonical(root)
	if err != nil {
		return nil, err
	}
	r := &Rules{root: root, git: New(), lino: New(), loaded: map[string]bool{}}
	if err := r.git.LoadFile("", filepath.Join(root, GitDir, "info", "exclude")); err != nil {
		return nil, err
	}
	if err := r.LoadDir(""); err != nil {
		return nil, err
	}
	return r, nil
}

// LoadDir loads .gitignore and .linoignore from root-relative directory rel
// ("" for the root). Each directory is loaded once.
func (r *Rules) LoadDir(rel string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loadDir(cleanRel(rel))
}

func (r *Rules) loadDir(rel string) error {
	if r.loaded[rel] {
		return nil
	}
	r.loaded[rel] = true
	dir := filepath.Join(r.root, filepath.FromSlash(rel))
	if err := r.git.LoadFile(rel, filepath.Join(dir, ".gitignore")); err != nil {
		return err
	}
	return r.lino.LoadFile(rel, filepath.Join(dir, ".linoignore"))
}

// Reset forgets every loaded ignore file and reloads the root's, for use after
// an ignore file changed.
func (r *Rules) Reset() error {
	fresh, err := NewRules(r.root)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.git, r.lino, r.loaded = fresh.git, fresh.lino, fresh.loaded
	r.mu.Unlock()
	return nil
}

// IgnoredSelf reports whether rel itself is ignored, without checking its
// parent directories. Walkers that skip ignored directories use this.
func (r *Rules) IgnoredSelf(rel string, isDir bool) bool {
	rel = cleanRel(rel)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ignoredSelf(rel, isDir)
}

func (r *Rules) ignoredSelf(rel string, isDir bool) bool {
	if rel == "" {
		return false
	}
	if rel == LinoDir || rel == GitDir || strings.HasPrefix(rel, LinoDir+"/") || strings.HasPrefix(rel, GitDir+"/") {
		return true
	}
	if ign, ok := r.lino.decide(rel, isDir); ok {
		return ign
	}
	return r.git.MatchSelf(rel, isDir)
}

// Ignored reports whether rel is ignored, itself or through a parent
// directory. Ignore files in rel's parent directories are loaded as needed.
func (r *Rules) Ignored(rel string, isDir bool) bool {
	rel = cleanRel(rel)
	if rel == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	parts := strings.Split(rel, "/")
	for i := range parts {
		dir := strings.Join(parts[:i], "/")
		if i > 0 && r.ignoredSelf(dir, true) {
			return true
		}
		_ = r.loadDir(dir)
	}
	return r.ignoredSelf(rel, isDir)
}

// decide returns the verdict of the last matching pattern, and whether any matched.
func (m *Matcher) decide(rel string, isDir bool) (ignored, matched bool) {
	for i := len(m.patterns) - 1; i >= 0; i-- {
		if m.patterns[i].match(rel, isDir) {
			return !m.patterns[i].Negate, true
		}
	}
	return false, false
}

func cleanRel(rel string) string {
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	if rel == "." {
		return ""
	}
	return path.Clean("/" + rel)[1:]
}

// Entry is a regular file found by Walk.
type Entry struct {
	Rel     string // root-relative, slash-separated
	Abs     string // absolute path to read; the link target for in-root symlinks
	Size    int64
	Mode    fs.FileMode
	ModTime time.Time
	Link    bool // Rel is a symlink to a file inside the root
}

// Root returns the canonical root the rules apply to.
func (r *Rules) Root() string { return r.root }

// Walk calls fn for every non-ignored regular file under root, in lexical
// order. Symlinks to files inside the root are yielded with Link set;
// symlinked directories, links escaping the root and dangling links are
// skipped. If rules is nil, Walk loads them; otherwise root is ignored in
// favour of rules.Root(). Unreadable directories are
// skipped. Returning fs.SkipAll from fn stops the walk without error.
func Walk(root string, rules *Rules, fn func(Entry) error) error {
	if rules == nil {
		var err error
		if rules, err = NewRules(root); err != nil {
			return err
		}
	}
	root = rules.root
	err := walkDir(root, rules, "", fn)
	if err == fs.SkipAll {
		return nil
	}
	return err
}

func walkDir(root string, rules *Rules, rel string, fn func(Entry) error) error {
	if err := rules.LoadDir(rel); err != nil {
		return err
	}
	dir := filepath.Join(root, filepath.FromSlash(rel))
	ents, err := os.ReadDir(dir)
	if err != nil {
		if rel == "" {
			return err
		}
		return nil
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	for _, de := range ents {
		child := de.Name()
		if rel != "" {
			child = rel + "/" + child
		}
		abs := filepath.Join(dir, de.Name())
		switch t := de.Type(); {
		case t.IsDir():
			if rules.IgnoredSelf(child, true) {
				continue
			}
			if err := walkDir(root, rules, child, fn); err != nil {
				return err
			}
		case t&fs.ModeSymlink != 0:
			target, err := filepath.EvalSymlinks(abs)
			if err != nil || !inside(root, target) {
				continue
			}
			if trel, _ := filepath.Rel(root, target); rules.Ignored(trel, false) {
				continue
			}
			fi, err := os.Stat(target)
			if err != nil || !fi.Mode().IsRegular() || rules.IgnoredSelf(child, false) {
				continue
			}
			if err := fn(Entry{Rel: child, Abs: target, Size: fi.Size(), Mode: fi.Mode(), ModTime: fi.ModTime(), Link: true}); err != nil {
				return err
			}
		case t.IsRegular():
			if rules.IgnoredSelf(child, false) {
				continue
			}
			fi, err := de.Info()
			if err != nil {
				continue
			}
			if err := fn(Entry{Rel: child, Abs: abs, Size: fi.Size(), Mode: fi.Mode(), ModTime: fi.ModTime()}); err != nil {
				return err
			}
		}
	}
	return nil
}

func inside(root, p string) bool {
	if p == root {
		return true
	}
	return strings.HasPrefix(p, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}

func canonical(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}
