package textfile

import (
	"reflect"
	"testing"
)

func TestParseRoundTrip(t *testing.T) {
	bom := string(BOM)
	tests := []struct {
		name  string
		in    string
		lines []string
		f     Format
		mixed bool
	}{
		{"empty", "", nil, Format{}, false},
		{"bom only", bom, nil, Format{BOM: true}, false},
		{"single line", "a", []string{"a"}, Format{}, false},
		{"single line newline", "a\n", []string{"a"}, Format{FinalNewline: true}, false},
		{"only newline", "\n", []string{""}, Format{FinalNewline: true}, false},
		{"lf", "a\nb\n", []string{"a", "b"}, Format{FinalNewline: true}, false},
		{"crlf", "a\r\nb\r\n", []string{"a", "b"}, Format{EOL: CRLF, FinalNewline: true}, false},
		{"crlf no final newline", "a\r\nb", []string{"a", "b"}, Format{EOL: CRLF}, false},
		{"lf no final newline", "a\nb", []string{"a", "b"}, Format{}, false},
		{"bom crlf", bom + "x\r\ny\r\n", []string{"x", "y"}, Format{EOL: CRLF, BOM: true, FinalNewline: true}, false},
		{"blank lines", "\n\na\n\n", []string{"", "", "a", ""}, Format{FinalNewline: true}, false},
		{"lone cr is content", "a\rb\n", []string{"a\rb"}, Format{FinalNewline: true}, false},
		{"trailing cr no newline", "a\nb\r", []string{"a", "b\r"}, Format{}, false},
		{"mixed lf dominant", "a\nb\r\nc\n", []string{"a", "b", "c"}, Format{FinalNewline: true}, true},
		{"mixed crlf dominant", "a\r\nb\nc\r\nd", []string{"a", "b", "c", "d"}, Format{EOL: CRLF}, true},
		{"mixed tie", "a\r\nb\n", []string{"a", "b"}, Format{FinalNewline: true}, true},
		{"multibyte", "привет\r\n世界\r\n", []string{"привет", "世界"}, Format{EOL: CRLF, FinalNewline: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Parse([]byte(tt.in))
			if !reflect.DeepEqual(d.Lines, tt.lines) {
				t.Fatalf("lines = %q, want %q", d.Lines, tt.lines)
			}
			if d.Format != tt.f {
				t.Fatalf("format = %+v, want %+v", d.Format, tt.f)
			}
			if d.Mixed() != tt.mixed {
				t.Fatalf("mixed = %v", d.Mixed())
			}
			if got := string(d.Bytes()); got != tt.in {
				t.Fatalf("round trip = %q, want %q", got, tt.in)
			}
		})
	}
}

func TestReplace(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		start, n int
		add      []string
		want     string
	}{
		{"crlf edit", "a\r\nb\r\nc\r\n", 1, 1, []string{"x", "y"}, "a\r\nx\r\ny\r\nc\r\n"},
		{"lf delete", "a\nb\nc\n", 0, 2, nil, "c\n"},
		{"append no final newline", "a\r\nb", 2, 0, []string{"c"}, "a\r\nb\r\nc"},
		{"insert into empty", "", 0, 0, []string{"a"}, "a"},
		{"bom kept", string(BOM) + "a\n", 0, 1, []string{"b"}, string(BOM) + "b\n"},
		{"mixed keeps other lines", "a\nb\r\nc\nd\n", 2, 1, []string{"x"}, "a\nb\r\nx\nd\n"},
		{"mixed crlf dominant", "a\r\nb\nc\r\n", 0, 1, []string{"x"}, "x\r\nb\nc\r\n"},
		{"mixed append after unterminated", "a\r\nb\nc", 3, 0, []string{"d"}, "a\r\nb\nc\nd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Parse([]byte(tt.in))
			d.Replace(tt.start, tt.n, tt.add)
			if got := string(d.Bytes()); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSplitInput(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"\n", []string{""}},
		{"a", []string{"a"}},
		{"a\n", []string{"a"}},
		{"a\r\nb\r\n", []string{"a", "b"}},
		{"a\nb\r\n\n", []string{"a", "b", ""}},
		{string(BOM) + "a\n", []string{"a"}},
	}
	for _, tt := range tests {
		if got := SplitInput([]byte(tt.in)); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("SplitInput(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestJoin(t *testing.T) {
	got := string(Join(SplitInput([]byte("a\nb\n")), Format{EOL: CRLF, BOM: true, FinalNewline: true}))
	if want := string(BOM) + "a\r\nb\r\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
