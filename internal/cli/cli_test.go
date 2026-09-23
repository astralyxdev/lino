package cli

import (
	"bytes"
	"context"
	"flag"
	"reflect"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
)

type seen struct {
	call  *Call
	lines string
	anch  bool
	paths StringList
	stdin string
}

func testRegistry(s *seen) *Registry {
	r := NewRegistry()
	r.Register(&Command{
		Name: "read", Usage: "<file> [--lines A:B] [--anchors]", Accept: File, MinArgs: 1, MaxArgs: 1,
		Setup: func(fs *flag.FlagSet) RunFunc {
			lines := fs.String("lines", "", "")
			anch := fs.Bool("anchors", false, "")
			return func(ctx context.Context, c *Call) (output.Result, error) {
				s.call, s.lines, s.anch = c, *lines, *anch
				return output.Result{Data: "read " + c.Arg(0)}, nil
			}
		},
	})
	r.Register(&Command{
		Name: "edit", Usage: "<file> <start> <end> --v V", Accept: Mutating, MinArgs: 3, MaxArgs: 3,
		Setup: func(fs *flag.FlagSet) RunFunc {
			fs.String("v", "", "")
			return func(ctx context.Context, c *Call) (output.Result, error) {
				s.call = c
				b, err := c.ReadStdin()
				if err != nil {
					return output.Result{}, err
				}
				s.stdin = string(b)
				return output.Result{Outcome: outcome.Updated, Data: "updated"}, nil
			}
		},
	})
	r.Register(&Command{
		Name: "search", Accept: AcceptID, MinArgs: 1, MaxArgs: 1,
		Setup: func(fs *flag.FlagSet) RunFunc {
			fs.Var(&s.paths, "path", "")
			return func(ctx context.Context, c *Call) (output.Result, error) {
				s.call = c
				return output.Result{}, outcome.New(outcome.NotFound, "nothing")
			}
		},
	})
	r.Register(&Command{
		Name: "ps", MaxArgs: 0,
		Setup: func(fs *flag.FlagSet) RunFunc {
			return func(ctx context.Context, c *Call) (output.Result, error) {
				s.call = c
				return output.Result{Outcome: outcome.Empty}, nil
			}
		},
	})
	return r
}

