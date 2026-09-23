package output

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/astralyx/lino/internal/outcome"
)

var update = flag.Bool("update", false, "rewrite golden files")

type sampleRead struct {
	Path  string         `json:"path"`
	V     string         `json:"v"`
	Lines []outcome.Line `json:"lines"`
}

func (s sampleRead) WriteText(w io.Writer) error {
	fmt.Fprintf(w, "%s v=%s lines %d-%d\n", s.Path, s.V, s.Lines[0].N, s.Lines[len(s.Lines)-1].N)
	for _, l := range s.Lines {
		fmt.Fprintln(w, FormatLine(l))
	}
	return nil
}

var sample = sampleRead{Path: "wallet/service.go", V: "8c21e0", Lines: []outcome.Line{
	{N: 12, Anchor: "a3f", Text: "func Withdraw() error {"},
	{N: 13, Anchor: "9c1", Text: "    if amt <= 0 {"},
}}

var conflictErr = fmt.Errorf("edit: %w", outcome.New(outcome.Conflict, "wallet/service.go changed since v=8c21e0").
	WithLines("wallet/service.go", []outcome.Line{{N: 13, Anchor: "4de", Text: "    if amt < 0 {"}}).
	WithHint("re-read with --lines 13:13 --anchors"))

func TestGolden(t *testing.T) {
	tests := []struct {
		name string
		run  func(p *Printer) int
		code int
	}{
		{"result", func(p *Printer) int { return p.Result(Result{Data: sample}) }, 0},
		{"truncated", func(p *Printer) int {
			return p.Result(Result{Outcome: outcome.Truncated, Data: sample, Hint: "--lines 14:500"})
		}, 0},
		{"error", func(p *Printer) int { return p.Error(conflictErr) }, 6},
		{"internal", func(p *Printer) int { return p.Error(errors.New("disk on fire")) }, 1},
		{"ambiguous", func(p *Printer) int {
			return p.Error(outcome.New(outcome.Ambiguous, "2 matches").WithCandidates("a.go:3", "a.go:9"))
		}, 5},
		{"strings", func(p *Printer) int {
			return p.Result(Result{Outcome: outcome.Created, Data: []string{"a", "b"}, Message: "reconciled 3 files"})
		}, 0},
	}
	for _, tt := range tests {
		for _, js := range []bool{false, true} {
			name := tt.name + ".txt"
			if js {
				name = tt.name + ".json"
			}
			t.Run(name, func(t *testing.T) {
				var out, errw bytes.Buffer
				code := tt.run(&Printer{Stdout: &out, Stderr: &errw, JSON: js})
				if code != tt.code {
					t.Errorf("exit = %d, want %d", code, tt.code)
				}
				got := "--- stdout\n" + out.String() + "--- stderr\n" + errw.String()
				path := filepath.Join("testdata", name+".golden")
				if *update {
					if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if got != string(want) {
					t.Errorf("got:\n%s\nwant:\n%s", got, want)
				}
			})
		}
	}
}

func TestStreams(t *testing.T) {
	tests := []struct {
		name             string
		json             bool
		r                Result
		wantOut, wantErr string
	}{
		{"data only", false, Result{Data: "x"}, "x\n", ""},
		{"message to stderr", false, Result{Data: "x", Message: "note"}, "x\n", "note\n"},
		{"error message stderr", false, Result{Outcome: outcome.NotFound, Message: "no such file"}, "", "not_found: no such file\n"},
		{"hint stderr", false, Result{Outcome: outcome.Empty, Hint: "try --words"}, "", "hint: try --words\n"},
		{"json all stdout", true, Result{Outcome: outcome.NotFound, Message: "no such file"},
			`{"version":1,"ok":false,"outcome":"not_found","message":"no such file"}` + "\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errw bytes.Buffer
			(&Printer{Stdout: &out, Stderr: &errw, JSON: tt.json}).Result(tt.r)
			if out.String() != tt.wantOut {
				t.Errorf("stdout = %q, want %q", out.String(), tt.wantOut)
			}
			if errw.String() != tt.wantErr {
				t.Errorf("stderr = %q, want %q", errw.String(), tt.wantErr)
			}
		})
	}
}
