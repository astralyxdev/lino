package textfile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/outcome"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want Kind
	}{
		{"empty", nil, Text},
		{"ascii", []byte("hello\nworld\n"), Text},
		{"utf8 multibyte", []byte("привет 世界 🙂\n"), Text},
		{"bom", append(append([]byte{}, BOM...), "x\n"...), Text},
		{"bom only", BOM, Text},
		{"nul", []byte("a\x00b"), Binary},
		{"latin1", []byte("caf\xe9\n"), Binary},
		{"truncated utf8", []byte("ok \xe4\xb8"), Binary},
		{"lone continuation", []byte("\x80abc"), Binary},
		{"bom then invalid", append(append([]byte{}, BOM...), 0xff), Binary},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.in); got != tt.want {
				t.Fatalf("Classify = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("é", PrefixSize) // 2 bytes each: a rune straddles the prefix end
	tests := []struct {
		name    string
		content []byte
		max     int64
		want    Kind
		outcome outcome.Outcome
	}{
		{"ascii", []byte("a\nb\n"), 100, Text, outcome.OK},
		{"multibyte", []byte("日本語\n"), 100, Text, outcome.OK},
		{"bom", append(append([]byte{}, BOM...), "x"...), 100, Text, outcome.OK},
		{"nul", []byte("a\x00"), 100, Binary, outcome.OK},
		{"latin1", []byte("\xe9t\xe9"), 100, Binary, outcome.OK},
		{"truncated utf8", []byte("abc\xe2\x82"), 100, Binary, outcome.OK},
		{"oversize", bytes.Repeat([]byte("a"), 101), 100, Binary, outcome.Refused},
		{"at limit", bytes.Repeat([]byte("a"), 100), 100, Text, outcome.OK},
		{"no limit", bytes.Repeat([]byte("a"), 1000), 0, Text, outcome.OK},
		{"large text rune across prefix", []byte("x" + big), 0, Text, outcome.OK},
		{"large binary prefix", append([]byte{0}, bytes.Repeat([]byte("a"), 2*PrefixSize)...), 0, Binary, outcome.OK},
		{"large binary tail", append(bytes.Repeat([]byte("a"), 2*PrefixSize), 0), 0, Binary, outcome.OK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "_"))
			if err := os.WriteFile(p, tt.content, 0o644); err != nil {
				t.Fatal(err)
			}
			data, kind, err := ReadFile(p, tt.max)
			if got := outcome.Of(err); got != tt.outcome {
				t.Fatalf("outcome = %v (%v), want %v", got, err, tt.outcome)
			}
			if err != nil {
				return
			}
			if kind != tt.want {
				t.Fatalf("kind = %v, want %v", kind, tt.want)
			}
			if kind == Text && !bytes.Equal(data, tt.content) {
				t.Fatal("data mismatch")
			}
			if kind == Binary && data != nil {
				t.Fatal("binary data should be nil")
			}
		})
	}
}

func TestReadFileErrors(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := ReadFile(filepath.Join(dir, "missing"), 0); outcome.Of(err) != outcome.NotFound {
		t.Fatalf("missing: %v", err)
	}
	if _, _, err := ReadFile(dir, 0); outcome.Of(err) != outcome.Refused {
		t.Fatalf("dir: %v", err)
	}
}

func TestClassifyPrefix(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Kind
	}{
		{"cut 2 of 3", "ab\xe4\xb8", Text},
		{"cut 1 of 4", "ab\xf0", Text},
		{"cut 3 of 4", "ab\xf0\x9f\x99", Text},
		{"invalid inside", "\xffab", Binary},
		{"stray continuation", "ab\x80", Binary},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyPrefix([]byte(tt.in)); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
