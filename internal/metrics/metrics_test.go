package metrics

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/outcome"
)

func TestCollectorSurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	c := Load(path)
	c.AddStart()
	c.Observe("edit", outcome.Updated, time.Millisecond)
	c.Observe("edit", outcome.Conflict, 2*time.Millisecond)
	c.Observe("edit", outcome.AnchorMismatch, time.Millisecond)
	c.Observe("edit", "", time.Millisecond)
	c.Observe("search", outcome.OK, 3*time.Millisecond)
	c.ObserveReindex(5 * time.Millisecond)
	c.AddExternal(3)
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	c2 := Load(path)
	c2.AddStart()
	c2.Observe("edit", outcome.Updated, time.Millisecond)
	s := c2.Snapshot()
	tests := []struct {
		name      string
		got, want any
	}{
		{"starts", s.Starts, uint64(2)},
		{"edit count", s.Commands["edit"].Count, uint64(5)},
		{"edit latency count", s.Commands["edit"].Latency.Count, uint64(5)},
		{"empty outcome is ok", s.Commands["edit"].Outcomes[outcome.OK], uint64(1)},
		{"total", s.Total(), uint64(6)},
		{"conflict rate of edit", s.Rate(outcome.Conflict, "edit"), 0.2},
		{"anchor rate overall", s.Rate(outcome.AnchorMismatch), 1.0 / 6},
		{"rate of unknown command", s.Rate(outcome.Conflict, "rm"), 0.0},
		{"reindex", s.Reindex.Count, uint64(1)},
		{"external", s.External, uint64(3)},
		{"names", len(s.Names()), 2},
		{"outcomes", s.Outcomes()[outcome.Updated], uint64(2)},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
		}
	}
	if !s.Since.Equal(c.Snapshot().Since) {
		t.Error("since not kept")
	}
}

func TestLoadBadFile(t *testing.T) {
	tests := map[string]string{
		"garbage":       "{not json",
		"other version": `{"version":99,"commands":{"x":{"count":5}}}`,
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			os.WriteFile(path, []byte(content), 0o644)
			if s := Load(path).Snapshot(); s.Total() != 0 || s.Version != SchemaVersion {
				t.Fatalf("stats %+v", s)
			}
		})
	}
}

func TestSnapshotIsCopy(t *testing.T) {
	c := Load(filepath.Join(t.TempDir(), FileName))
	c.Observe("read", outcome.OK, time.Millisecond)
	s := c.Snapshot()
	c.Observe("read", outcome.OK, time.Millisecond)
	if s.Commands["read"].Count != 1 || s.Commands["read"].Latency.Count != 1 {
		t.Fatal("snapshot shares state")
	}
}

func TestSaveOnlyWhenDirty(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	c := Load(path)
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("clean collector wrote a file")
	}
}
