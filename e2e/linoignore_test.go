package e2e

import (
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/ignore"
)

func TestRunCreatesDefaultLinoignore(t *testing.T) {
	cases := []struct {
		name     string
		existing *string // nil = no .linoignore before run
	}{
		{"missing", nil},
		{"empty", ptr("")},
		{"custom", ptr("# mine\n*.log\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := New(t)
			h.Home = shortHome(t)
			h.Write("a.txt", "needle here\n").Write("node_modules/pkg/index.js", "needle in dep\n")
			if tc.existing != nil {
				h.Write(".linoignore", *tc.existing)
			}
			h.ExpectExit(h.Run("init"), 0)
			stopOnCleanup(t, h)

			r := h.Run("run")
			h.ExpectExit(r, 0)
			note := strings.Contains(r.Stderr, "created .linoignore with defaults")
			if note != (tc.existing == nil) {
				t.Errorf("note on stderr = %v:\n%s", note, h.Transcript(r))
			}
			want := string(ignore.DefaultLinoignoreContent())
			if tc.existing != nil {
				want = *tc.existing
			}
			if got := h.Read(".linoignore"); got != want {
				t.Fatalf(".linoignore = %q, want %q", got, want)
			}

			// A second run of the live process and an auto-start after stop
			// leave the file alone.
			r = h.Run("run")
			h.ExpectExit(r, 0)
			h.ExpectExit(h.Run("stop"), 0)
			r = h.Run("search", "needle")
			h.ExpectExit(r, 0)
			if strings.Contains(r.Stderr, "created .linoignore") {
				t.Errorf("auto-start recreated .linoignore:\n%s", h.Transcript(r))
			}
			if got := h.Read(".linoignore"); got != want {
				t.Fatalf(".linoignore rewritten: %q", got)
			}
			dep := strings.Contains(r.Stdout, "node_modules/")
			if dep != (tc.name == "empty" || tc.name == "custom") {
				t.Errorf("node_modules indexed = %v:\n%s", dep, h.Transcript(r))
			}
			if !strings.Contains(r.Stdout, "a.txt") {
				t.Errorf("a.txt not found:\n%s", h.Transcript(r))
			}
		})
	}
}

func ptr(s string) *string { return &s }
