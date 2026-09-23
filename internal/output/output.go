// Package output renders results as compact text or the --json envelope.
// Data goes to stdout; messages and hints go to stderr (text mode only).
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/astralyx/lino/internal/outcome"
)

// Version is the --json envelope version.
const Version = 1

// Texter is implemented by result data that knows its compact text form.
type Texter interface {
	WriteText(w io.Writer) error
}

// Result is what a command produced.
type Result struct {
	Outcome outcome.Outcome // empty means ok
	Data    any             // JSON data; its text form via Texter, string, []string or fmt
	Message string          // human note, stderr in text mode
	Hint    string          // continuation hint, e.g. "--lines 501:1000"
}

// Envelope is the --json wire form.
type Envelope struct {
	Version int             `json:"version"`
	OK      bool            `json:"ok"`
	Outcome outcome.Outcome `json:"outcome"`
	Data    any             `json:"data,omitempty"`
	Message string          `json:"message,omitempty"`
	Hint    string          `json:"hint,omitempty"`
}

// ErrorData is the data payload of a failed command.
type ErrorData struct {
	Path       string         `json:"path,omitempty"`
	Lines      []outcome.Line `json:"lines,omitempty"`
	Candidates []string       `json:"candidates,omitempty"`
}

// WriteText prints current lines (with a path header) and candidates.
func (d ErrorData) WriteText(w io.Writer) error {
	if d.Path != "" && len(d.Lines) > 0 {
		if _, err := fmt.Fprintln(w, d.Path); err != nil {
			return err
		}
	}
	for _, l := range d.Lines {
		if _, err := fmt.Fprintln(w, FormatLine(l)); err != nil {
			return err
		}
	}
	for _, c := range d.Candidates {
		if _, err := fmt.Fprintln(w, c); err != nil {
			return err
		}
	}
	return nil
}

// FormatLine renders "13:9c1| text", or "13| text" without an anchor.
func FormatLine(l outcome.Line) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(l.N))
	if l.Anchor != "" {
		b.WriteByte(':')
		b.WriteString(l.Anchor)
	}
	b.WriteString("| ")
	b.WriteString(l.Text)
	return b.String()
}

// Printer writes results in text or JSON form.
type Printer struct {
	Stdout io.Writer
	Stderr io.Writer
	JSON   bool
}

// Result prints r and returns the process exit code.
func (p *Printer) Result(r Result) int {
	o := r.Outcome
	if o == "" {
		o = outcome.OK
	}
	if p.JSON {
		p.writeJSON(Envelope{Version: Version, OK: o.Success(), Outcome: o, Data: r.Data, Message: r.Message, Hint: r.Hint})
		return o.ExitCode()
	}
	if err := writeText(p.Stdout, r.Data); err != nil {
		fmt.Fprintf(p.Stderr, "internal: %v\n", err)
		return outcome.Internal.ExitCode()
	}
	switch {
	case !o.Success() && r.Message != "":
		fmt.Fprintf(p.Stderr, "%s: %s\n", o, r.Message)
	case !o.Success():
		fmt.Fprintln(p.Stderr, o)
	case r.Message != "":
		fmt.Fprintln(p.Stderr, r.Message)
	}
	if r.Hint != "" {
		fmt.Fprintf(p.Stderr, "hint: %s\n", r.Hint)
	}
	return o.ExitCode()
}

// Error prints err and returns the exit code. nil prints nothing and returns 0.
func (p *Printer) Error(err error) int {
	if err == nil {
		return 0
	}
	return p.Result(FromError(err))
}

// FromError converts err into a Result; non-outcome errors become internal.
func FromError(err error) Result {
	e, ok := outcome.As(err)
	if !ok {
		return Result{Outcome: outcome.Internal, Message: err.Error()}
	}
	r := Result{Outcome: e.Outcome, Message: e.Error(), Hint: e.Hint}
	if e.Path != "" || len(e.Lines) > 0 || len(e.Candidates) > 0 {
		r.Data = ErrorData{Path: e.Path, Lines: e.Lines, Candidates: e.Candidates}
	}
	return r
}

func (p *Printer) writeJSON(env Envelope) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(env); err != nil {
		buf.Reset()
		enc.Encode(Envelope{Version: Version, Outcome: outcome.Internal, Message: err.Error()})
	}
	p.Stdout.Write(buf.Bytes())
}

func writeText(w io.Writer, data any) error {
	switch d := data.(type) {
	case nil:
		return nil
	case Texter:
		return d.WriteText(w)
	case string:
		if d == "" {
			return nil
		}
		if !strings.HasSuffix(d, "\n") {
			d += "\n"
		}
		_, err := io.WriteString(w, d)
		return err
	case []string:
		for _, s := range d {
			if _, err := fmt.Fprintln(w, s); err != nil {
				return err
			}
		}
		return nil
	default:
		_, err := fmt.Fprintln(w, d)
		return err
	}
}
