package outcome

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestExitCodes(t *testing.T) {
	tests := []struct {
		o       Outcome
		code    int
		success bool
	}{
		{OK, 0, true},
		{Updated, 0, true},
		{Created, 0, true},
		{Empty, 0, true},
		{Truncated, 0, true},
		{Usage, 2, false},
		{NotFound, 3, false},
		{AnchorMismatch, 4, false},
		{Ambiguous, 5, false},
		{Conflict, 6, false},
		{Refused, 7, false},
		{NotRunning, 8, false},
		{LiveExists, 9, false},
		{Internal, 1, false},
		{Outcome("bogus"), 1, false},
	}
	seen := map[Outcome]bool{}
	for _, tt := range tests {
		t.Run(string(tt.o), func(t *testing.T) {
			if got := tt.o.ExitCode(); got != tt.code {
				t.Errorf("ExitCode = %d, want %d", got, tt.code)
			}
			if got := tt.o.Success(); got != tt.success {
				t.Errorf("Success = %v, want %v", got, tt.success)
			}
			err := New(tt.o, "msg")
			if got := ExitCode(fmt.Errorf("wrapped: %w", err)); !tt.success && got != tt.code {
				t.Errorf("ExitCode(err) = %d, want %d", got, tt.code)
			}
		})
		seen[tt.o] = true
	}
	for _, o := range All {
		if !seen[o] {
			t.Errorf("outcome %q not covered", o)
		}
		if !o.Valid() {
			t.Errorf("outcome %q not valid", o)
		}
	}
}

func TestOf(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Outcome
	}{
		{"nil", nil, OK},
		{"plain", io.EOF, Internal},
		{"direct", New(Conflict, "x"), Conflict},
		{"wrapped", fmt.Errorf("a: %w", New(Refused, "x")), Refused},
		{"wrap", Wrap(NotFound, io.EOF, ""), NotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Of(tt.err); got != tt.want {
				t.Errorf("Of = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestErrorPayload(t *testing.T) {
	e := Wrap(AnchorMismatch, io.EOF, "anchor 13:9c1 stale").
		WithLines("a.go", []Line{{N: 13, Anchor: "9c1", Text: "x"}}).
		WithCandidates("12", "40").
		WithHint("--lines 10:20")
	if !errors.Is(e, io.EOF) {
		t.Error("Unwrap lost cause")
	}
	if !Is(e, AnchorMismatch) || Is(nil, OK) {
		t.Error("Is mismatch")
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"outcome":"anchor_mismatch","message":"anchor 13:9c1 stale","path":"a.go","lines":[{"n":13,"anchor":"9c1","text":"x"}],"candidates":["12","40"],"hint":"--lines 10:20"}`
	if string(b) != want {
		t.Errorf("json = %s\nwant %s", b, want)
	}
	if (&Error{Outcome: Usage}).Error() != "usage" {
		t.Error("empty message should fall back to outcome")
	}
	if Wrap(Internal, io.EOF, "").Error() != "EOF" {
		t.Error("Wrap message")
	}
}
