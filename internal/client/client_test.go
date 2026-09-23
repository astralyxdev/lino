package client

import (
	"context"
	"flag"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/output"
)

func testRegistry() *cli.Registry {
	r := cli.NewRegistry()
	nop := func(fs *flag.FlagSet) cli.RunFunc {
		fs.String("v", "", "")
		return func(context.Context, *cli.Call) (output.Result, error) { return output.Result{}, nil }
	}
	r.Register(&cli.Command{Name: "read", Accept: cli.File, MaxArgs: -1, Setup: nop})
	r.Register(&cli.Command{Name: "edit", Accept: cli.Mutating, Stdin: true, MaxArgs: -1, Setup: nop})
	r.Register(&cli.Command{Name: "stop", Accept: cli.AcceptID, Local: true, Setup: nop})
	r.Register(&cli.Command{Name: "ps", Setup: nop})
	return r
}

func TestForwarded(t *testing.T) {
	r := testRegistry()
	tests := []struct {
		argv []string
		want bool
	}{
		{[]string{"read", "a.go"}, true},
		{[]string{"read", "a.go", "--direct"}, false},
		{[]string{"edit", "a.go", "--v", "abc"}, true},
		{[]string{"stop"}, false},
		{[]string{"ps"}, false},
	}
	for _, tt := range tests {
		c, err := r.Parse(tt.argv, cli.Env{})
		if err != nil {
			t.Fatal(err)
		}
		if got := Forwarded(c); got != tt.want {
			t.Errorf("Forwarded(%v) = %v, want %v", tt.argv, got, tt.want)
		}
	}
}

func TestRequest(t *testing.T) {
	r := testRegistry()
	tests := []struct {
		argv  []string
		stdin string
		want  string
	}{
		{[]string{"edit", "a.go", "--v", "abc", "--by", "x"}, "new\n", "new\n"},
		{[]string{"read", "a.go"}, "ignored", ""},
	}
	for _, tt := range tests {
		c, err := r.Parse(tt.argv, cli.Env{Stdin: strings.NewReader(tt.stdin), Cwd: "/w/sub"})
		if err != nil {
			t.Fatal(err)
		}
		req, err := Request(c)
		if err != nil {
			t.Fatal(err)
		}
		if string(req.Stdin) != tt.want || req.Cwd != "/w/sub" || req.Command != tt.argv[0] || req.Args[0] != "a.go" {
			t.Errorf("Request(%v) = %+v", tt.argv, req)
		}
		if tt.argv[0] == "edit" && (req.Flag("v") != "abc" || req.By != "x") {
			t.Errorf("flags/by not carried: %+v", req)
		}
	}
}
