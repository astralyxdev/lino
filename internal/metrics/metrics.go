package metrics

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/astralyx/lino/internal/outcome"
)

// FileName is the stats file inside .lino/.
const FileName = "stats.json"

// SchemaVersion of the stats file; a different version starts afresh.
const SchemaVersion = 1

// Command holds the counters of one command.
type Command struct {
	Count    uint64                     `json:"count"`
	Outcomes map[outcome.Outcome]uint64 `json:"outcomes"`
	Latency  *Histogram                 `json:"latency"`
}

// Stats is everything collected, as persisted and as returned by Snapshot.
type Stats struct {
	Version  int                 `json:"version"`
	Since    time.Time           `json:"since"`    // first collection
	Starts   uint64              `json:"starts"`   // live process starts
	Commands map[string]*Command `json:"commands"` // by command name
	Reindex  *Histogram          `json:"reindex"`  // own-edit re-index time
	External uint64              `json:"external"` // external file changes applied
}

func newStats(now time.Time) *Stats {
	return &Stats{Version: SchemaVersion, Since: now, Commands: map[string]*Command{}, Reindex: &Histogram{}}
}

// Names returns the command names in order.
func (s *Stats) Names() []string {
	names := make([]string, 0, len(s.Commands))
	for n := range s.Commands {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Total is the number of calls over all commands.
func (s *Stats) Total() uint64 {
	var n uint64
	for _, c := range s.Commands {
		n += c.Count
	}
	return n
}

// Outcomes sums outcome counts over all commands.
func (s *Stats) Outcomes() map[outcome.Outcome]uint64 {
	m := map[outcome.Outcome]uint64{}
	for _, c := range s.Commands {
		for o, n := range c.Outcomes {
			m[o] += n
		}
	}
	return m
}

// Rate is the share of calls of the given commands (all when none are given)
// that ended with o; 0 when there were none.
func (s *Stats) Rate(o outcome.Outcome, commands ...string) float64 {
	var hit, all uint64
	for name, c := range s.Commands {
		if len(commands) > 0 && !contains(commands, name) {
			continue
		}
		all += c.Count
		hit += c.Outcomes[o]
	}
	if all == 0 {
		return 0
	}
	return float64(hit) / float64(all)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func (s *Stats) clone() *Stats {
	c := *s
	c.Commands = make(map[string]*Command, len(s.Commands))
	for n, cmd := range s.Commands {
		cc := &Command{Count: cmd.Count, Outcomes: make(map[outcome.Outcome]uint64, len(cmd.Outcomes)), Latency: cmd.Latency.clone()}
		for o, k := range cmd.Outcomes {
			cc.Outcomes[o] = k
		}
		c.Commands[n] = cc
	}
	c.Reindex = s.Reindex.clone()
	return &c
}

// Collector is safe for concurrent use.
type Collector struct {
	path  string
	mu    sync.Mutex
	stats *Stats
	dirty bool
}

// Path returns <root>/.lino/stats.json.
func Path(root string) string { return filepath.Join(root, ".lino", FileName) }

// Load reads the stats file at path. A missing, unreadable or other-version
// file starts empty: stats are best effort and never block the process.
func Load(path string) *Collector {
	c := &Collector{path: path, stats: newStats(time.Now())}
	b, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var s Stats
	if json.Unmarshal(b, &s) != nil || s.Version != SchemaVersion {
		return c
	}
	if s.Commands == nil {
		s.Commands = map[string]*Command{}
	}
	for _, cmd := range s.Commands {
		if cmd.Outcomes == nil {
			cmd.Outcomes = map[outcome.Outcome]uint64{}
		}
		if cmd.Latency == nil {
			cmd.Latency = &Histogram{}
		}
	}
	if s.Reindex == nil {
		s.Reindex = &Histogram{}
	}
	c.stats = &s
	return c
}

// Observe records one command call.
func (c *Collector) Observe(command string, o outcome.Outcome, d time.Duration) {
	if o == "" {
		o = outcome.OK
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cmd := c.stats.Commands[command]
	if cmd == nil {
		cmd = &Command{Outcomes: map[outcome.Outcome]uint64{}, Latency: &Histogram{}}
		c.stats.Commands[command] = cmd
	}
	cmd.Count++
	cmd.Outcomes[o]++
	cmd.Latency.Observe(d)
	c.dirty = true
}

// ObserveReindex records the time of one own-edit re-index.
func (c *Collector) ObserveReindex(d time.Duration) {
	c.mu.Lock()
	c.stats.Reindex.Observe(d)
	c.dirty = true
	c.mu.Unlock()
}

// AddExternal counts external file changes applied to the index.
func (c *Collector) AddExternal(n int) {
	if n <= 0 {
		return
	}
	c.mu.Lock()
	c.stats.External += uint64(n)
	c.dirty = true
	c.mu.Unlock()
}

// AddStart counts a live process start.
func (c *Collector) AddStart() {
	c.mu.Lock()
	c.stats.Starts++
	c.dirty = true
	c.mu.Unlock()
}

// Snapshot returns a deep copy of the current stats.
func (c *Collector) Snapshot() *Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats.clone()
}

// Save writes the stats atomically when anything changed since the last save.
func (c *Collector) Save() error {
	c.mu.Lock()
	if !c.dirty {
		c.mu.Unlock()
		return nil
	}
	b, err := json.Marshal(c.stats)
	c.dirty = false
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err := writeAtomic(c.path, b); err != nil {
		c.mu.Lock()
		c.dirty = true
		c.mu.Unlock()
		return err
	}
	return nil
}

func writeAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(dir, ".stats-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if err == nil {
		err = f.Close()
	} else {
		f.Close()
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
