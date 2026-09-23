package proto

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
)

func TestRequestRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		req  *Request
	}{
		{"bare", &Request{Command: "status"}},
		{"full", &Request{
			Command: "edit",
			Args:    []string{"wallet/service.go", "13:9c1", "15:e42"},
			Flags:   map[string][]string{"v": {"8c21e0"}, "path": {"a/**", "b/*"}},
			Stdin:   []byte("line <one>\r\n\x00binary\n"),
			Cwd:     "/tmp/root/wallet",
			By:      "agent-2",
			JSON:    true,
		}},
		{"multiline stdin", &Request{Command: "replace", Stdin: []byte("a\n<<<lino>>>\nb\n")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteRequest(&buf, tt.req); err != nil {
				t.Fatal(err)
			}
			if n := bytes.Count(buf.Bytes(), []byte("\n")); n != 1 {
				t.Fatalf("want one line, got %d newlines", n)
			}
			got, err := NewReader(&buf).ReadRequest()
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != Version {
				t.Errorf("version = %d", got.Version)
			}
			if !reflect.DeepEqual(got, tt.req) {
				t.Errorf("got %+v, want %+v", got, tt.req)
			}
		})
	}
}

type sample struct {
	N int `json:"n"`
}

func (s sample) WriteText(w io.Writer) error {
	_, err := io.WriteString(w, "sample\n")
	return err
}

func TestResponseRoundTrip(t *testing.T) {
	tests := []struct {
		name       string
		res        output.Result
		json       bool
		exit       int
		stdout     string
		stderrPart string
	}{
		{"ok text", output.Result{Data: sample{N: 3}, Message: "note"}, false, 0, "sample\n", "note"},
		{"ok json", output.Result{Outcome: outcome.Updated, Data: sample{N: 3}}, true, 0, `"outcome":"updated"`, ""},
		{"truncated", output.Result{Outcome: outcome.Truncated, Hint: "--lines 501:1000"}, false, 0, "", "hint: --lines 501:1000"},
		{"conflict", output.Result{Outcome: outcome.Conflict, Message: "changed"}, false, 6, "", "conflict: changed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := NewResponse(tt.res, tt.json)
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			if err := WriteResponse(&buf, resp); err != nil {
				t.Fatal(err)
			}
			got, err := NewReader(&buf).ReadResponse()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, resp) {
				t.Fatalf("got %+v, want %+v", got, resp)
			}
			if got.Exit != tt.exit {
				t.Errorf("exit = %d, want %d", got.Exit, tt.exit)
			}
			if !strings.Contains(got.Stdout, tt.stdout) {
				t.Errorf("stdout %q lacks %q", got.Stdout, tt.stdout)
			}
			if !strings.Contains(got.Stderr, tt.stderrPart) {
				t.Errorf("stderr %q lacks %q", got.Stderr, tt.stderrPart)
			}
			if tt.json && got.Stderr != "" {
				t.Errorf("json mode wrote stderr %q", got.Stderr)
			}
			var out, errOut bytes.Buffer
			if code := got.Print(&out, &errOut); code != tt.exit || out.String() != got.Stdout {
				t.Errorf("Print = %d %q", code, out.String())
			}
		})
	}
}

func TestErrorResponse(t *testing.T) {
	err := outcome.New(outcome.AnchorMismatch, "stale").WithLines("a.go", []outcome.Line{{N: 13, Anchor: "9c1", Text: "x"}})
	resp := ErrorResponse(err, false)
	if resp.Exit != 4 || resp.Outcome != outcome.AnchorMismatch || resp.OK {
		t.Fatalf("got %+v", resp)
	}
	if resp.Stdout != "a.go\n13:9c1| x\n" {
		t.Errorf("stdout = %q", resp.Stdout)
	}
	env := resp.Envelope()
	b, _ := json.Marshal(env)
	if !strings.Contains(string(b), `"lines":[{"n":13,"anchor":"9c1","text":"x"}]`) {
		t.Errorf("envelope = %s", b)
	}
}

func TestVersionMismatch(t *testing.T) {
	tests := []struct {
		name string
		line string
		got  string
	}{
		{"newer", `{"v":2,"command":"read"}`, "process speaks v2"},
		{"older", `{"v":0,"exit":0}`, "process speaks v0"},
		{"missing", `{"command":"read"}`, "process speaks v0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, read := range []func(*Reader) error{
				func(r *Reader) error { _, err := r.ReadRequest(); return err },
				func(r *Reader) error { _, err := r.ReadResponse(); return err },
			} {
				err := read(NewReader(strings.NewReader(tt.line + "\n")))
				if !outcome.Is(err, outcome.Internal) {
					t.Fatalf("err = %v, want internal", err)
				}
				msg := err.Error()
				if !strings.Contains(msg, "version mismatch") || !strings.Contains(msg, "restart") || !strings.Contains(msg, tt.got) {
					t.Errorf("message = %q", msg)
				}
			}
		})
	}
}

func TestReadErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check func(error) bool
	}{
		{"eof", "", func(err error) bool { return err == io.EOF }},
		{"partial", `{"v":1`, func(err error) bool { return errors.Is(err, io.ErrUnexpectedEOF) }},
		{"malformed", "not json\n", func(err error) bool { return outcome.Is(err, outcome.Internal) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewReader(strings.NewReader(tt.input)).ReadRequest()
			if !tt.check(err) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestLargeAndSequential(t *testing.T) {
	var buf bytes.Buffer
	big := bytes.Repeat([]byte("x"), 1<<20)
	for i, c := range []string{"write", "read"} {
		req := NewRequest(c, "f.txt")
		if i == 0 {
			req.Stdin = big
		}
		if err := WriteRequest(&buf, req); err != nil {
			t.Fatal(err)
		}
	}
	r := NewReader(&buf)
	first, err := r.ReadRequest()
	if err != nil || !bytes.Equal(first.Stdin, big) {
		t.Fatalf("first: %v", err)
	}
	second, err := r.ReadRequest()
	if err != nil || second.Command != "read" {
		t.Fatalf("second: %v %+v", err, second)
	}
	if _, err := r.ReadRequest(); err != io.EOF {
		t.Errorf("want EOF, got %v", err)
	}
}

func TestFlag(t *testing.T) {
	r := &Request{Flags: map[string][]string{"v": {"a", "b"}}}
	if r.Flag("v") != "b" || r.Flag("x") != "" {
		t.Error("Flag")
	}
}
