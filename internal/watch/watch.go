// Package watch watches the root for external changes. It watches every
// non-ignored directory recursively (never .lino/), follows directories as
// they appear and disappear, and emits raw root-relative path events. It does
// not debounce or reconcile; the consumer does that by hash.
package watch

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"

	"github.com/astralyx/lino/internal/ignore"
)

// Op describes what happened to a path. Several bits may be set.
type Op uint8

const (
	Create Op = 1 << iota
	Write
	Remove
	Rename // the path was moved away; the new name arrives as Create
	Chmod
)

func (o Op) String() string {
	var parts []string
	for _, x := range []struct {
		op   Op
		name string
	}{{Create, "create"}, {Write, "write"}, {Remove, "remove"}, {Rename, "rename"}, {Chmod, "chmod"}} {
		if o&x.op != 0 {
			parts = append(parts, x.name)
		}
	}
	return strings.Join(parts, "|")
}

// Event is one change under the root. When Rescan is set, events may have
// been lost (kernel overflow, ignore rules changed) and the consumer should
// run a full reconcile; Rel and Op are then empty.
type Event struct {
	Rel    string // slash-separated, relative to the root
	Op     Op
	Dir    bool
	Rescan bool
	Reason string // why a rescan is needed
}

// Stats describe the watcher's state for `lino status`.
type Stats struct {
	Backend   string `json:"backend"`   // inotify, kqueue, ...
	Dirs      int    `json:"dirs"`      // directories watched
	Failed    int    `json:"failed"`    // directories that could not be watched
	Overflows int    `json:"overflows"` // kernel queue overflows seen
	Limit     int64  `json:"limit"`     // OS watch limit (inotify watches or open files); 0 = unknown
	LastError string `json:"last_error,omitempty"`
}

// Watcher watches one root.
type Watcher struct {
	root   string
	rules  *ignore.Rules
	fs     *fsnotify.Watcher
	events chan Event
	done   chan struct{}
	wg     sync.WaitGroup

	mu    sync.Mutex
	dirs  map[string]bool // watched dirs, root-relative ("" = root)
	stats Stats
	once  sync.Once
}

// Buffer is the capacity of the Events channel.
var Buffer = 4096

// New starts watching rules' root. Directories that cannot be watched (for
// example past the OS limit) are counted in Stats, not returned as errors.
func New(rules *ignore.Rules) (*Watcher, error) {
	fw, err := fsnotify.NewBufferedWatcher(uint(Buffer))
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		root:   rules.Root(),
		rules:  rules,
		fs:     fw,
		events: make(chan Event, Buffer),
		done:   make(chan struct{}),
		dirs:   map[string]bool{},
		stats:  Stats{Backend: backend(), Limit: watchLimit()},
	}
	w.addTree("", nil)
	w.wg.Add(1)
	go w.loop()
	return w, nil
}

// Events returns the event channel. It is closed by Close.
func (w *Watcher) Events() <-chan Event { return w.events }

// Stats returns a snapshot of the watcher state.
func (w *Watcher) Stats() Stats {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.stats
	s.Dirs = len(w.dirs)
	return s
}

// Close stops watching and closes the Events channel.
func (w *Watcher) Close() error {
	var err error
	w.once.Do(func() {
		close(w.done)
		err = w.fs.Close()
		w.wg.Wait()
		close(w.events)
	})
	return err
}

func (w *Watcher) loop() {
	defer w.wg.Done()
	for {
		select {
		case <-w.done:
			return
		case ev, ok := <-w.fs.Events:
			if !ok {
				return
			}
			w.handle(ev)
		case err, ok := <-w.fs.Errors:
			if !ok {
				return
			}
			w.handleErr(err)
		}
	}
}

func (w *Watcher) handleErr(err error) {
	w.mu.Lock()
	w.stats.LastError = err.Error()
	overflow := errors.Is(err, fsnotify.ErrEventOverflow)
	if overflow {
		w.stats.Overflows++
	}
	w.mu.Unlock()
	if overflow {
		w.emit(Event{Rescan: true, Reason: "event queue overflow"})
	}
}

