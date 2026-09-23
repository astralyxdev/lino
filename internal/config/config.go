package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/astralyx/lino/internal/outcome"
)

// FileName is the config file inside the .lino directory.
const FileName = "config"

// Config holds tunable limits. The zero value is not useful; start from Default.
type Config struct {
	ReadLines      int           // read.lines: lines per read call
	SearchHits     int           // search.hits: default hits per search
	SearchMaxHits  int           // search.max_hits: upper bound for -k
	LineChars      int           // search.line_chars: search lines cut at this many characters
	LsEntries      int           // ls.entries
	ChangesEvents  int           // changes.events
	Retention      time.Duration // history.retention, e.g. 14d
	HistoryMaxSize int64         // history.max_size in bytes, e.g. 1GB
	Idle           time.Duration // idle: 0 = never exit
	MaxFileSize    int64         // file.max_size: larger files are refused
}

// Default returns the built-in defaults from the scope.
func Default() Config {
	return Config{
		ReadLines:      500,
		SearchHits:     20,
		SearchMaxHits:  200,
		LineChars:      200,
		LsEntries:      200,
		ChangesEvents:  500,
		Retention:      14 * 24 * time.Hour,
		HistoryMaxSize: 1 << 30,
		Idle:           2 * time.Hour,
		MaxFileSize:    16 << 20,
	}
}

// Error reports an invalid config file. Parse and Load return it wrapped in an
// *outcome.Error with outcome usage (exit 2).
type Error struct {
	Path string
	Line int
	Msg  string
}

func (e *Error) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", e.Path, e.Line, e.Msg)
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Msg)
}

// Outcome returns the outcome name for this error.
func (e *Error) Outcome() string { return "usage" }

// Load reads <root>/.lino/config. A missing file yields the defaults.
func Load(root string) (Config, error) {
	path := filepath.Join(root, ".lino", FileName)
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	return Parse(f, path)
}

func usage(e *Error) error { return outcome.Wrap(outcome.Usage, e, "") }

// Parse reads key = value lines on top of the defaults. Blank lines and
// lines starting with # are ignored; values may be quoted.
func Parse(r io.Reader, name string) (Config, error) {
	c := Default()
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return Config{}, usage(&Error{name, n, fmt.Sprintf("expected key = value, got %q", line)})
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"`)
		if err := c.set(k, v); err != nil {
			return Config{}, usage(&Error{name, n, err.Error()})
		}
	}
	if err := sc.Err(); err != nil {
		return Config{}, err
	}
	if c.SearchHits > c.SearchMaxHits {
		return Config{}, usage(&Error{Path: name, Msg: "search.hits exceeds search.max_hits"})
	}
	return c, nil
}

func (c *Config) set(key, val string) error {
	var err error
	switch key {
	case "read.lines":
		c.ReadLines, err = parsePositive(val)
	case "search.hits":
		c.SearchHits, err = parsePositive(val)
	case "search.max_hits":
		c.SearchMaxHits, err = parsePositive(val)
	case "search.line_chars":
		c.LineChars, err = parsePositive(val)
	case "ls.entries":
		c.LsEntries, err = parsePositive(val)
	case "changes.events":
		c.ChangesEvents, err = parsePositive(val)
	case "history.retention":
		c.Retention, err = ParseDuration(val)
		if err == nil && c.Retention <= 0 {
			err = errors.New("must be positive")
		}
	case "history.max_size":
		c.HistoryMaxSize, err = ParseSize(val)
	case "idle":
		c.Idle, err = ParseDuration(val)
	case "file.max_size":
		c.MaxFileSize, err = ParseSize(val)
	default:
		return fmt.Errorf("unknown key %q", key)
	}
	if err != nil {
		return fmt.Errorf("%s: invalid value %q: %v", key, val, err)
	}
	return nil
}

func parsePositive(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, errors.New("not an integer")
	}
	if n <= 0 {
		return 0, errors.New("must be positive")
	}
	return n, nil
}

// ParseDuration accepts Go durations plus a "d" (day) unit, e.g. "14d".
// "0" means zero. Negative values are rejected.
func ParseDuration(s string) (time.Duration, error) {
	if s == "0" {
		return 0, nil
	}
	var d time.Duration
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, errors.New("bad duration")
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, errors.New("bad duration")
		}
	}
	if d < 0 {
		return 0, errors.New("must not be negative")
	}
	return d, nil
}

var sizeUnits = []struct {
	suffix string
	mult   int64
}{
	{"KB", 1 << 10}, {"MB", 1 << 20}, {"GB", 1 << 30}, {"TB", 1 << 40},
	{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30}, {"T", 1 << 40},
	{"B", 1},
}

// ParseSize accepts a byte count with an optional binary unit: B, K/KB, M/MB, G/GB, T/TB.
func ParseSize(s string) (int64, error) {
	u := strings.ToUpper(strings.TrimSpace(s))
	mult := int64(1)
	for _, su := range sizeUnits {
		if num, ok := strings.CutSuffix(u, su.suffix); ok {
			u, mult = strings.TrimSpace(num), su.mult
			break
		}
	}
	n, err := strconv.ParseInt(u, 10, 64)
	if err != nil {
		return 0, errors.New("bad size")
	}
	if n <= 0 {
		return 0, errors.New("must be positive")
	}
	if n > (1<<63-1)/mult {
		return 0, errors.New("too large")
	}
	return n * mult, nil
}
