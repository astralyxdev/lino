// Package outcome defines command outcomes and their exit codes.
package outcome

import (
	"errors"
	"fmt"
)

// Outcome is the machine-readable result of a command, as printed in --json output.
type Outcome string

const (
	OK             Outcome = "ok"
	Updated        Outcome = "updated"
	Created        Outcome = "created"
	Empty          Outcome = "empty"
	Truncated      Outcome = "truncated"
	Usage          Outcome = "usage"
	NotFound       Outcome = "not_found"
	AnchorMismatch Outcome = "anchor_mismatch"
	Ambiguous      Outcome = "ambiguous"
	Conflict       Outcome = "conflict"
	Refused        Outcome = "refused"
	NotRunning     Outcome = "not_running"
	LiveExists     Outcome = "live_exists"
	Internal       Outcome = "internal"
)

// All lists every outcome in a stable order.
var All = []Outcome{
	OK, Updated, Created, Empty, Truncated, Usage, NotFound, AnchorMismatch,
	Ambiguous, Conflict, Refused, NotRunning, LiveExists, Internal,
}

var exitCodes = map[Outcome]int{
	OK: 0, Updated: 0, Created: 0, Empty: 0, Truncated: 0,
	Usage:          2,
	NotFound:       3,
	AnchorMismatch: 4,
	Ambiguous:      5,
	Conflict:       6,
	Refused:        7,
	NotRunning:     8,
	LiveExists:     9,
	Internal:       1,
}

// ExitCode returns the process exit code for o. Unknown outcomes map to 1.
func (o Outcome) ExitCode() int {
	if c, ok := exitCodes[o]; ok {
		return c
	}
	return 1
}

// Success reports whether o is a non-error outcome (exit code 0).
func (o Outcome) Success() bool { return o.ExitCode() == 0 }

// Valid reports whether o is a known outcome.
func (o Outcome) Valid() bool {
	_, ok := exitCodes[o]
	return ok
}

func (o Outcome) String() string { return string(o) }

// Line is one line of the current file content attached to an error, e.g. the
// region of a conflict or anchor mismatch.
type Line struct {
	N      int    `json:"n"`
	Anchor string `json:"anchor,omitempty"`
	Text   string `json:"text"`
}

// Error is a failure carrying an outcome, a human message and optional payload.
type Error struct {
	Outcome    Outcome  `json:"outcome"`
	Message    string   `json:"message"`
	Path       string   `json:"path,omitempty"`
	Lines      []Line   `json:"lines,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
	Hint       string   `json:"hint,omitempty"`
	Err        error    `json:"-"`
}

func (e *Error) Error() string {
	if e.Message == "" {
		return string(e.Outcome)
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// New returns an Error with outcome o and a formatted message.
func New(o Outcome, format string, args ...any) *Error {
	return &Error{Outcome: o, Message: fmt.Sprintf(format, args...)}
}

// Wrap returns an Error with outcome o wrapping err. The message is err's text
// unless msg is non-empty.
func Wrap(o Outcome, err error, msg string) *Error {
	if msg == "" && err != nil {
		msg = err.Error()
	}
	return &Error{Outcome: o, Message: msg, Err: err}
}

// WithLines attaches current lines and returns e.
func (e *Error) WithLines(path string, lines []Line) *Error {
	e.Path, e.Lines = path, lines
	return e
}

// WithCandidates attaches candidates (e.g. ambiguous matches) and returns e.
func (e *Error) WithCandidates(c ...string) *Error {
	e.Candidates = append(e.Candidates, c...)
	return e
}

// WithHint attaches a hint for the next call and returns e.
func (e *Error) WithHint(h string) *Error {
	e.Hint = h
	return e
}

// As returns the *Error in err's chain, if any.
func As(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// Of returns the outcome for err: OK for nil, the carried outcome for an
// *Error in the chain, Internal otherwise.
func Of(err error) Outcome {
	if err == nil {
		return OK
	}
	if e, ok := As(err); ok {
		return e.Outcome
	}
	return Internal
}

// ExitCode returns the exit code for err (0 for nil).
func ExitCode(err error) int { return Of(err).ExitCode() }

// Is reports whether err carries outcome o.
func Is(err error, o Outcome) bool { return err != nil && Of(err) == o }
