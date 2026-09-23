package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/outcome"
)

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c != Default() {
		t.Fatalf("got %+v, want defaults", c)
	}
	d := Default()
	if d.ReadLines != 500 || d.SearchHits != 20 || d.SearchMaxHits != 200 || d.LineChars != 200 ||
		d.LsEntries != 200 || d.ChangesEvents != 500 || d.Retention != 14*24*time.Hour ||
		d.HistoryMaxSize != 1<<30 || d.Idle != 2*time.Hour || d.MaxFileSize <= 0 {
		t.Fatalf("unexpected defaults %+v", d)
	}
}

func TestLoadFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".lino"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".lino", "config"), []byte("read.lines = 42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.ReadLines != 42 || c.SearchHits != 20 {
		t.Fatalf("got %+v", c)
	}
}

func TestParseOverrides(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		check func(Config) bool
	}{
		{"comments and blanks", "# comment\n\n  \n", func(c Config) bool { return c == Default() }},
		{"read", "read.lines=1000", func(c Config) bool { return c.ReadLines == 1000 }},
		{"search", "search.hits = 50\nsearch.max_hits = 300", func(c Config) bool { return c.SearchHits == 50 && c.SearchMaxHits == 300 }},
		{"line chars", "search.line_chars=80", func(c Config) bool { return c.LineChars == 80 }},
		{"ls", "ls.entries=10", func(c Config) bool { return c.LsEntries == 10 }},
		{"changes", "changes.events=7", func(c Config) bool { return c.ChangesEvents == 7 }},
		{"retention days", "history.retention = 7d", func(c Config) bool { return c.Retention == 7*24*time.Hour }},
		{"retention go", "history.retention = 36h", func(c Config) bool { return c.Retention == 36*time.Hour }},
		{"history size", "history.max_size = 512MB", func(c Config) bool { return c.HistoryMaxSize == 512<<20 }},
		{"history size plain", "history.max_size = 2048", func(c Config) bool { return c.HistoryMaxSize == 2048 }},
		{"idle", "idle = 30m", func(c Config) bool { return c.Idle == 30*time.Minute }},
		{"idle never", "idle = 0", func(c Config) bool { return c.Idle == 0 }},
		{"quoted", `idle = "1h"`, func(c Config) bool { return c.Idle == time.Hour }},
		{"file size", "file.max_size = 4m", func(c Config) bool { return c.MaxFileSize == 4<<20 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse(strings.NewReader(tt.in), "config")
			if err != nil {
				t.Fatal(err)
			}
			if !tt.check(c) {
				t.Fatalf("unexpected config %+v", c)
			}
		})
	}
}

func TestParseInvalid(t *testing.T) {
	tests := []struct {
		name, in string
	}{
		{"no equals", "read.lines 500"},
		{"unknown key", "foo = 1"},
		{"not int", "read.lines = many"},
		{"zero", "ls.entries = 0"},
		{"negative", "changes.events = -1"},
		{"bad duration", "idle = soon"},
		{"negative duration", "idle = -1h"},
		{"zero retention", "history.retention = 0"},
		{"bad size", "history.max_size = 1XB"},
		{"zero size", "file.max_size = 0"},
		{"hits over max", "search.hits = 300"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.in), "config")
			var ce *Error
			if !errors.As(err, &ce) {
				t.Fatalf("got %v, want *Error", err)
			}
			if outcome.Of(err) != outcome.Usage || outcome.ExitCode(err) != 2 {
				t.Fatalf("outcome %q", outcome.Of(err))
			}
		})
	}
}
