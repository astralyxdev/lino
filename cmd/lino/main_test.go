package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/cli"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		code     int
		stdout   string
		stderrIn string
	}{
		{"version flag", []string{"--version"}, 0, "lino dev (none)\n", ""},
		{"no args", nil, 2, "", "usage"},
		{"unknown command", []string{"frob"}, 2, "", `unknown command "frob"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if got := run(tt.args, cli.Env{}, &out, &errb); got != tt.code {
				t.Fatalf("code = %d, want %d", got, tt.code)
			}
			if out.String() != tt.stdout {
				t.Errorf("stdout = %q, want %q", out.String(), tt.stdout)
			}
			if !strings.Contains(errb.String(), tt.stderrIn) {
				t.Errorf("stderr = %q, want to contain %q", errb.String(), tt.stderrIn)
			}
		})
	}
}
