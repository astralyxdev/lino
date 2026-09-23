package anchor

import (
	"fmt"
	"testing"

	"github.com/astralyx/lino/internal/outcome"
)

func TestHash(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		same bool
	}{
		{"crlf ignored", "foo", "foo\r", true},
		{"deterministic", "return nil", "return nil", true},
		{"differs", "foo", "bar", false},
		{"whitespace matters", "  x", " x", false},
		{"empty", "", "\r", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ha, hb := Hash(tt.a), Hash(tt.b)
			if !ValidHash(ha) || !ValidHash(hb) {
				t.Fatalf("invalid hash %q %q", ha, hb)
			}
			if (ha == hb) != tt.same {
				t.Fatalf("Hash(%q)=%s Hash(%q)=%s same=%v", tt.a, ha, tt.b, hb, tt.same)
			}
		})
	}
}

func TestHashDistribution(t *testing.T) {
	const n = 100000
	seen := make(map[string]int, n)
	firstChar := make(map[byte]int)
	for i := 0; i < n; i++ {
		h := Hash(fmt.Sprintf("\tx := foo(%d)", i))
		seen[h]++
		firstChar[h[0]]++
	}
	// Expected distinct for n balls in Space bins: Space*(1-exp(-n/Space)) ≈ 81,900.
	if len(seen) < 78000 {
		t.Fatalf("only %d distinct hashes of %d", len(seen), n)
	}
	if len(firstChar) != 62 {
		t.Fatalf("first char uses %d of 62 symbols", len(firstChar))
	}
	for c, cnt := range firstChar {
		if cnt < n/62/2 || cnt > n/62*2 {
			t.Fatalf("first char %q count %d is skewed", c, cnt)
		}
	}
}

func TestParseFormatRoundTrip(t *testing.T) {
	tests := []struct {
		in   string
		want Anchor
	}{
		{"1", Anchor{Line: 1}},
		{"13:9c1", Anchor{Line: 13, Hash: "9c1"}},
		{"240:ZzA", Anchor{Line: 240, Hash: "ZzA"}},
		{"999999999:000", Anchor{Line: 999999999, Hash: "000"}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := Parse(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("Parse(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
			if got.String() != tt.in {
				t.Fatalf("String() = %q, want %q", got.String(), tt.in)
			}
			if got.HasHash() != (tt.want.Hash != "") {
				t.Fatal("HasHash mismatch")
			}
		})
	}
	a := Of(7, "hello")
	b, err := Parse(a.String())
	if err != nil || b != a {
		t.Fatalf("round-trip Of: %+v %+v %v", a, b, err)
	}
}

func TestParseInvalid(t *testing.T) {
	for _, in := range []string{
		"", ":", "0", "0:abc", "-1", "+1", "1.5", "a", "12:", ":abc", "12:ab", "12:abcd",
		"12:a-c", "12:abc:d", " 12", "12 ", "1e3", "1234567890", "12:ab\r",
	} {
		t.Run(in, func(t *testing.T) {
			_, err := Parse(in)
			if !outcome.Is(err, outcome.Usage) {
				t.Fatalf("Parse(%q) err = %v, want usage", in, err)
			}
		})
	}
}

func TestFormatLine(t *testing.T) {
	tests := []struct {
		n    int
		text string
	}{
		{12, "func Withdraw() {"},
		{13, "    if amt <= 0 {\r"},
		{1, ""},
	}
	for _, tt := range tests {
		h := Hash(tt.text)
		want := fmt.Sprintf("%d:%s| %s", tt.n, h, trimCR(tt.text))
		if got := FormatLine(tt.n, tt.text); got != want {
			t.Fatalf("FormatLine = %q, want %q", got, want)
		}
		if got := Prefix(tt.n, tt.text); got != fmt.Sprintf("%d:%s|", tt.n, h) {
			t.Fatalf("Prefix = %q", got)
		}
	}
}

func trimCR(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\r' {
		return s[:len(s)-1]
	}
	return s
}
