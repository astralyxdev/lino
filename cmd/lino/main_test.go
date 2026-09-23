package main

import (
	"bytes"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/cli"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		code    int
		stdout  string
		handoff []string // argv handed to lino-core; nil: none
	}{
		{"version flag", []string{"--version"}, 0, "lino dev (none)\n", nil},
		{"no args", nil, 1, "", []string{}},
		{"unknown command", []string{"frob"}, 1, "", []string{"frob"}},
		{"help", []string{"help", "read"}, 1, "", []string{"help", "read"}},
		{"init is local", []string{"init"}, 1, "", []string{"init"}},
		{"direct", []string{"read", "x", "--direct"}, 1, "", []string{"read", "x", "--direct"}},
		{"usage error", []string{"read"}, 1, "", []string{"read"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			var got []string
			handoff := func(argv []string) error {
				got = append([]string{}, argv...)
				return errors.New("no core")
			}
			env := cli.Env{Cwd: t.TempDir(), Getenv: func(string) string { return "" }}
			if code := run(tt.args, env, &out, &errb, handoff); code != tt.code {
				t.Fatalf("code = %d, want %d (stderr %q)", code, tt.code, errb.String())
			}
			if out.String() != tt.stdout {
				t.Errorf("stdout = %q, want %q", out.String(), tt.stdout)
			}
			if !reflect.DeepEqual(got, tt.handoff) {
				t.Errorf("handoff = %q, want %q", got, tt.handoff)
			}
		})
	}
}

func TestNoSQLite(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skip("go list:", err)
	}
	for _, p := range strings.Fields(string(out)) {
		if strings.HasPrefix(p, "modernc.org/") || strings.Contains(p, "sqlite") ||
			p == "github.com/astralyx/lino/internal/index" {
			t.Errorf("thin client links %s", p)
		}
	}
}
