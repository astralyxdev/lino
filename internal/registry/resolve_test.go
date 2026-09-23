package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/paths"
)

func TestResolve(t *testing.T) {
	base, err := paths.Canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	reg := &Registry{Dir: filepath.Join(home, ".lino", "run")}
	projA := filepath.Join(home, "projA")
	projB := filepath.Join(base, "projB")
	nested := filepath.Join(projA, "src", "deep", "er")
	outside := filepath.Join(home, "scratch", "x")
	for _, d := range []string{reg.Dir, projA + "/.lino", nested, projB + "/.lino", outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	idA, idB := IDFor(projA), IDFor(projB)
	for _, e := range []Entry{
		{ID: idA, PID: 1 << 30, Root: projA, Started: time.Now(), Name: "alpha"},
		{ID: idB, PID: 1 << 30, Root: projB, Started: time.Now(), Name: "beta"},
	} {
		if err := reg.Write(e); err != nil {
			t.Fatal(err)
		}
	}
	// projC is initialised but has no registry entry.
	projC := filepath.Join(base, "projC")
	if err := os.MkdirAll(projC+"/.lino", 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		flag, env   string
		cwd         string
		wantRoot    string
		wantSrc     Source
		wantEntry   bool
		wantOutcome outcome.Outcome
	}{
		{name: "cwd nested subdir", cwd: nested, wantRoot: projA, wantSrc: FromCwd, wantEntry: true},
		{name: "cwd at root", cwd: projA, wantRoot: projA, wantSrc: FromCwd, wantEntry: true},
		{name: "cwd root without entry", cwd: projC, wantRoot: projC, wantSrc: FromCwd},
		{name: "flag id", flag: idB, cwd: nested, wantRoot: projB, wantSrc: FromFlag, wantEntry: true},
		{name: "flag name", flag: "beta", cwd: nested, wantRoot: projB, wantSrc: FromFlag, wantEntry: true},
		{name: "env id", env: idB, cwd: nested, wantRoot: projB, wantSrc: FromEnv, wantEntry: true},
		{name: "env name", env: "beta", cwd: outside, wantRoot: projB, wantSrc: FromEnv, wantEntry: true},
		{name: "flag beats env", flag: "alpha", env: "beta", cwd: outside, wantRoot: projA, wantSrc: FromFlag, wantEntry: true},
		{name: "env beats cwd", env: "alpha", cwd: projB, wantRoot: projA, wantSrc: FromEnv, wantEntry: true},
		{name: "unknown flag id", flag: "zzzzzz", cwd: nested, wantOutcome: outcome.NotFound},
		{name: "unknown env name", env: "nope", cwd: nested, wantOutcome: outcome.NotFound},
		{name: "outside any root", cwd: outside, wantOutcome: outcome.NotRunning},
		{name: "registry home is not a root", cwd: home, wantOutcome: outcome.NotRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := reg.Resolve(tt.flag, tt.env, tt.cwd)
			if tt.wantOutcome != "" {
				if !outcome.Is(err, tt.wantOutcome) {
					t.Fatalf("err = %v, want %s", err, tt.wantOutcome)
				}
				if tt.wantOutcome == outcome.NotRunning {
					e, _ := outcome.As(err)
					if !strings.HasPrefix(e.Hint, "lino init ") {
						t.Errorf("hint = %q, want lino init command", e.Hint)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got.Root != tt.wantRoot || got.Source != tt.wantSrc || got.ID != IDFor(tt.wantRoot) {
				t.Errorf("got %+v, want root %s src %s", got, tt.wantRoot, tt.wantSrc)
			}
			if (got.Entry != nil) != tt.wantEntry {
				t.Errorf("entry = %v, want present %v", got.Entry, tt.wantEntry)
			}
		})
	}
}
