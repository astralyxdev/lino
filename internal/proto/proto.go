package proto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
)

// Version is the socket protocol version carried in every message.
const Version = 1

// MaxMessage bounds one encoded message (stdin content is base64 inside it).
const MaxMessage = 256 << 20

// Request is one CLI invocation forwarded to the live process.
type Request struct {
	Version int                 `json:"v"`
	Command string              `json:"command"`
	Args    []string            `json:"args,omitempty"`
	Flags   map[string][]string `json:"flags,omitempty"`
	Stdin   []byte              `json:"stdin,omitempty"`
	Cwd     string              `json:"cwd,omitempty"`
	By      string              `json:"by,omitempty"`
	JSON    bool                `json:"json,omitempty"`
}

// Flag returns the last value of flag name, or "".
func (r *Request) Flag(name string) string {
	v := r.Flags[name]
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

// Response is the reply to one Request: the --json envelope plus what the
// client must print and exit with.
type Response struct {
	Version int             `json:"v"`
	OK      bool            `json:"ok"`
	Outcome outcome.Outcome `json:"outcome"`
	Data    json.RawMessage `json:"data,omitempty"`
	Message string          `json:"message,omitempty"`
	Hint    string          `json:"hint,omitempty"`
	Exit    int             `json:"exit"`
	Stdout  string          `json:"stdout,omitempty"`
	Stderr  string          `json:"stderr,omitempty"`
}

// Envelope returns the --json envelope carried by r.
func (r *Response) Envelope() output.Envelope {
	env := output.Envelope{Version: output.Version, OK: r.OK, Outcome: r.Outcome, Message: r.Message, Hint: r.Hint}
	if len(r.Data) > 0 {
		env.Data = r.Data
	}
	return env
}

// NewResponse renders res both ways: the envelope fields for --json clients and
// the text stdout/stderr for plain clients, using output.Printer.
func NewResponse(res output.Result, jsonMode bool) (*Response, error) {
	o := res.Outcome
	if o == "" {
		o = outcome.OK
	}
	resp := &Response{Version: Version, OK: o.Success(), Outcome: o, Message: res.Message, Hint: res.Hint}
	if res.Data != nil {
		b, err := json.Marshal(res.Data)
		if err != nil {
			return nil, err
		}
		resp.Data = b
	}
	var stdout, stderr bytes.Buffer
	p := output.Printer{Stdout: &stdout, Stderr: &stderr, JSON: jsonMode}
	resp.Exit = p.Result(res)
	resp.Stdout, resp.Stderr = stdout.String(), stderr.String()
	return resp, nil
}

// ErrorResponse builds a Response for err.
func ErrorResponse(err error, jsonMode bool) *Response {
	resp, merr := NewResponse(output.FromError(err), jsonMode)
	if merr != nil {
		resp, _ = NewResponse(output.Result{Outcome: outcome.Internal, Message: merr.Error()}, jsonMode)
	}
	return resp
}

// Print writes r's stdout and stderr text and returns its exit code.
func (r *Response) Print(stdout, stderr io.Writer) int {
	io.WriteString(stdout, r.Stdout)
	io.WriteString(stderr, r.Stderr)
	return r.Exit
}

// VersionError reports that client and process speak different protocol versions.
func VersionError(got int) *outcome.Error {
	return outcome.New(outcome.Internal,
		"protocol version mismatch: this lino speaks v%d, the process speaks v%d; restart the process (lino stop, then retry)",
		Version, got)
}

// Encode writes m as one JSON line.
func Encode(w io.Writer, m any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return err
	}
	if buf.Len() > MaxMessage {
		return outcome.New(outcome.Refused, "message too large: %d bytes", buf.Len())
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// NewRequest returns a Request for command with the current protocol version.
func NewRequest(command string, args ...string) *Request {
	return &Request{Version: Version, Command: command, Args: args}
}

// WriteRequest encodes req, stamping the protocol version.
func WriteRequest(w io.Writer, req *Request) error {
	req.Version = Version
	return Encode(w, req)
}

// WriteResponse encodes resp, stamping the protocol version.
func WriteResponse(w io.Writer, resp *Response) error {
	resp.Version = Version
	return Encode(w, resp)
}

// Reader reads JSON-line messages from a connection.
type Reader struct {
	br *bufio.Reader
}

// NewReader wraps r.
func NewReader(r io.Reader) *Reader {
	return &Reader{br: bufio.NewReaderSize(r, 64<<10)}
}

// ReadRequest reads the next request. It returns io.EOF at a clean end of
// stream and a VersionError when the peer's version differs.
func (r *Reader) ReadRequest() (*Request, error) {
	var req Request
	if err := r.read(&req); err != nil {
		return nil, err
	}
	return &req, nil
}

// ReadResponse reads the next response, like ReadRequest.
func (r *Reader) ReadResponse() (*Response, error) {
	var resp Response
	if err := r.read(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (r *Reader) read(m any) error {
	line, err := r.line()
	if err != nil {
		return err
	}
	var head struct {
		Version *int `json:"v"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return outcome.Wrap(outcome.Internal, err, "protocol: malformed message: "+err.Error())
	}
	if head.Version == nil {
		return VersionError(0)
	}
	if *head.Version != Version {
		return VersionError(*head.Version)
	}
	if err := json.Unmarshal(line, m); err != nil {
		return outcome.Wrap(outcome.Internal, err, "protocol: malformed message: "+err.Error())
	}
	return nil
}

func (r *Reader) line() ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.br.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > MaxMessage {
			return nil, outcome.New(outcome.Refused, "protocol: message exceeds %d bytes", MaxMessage)
		}
		switch {
		case err == nil:
			return buf, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(buf) > 0:
			return nil, fmt.Errorf("protocol: %w", io.ErrUnexpectedEOF)
		default:
			return nil, err
		}
	}
}