func TestMain(t *testing.T) {
	tests := []struct {
		name     string
		argv     []string
		env      map[string]string
		stdin    string
		code     int
		stdoutIn string
		stderrIn string
		check    func(t *testing.T, s *seen)
	}{
		{name: "no args", argv: nil, code: 2, stderrIn: "usage: missing command"},
		{name: "unknown command", argv: []string{"frob"}, code: 2, stderrIn: `unknown command "frob"`},
		{name: "unknown flag", argv: []string{"read", "a.go", "--nope"}, code: 2, stderrIn: "flag provided but not defined: -nope"},
		{name: "unknown flag json", argv: []string{"read", "--json", "--nope", "a"}, code: 2, stdoutIn: `"outcome":"usage"`},
		{name: "unknown command json", argv: []string{"frob", "--json"}, code: 2, stdoutIn: `"ok":false`},
		{name: "missing arg", argv: []string{"read"}, code: 2, stderrIn: "hint: lino read <file>"},
		{name: "too many args", argv: []string{"read", "a", "b"}, code: 2, stderrIn: "too many arguments"},
		{name: "global not accepted", argv: []string{"ps", "-i", "x"}, code: 2, stderrIn: "not defined: -i"},
		{name: "direct not accepted", argv: []string{"search", "q", "--direct"}, code: 2},
		{name: "help", argv: []string{"help"}, code: 0, stdoutIn: "lino read <file>"},
		{name: "command -h", argv: []string{"edit", "-h"}, code: 0, stdoutIn: "usage: lino edit <file> <start> <end> --v V"},
		{
			name: "interspersed flags", argv: []string{"read", "--json", "a.go", "--lines", "12:15", "--anchors", "-i", "k3f9qa", "--direct"},
			code: 0, stdoutIn: `"data":"read a.go"`,
			check: func(t *testing.T, s *seen) {
				c := s.call
				if c.Arg(0) != "a.go" || s.lines != "12:15" || !s.anch || c.ID != "k3f9qa" || !c.Direct || !c.JSON {
					t.Errorf("call = %+v lines=%q anch=%v", c, s.lines, s.anch)
				}
				want := map[string][]string{"lines": {"12:15"}, "anchors": {"true"}}
				if !reflect.DeepEqual(c.Flags, want) {
					t.Errorf("flags = %v, want %v", c.Flags, want)
				}
			},
		},
		{
			name: "long id", argv: []string{"read", "--id=web", "a"}, code: 0,
			check: func(t *testing.T, s *seen) {
				if s.call.ID != "web" {
					t.Errorf("id = %q", s.call.ID)
				}
			},
		},
		{
			name: "double dash", argv: []string{"read", "--", "--lines"}, code: 0, stdoutIn: "read --lines",
		},
		{
			name: "LINO_BY fallback", argv: []string{"edit", "f", "1", "2", "--v", "abc"}, env: map[string]string{"LINO_BY": "agent-2"},
			stdin: "x\n", code: 0, stdoutIn: "updated",
			check: func(t *testing.T, s *seen) {
				if s.call.By != "agent-2" || s.stdin != "x\n" {
					t.Errorf("by = %q stdin = %q", s.call.By, s.stdin)
				}
			},
		},
		{
			name: "--by wins over LINO_BY", argv: []string{"edit", "--by", "me", "f", "1", "2"}, env: map[string]string{"LINO_BY": "agent-2"},
			code: 0,
			check: func(t *testing.T, s *seen) {
				if s.call.By != "me" {
					t.Errorf("by = %q", s.call.By)
				}
			},
		},
		{
			name: "repeatable flag", argv: []string{"search", "q", "--path", "a/**", "--path", "b"}, code: 3, stderrIn: "not_found: nothing",
			check: func(t *testing.T, s *seen) {
				if !reflect.DeepEqual([]string(s.paths), []string{"a/**", "b"}) || !reflect.DeepEqual(s.call.Flags["path"], []string{"a/**", "b"}) {
					t.Errorf("paths = %v flags = %v", s.paths, s.call.Flags)
				}
			},
		},
		{name: "empty outcome", argv: []string{"ps"}, code: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &seen{}
			r := testRegistry(s)
			var out, errb bytes.Buffer
			env := Env{Stdin: strings.NewReader(tt.stdin), Getenv: func(k string) string { return tt.env[k] }}
			code := r.Main(context.Background(), tt.argv, env, &out, &errb)
			if code != tt.code {
				t.Fatalf("code = %d, want %d (stdout %q, stderr %q)", code, tt.code, out.String(), errb.String())
			}
			if !strings.Contains(out.String(), tt.stdoutIn) {
				t.Errorf("stdout = %q, want %q", out.String(), tt.stdoutIn)
			}
			if !strings.Contains(errb.String(), tt.stderrIn) {
				t.Errorf("stderr = %q, want %q", errb.String(), tt.stderrIn)
			}
			if tt.code == 2 && tt.stdoutIn == "" && out.Len() != 0 {
				t.Errorf("usage error wrote to stdout: %q", out.String())
			}
			if tt.check != nil {
				tt.check(t, s)
			}
		})
	}
}

func TestParseArgsRoundTrip(t *testing.T) {
	s := &seen{}
	r := testRegistry(s)
	c, err := r.Parse([]string{"search", "--path", "a", "--path", "b", "--", "-x-"}, Env{})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := testRegistry(&seen{}).ParseArgs("search", c.Args, c.Flags, Env{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c2.Args, []string{"-x-"}) || !reflect.DeepEqual(c2.Flags, c.Flags) {
		t.Errorf("args = %v flags = %v, want %v %v", c2.Args, c2.Flags, c.Args, c.Flags)
	}
}

func TestReadStdinLimit(t *testing.T) {
	tests := []struct {
		in   string
		max  int64
		want outcome.Outcome
	}{
		{"abc", 3, outcome.OK},
		{"abcd", 3, outcome.Refused},
		{"", 3, outcome.OK},
	}
	for _, tt := range tests {
		_, err := ReadStdin(strings.NewReader(tt.in), tt.max)
		if got := outcome.Of(err); got != tt.want {
			t.Errorf("ReadStdin(%q, %d) = %v, want %v", tt.in, tt.max, got, tt.want)
		}
	}
}
