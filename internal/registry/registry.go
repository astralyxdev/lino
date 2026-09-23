package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/astralyx/lino/internal/outcome"
)

// Entry describes one live process, stored as <dir>/<id>.json.
type Entry struct {
	ID      string    `json:"id"`
	PID     int       `json:"pid"`
	Root    string    `json:"root"`
	Socket  string    `json:"socket"`
	Started time.Time `json:"started"`
	Version string    `json:"version"`
	Name    string    `json:"name,omitempty"`
}

// State is the liveness of an entry.
type State int

const (
	Live State = iota
	DeadPID
	DeadSocket
)

func (s State) String() string {
	switch s {
	case Live:
		return "live"
	case DeadPID:
		return "dead pid"
	case DeadSocket:
		return "dead socket"
	}
	return "unknown"
}

// DialTimeout bounds the socket probe in Check.
var DialTimeout = 300 * time.Millisecond

// Registry is a directory of run entries, normally ~/.lino/run.
type Registry struct {
	Dir string
}

// Default returns the registry under the user's home directory ($HOME/.lino/run).
func Default() (*Registry, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Registry{Dir: filepath.Join(home, ".lino", "run")}, nil
}

// EntryPath returns the JSON path for id.
func (r *Registry) EntryPath(id string) string { return filepath.Join(r.Dir, id+".json") }

// SocketPath returns the socket path for id.
func (r *Registry) SocketPath(id string) string { return filepath.Join(r.Dir, id+".sock") }

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidName reports whether s is an acceptable --name alias.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// StartGrace is how long a just-started entry with a live pid holds its name
// before its socket accepts connections.
var StartGrace = 30 * time.Second

// Write stores e atomically. A name already used by another live entry, or
// equal to another entry's id, is refused. The check and the write happen under
// an exclusive lock on the registry directory, so concurrent claims of one name
// across processes let exactly one win.
func (r *Registry) Write(e Entry) error {
	if !ValidID(e.ID) {
		return outcome.New(outcome.Usage, "invalid process id %q", e.ID)
	}
	if e.Name != "" && !ValidName(e.Name) {
		return outcome.New(outcome.Usage, "invalid name %q: use letters, digits, '.', '_' or '-'", e.Name)
	}
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return err
	}
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if e.Name != "" {
		entries, err := r.List()
		if err != nil {
			return err
		}
		for _, o := range entries {
			if o.ID == e.ID {
				continue
			}
			if o.ID == e.Name {
				return outcome.New(outcome.Refused, "name %q is the id of another process", e.Name)
			}
			if o.Name == e.Name && holdsName(o) {
				return outcome.New(outcome.Refused, "name %q is already used by process %s (%s)", e.Name, o.ID, o.Root)
			}
		}
	}
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(r.EntryPath(e.ID), append(b, '\n'))
}

// Read returns the entry for id, or a not_found error.
func (r *Registry) Read(id string) (Entry, error) {
	b, err := os.ReadFile(r.EntryPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return Entry{}, outcome.New(outcome.NotFound, "no process %q", id)
	}
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	if err := json.Unmarshal(b, &e); err != nil {
		return Entry{}, fmt.Errorf("registry entry %s: %w", id, err)
	}
	return e, nil
}

// List returns all readable entries sorted by id. Unparseable files are skipped.
func (r *Registry) List() ([]Entry, error) {
	des, err := os.ReadDir(r.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, de := range des {
		id, ok := strings.CutSuffix(de.Name(), ".json")
		if !ok || !ValidID(id) {
			continue
		}
		if e, err := r.Read(id); err == nil {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Lookup finds an entry by id, then by name. Among entries sharing a name, a
// live one wins. It does not check liveness of the result; use Check.
func (r *Registry) Lookup(idOrName string) (Entry, error) {
	if ValidID(idOrName) {
		if e, err := r.Read(idOrName); err == nil || !outcome.Is(err, outcome.NotFound) {
			return e, err
		}
	}
	entries, err := r.List()
	if err != nil {
		return Entry{}, err
	}
	var found *Entry
	for i := range entries {
		if entries[i].Name != idOrName {
			continue
		}
		if Check(entries[i]) == Live {
			return entries[i], nil
		}
		if found == nil {
			found = &entries[i]
		}
	}
	if found != nil {
		return *found, nil
	}
	return Entry{}, outcome.New(outcome.NotFound, "no process with id or name %q", idOrName)
}

// Remove deletes the entry and socket for id. Missing files are not an error.
func (r *Registry) Remove(id string) error {
	var errs []error
	for _, p := range []string{r.EntryPath(id), r.SocketPath(id)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Clean removes every stale entry and returns the removed ones.
func (r *Registry) Clean() ([]Entry, error) {
	entries, err := r.List()
	if err != nil {
		return nil, err
	}
	var removed []Entry
	var errs []error
	for _, e := range entries {
		if Check(e) == Live {
			continue
		}
		if err := r.Remove(e.ID); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, e)
	}
	return removed, errors.Join(errs...)
}

// Check reports whether e's pid is alive and its socket accepts connections.
func Check(e Entry) State {
	if !PIDAlive(e.PID) {
		return DeadPID
	}
	c, err := net.DialTimeout("unix", e.Socket, DialTimeout)
	if err != nil {
		return DeadSocket
	}
	c.Close()
	return Live
}

// holdsName reports whether o still owns its name: it is live, or its pid is
// alive and it started within StartGrace (socket not listening yet).
func holdsName(o Entry) bool {
	if st := Check(o); st == Live {
		return true
	} else if st == DeadPID {
		return false
	}
	return time.Since(o.Started) < StartGrace
}

// lock takes an exclusive flock on <dir>/.lock and returns its release func.
func (r *Registry) lock() (func(), error) {
	f, err := os.OpenFile(filepath.Join(r.Dir, ".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("lock registry: %w", err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// PIDAlive reports whether a process with pid exists.
func PIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}