func (w *Watcher) handle(ev fsnotify.Event) {
	rel, ok := w.rel(ev.Name)
	if !ok || rel == "" {
		return
	}
	op := convert(ev.Op)
	if op == 0 {
		return
	}

	w.mu.Lock()
	wasDir := w.dirs[rel]
	w.mu.Unlock()

	isDir := wasDir
	if op&(Create|Write|Chmod) != 0 {
		if st, err := os.Lstat(ev.Name); err == nil {
			isDir = st.IsDir()
		} else if op&(Remove|Rename) == 0 {
			return // already gone; its Remove event follows
		}
	}
	if w.rules.Ignored(rel, isDir) {
		return
	}

	if op&(Remove|Rename) != 0 && wasDir {
		w.dropTree(rel)
	}

	if isIgnoreFile(rel) {
		w.emit(Event{Rel: rel, Op: op})
		w.rulesChanged()
		return
	}

	if op&Create != 0 && isDir && !wasDir {
		// Files may have been created before the watch was in place: report
		// everything already inside the new directory.
		var found []Event
		w.addTree(rel, &found)
		w.emit(Event{Rel: rel, Op: op, Dir: true})
		for _, e := range found {
			w.emit(e)
		}
		return
	}
	w.emit(Event{Rel: rel, Op: op, Dir: isDir})
}

// rulesChanged reloads ignore rules, re-syncs the watched directories and
// asks for a full reconcile, since the set of indexed files may have changed.
func (w *Watcher) rulesChanged() {
	if err := w.rules.Reset(); err != nil {
		w.mu.Lock()
		w.stats.LastError = err.Error()
		w.mu.Unlock()
	}
	w.mu.Lock()
	var stale []string
	for d := range w.dirs {
		if d != "" && w.rules.Ignored(d, true) {
			stale = append(stale, d)
		}
	}
	w.mu.Unlock()
	for _, d := range stale {
		w.dropTree(d)
	}
	w.addTree("", nil)
	w.emit(Event{Rescan: true, Reason: "ignore rules changed"})
}

// addTree watches rel and every non-ignored directory below it that is not
// watched yet. With found non-nil, it collects Create events for the entries
// seen (used when a directory appears).
func (w *Watcher) addTree(rel string, found *[]Event) {
	start := filepath.Join(w.root, filepath.FromSlash(rel))
	filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		r, ok := w.rel(p)
		if !ok {
			return nil
		}
		if r != "" && w.rules.Ignored(r, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			if found != nil {
				*found = append(*found, Event{Rel: r, Op: Create})
			}
			return nil
		}
		w.mu.Lock()
		watched := w.dirs[r]
		w.mu.Unlock()
		if watched {
			return nil
		}
		if err := w.fs.Add(p); err != nil {
			w.mu.Lock()
			w.stats.Failed++
			w.stats.LastError = err.Error()
			w.mu.Unlock()
			return filepath.SkipDir
		}
		w.mu.Lock()
		w.dirs[r] = true
		w.mu.Unlock()
		if found != nil && r != rel {
			*found = append(*found, Event{Rel: r, Op: Create, Dir: true})
		}
		return nil
	})
}

// dropTree forgets rel and every watched directory below it.
func (w *Watcher) dropTree(rel string) {
	w.mu.Lock()
	var gone []string
	for d := range w.dirs {
		if d == rel || strings.HasPrefix(d, rel+"/") {
			gone = append(gone, d)
			delete(w.dirs, d)
		}
	}
	w.mu.Unlock()
	for _, d := range gone {
		_ = w.fs.Remove(filepath.Join(w.root, filepath.FromSlash(d))) // may already be gone
	}
}

func (w *Watcher) emit(e Event) {
	select {
	case w.events <- e:
	case <-w.done:
	}
}

// rel maps an absolute path to a root-relative slash path; false outside the
// root or inside .lino/.
func (w *Watcher) rel(p string) (string, bool) {
	r, err := filepath.Rel(w.root, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	r = filepath.ToSlash(r)
	if r == "." {
		r = ""
	}
	if r == ignore.LinoDir || strings.HasPrefix(r, ignore.LinoDir+"/") {
		return "", false
	}
	return r, true
}

func isIgnoreFile(rel string) bool {
	b := path.Base(rel)
	return b == ".gitignore" || b == ".linoignore"
}

func convert(o fsnotify.Op) Op {
	var r Op
	if o.Has(fsnotify.Create) {
		r |= Create
	}
	if o.Has(fsnotify.Write) {
		r |= Write
	}
	if o.Has(fsnotify.Remove) {
		r |= Remove
	}
	if o.Has(fsnotify.Rename) {
		r |= Rename
	}
	if o.Has(fsnotify.Chmod) {
		r |= Chmod
	}
	return r
}
