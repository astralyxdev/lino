package anchor

import (
	"hash/fnv"
	"strconv"
	"strings"

	"github.com/astralyx/lino/internal/outcome"
)

// HashLen is the number of base62 characters in a line hash.
const HashLen = 3

// Space is the number of distinct line hashes (62^3).
const Space = 62 * 62 * 62

const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// Hash returns the 3-character base62 hash of a line's content. A trailing
// "\r" is ignored so LF and CRLF files hash the same.
func Hash(line string) string {
	line = strings.TrimSuffix(line, "\r")
	h := fnv.New64a()
	h.Write([]byte(line))
	v := h.Sum64()
	// Fold the high bits in so the modulo uses the whole hash.
	v ^= v >> 29
	v *= 0xbf58476d1ce4e5b9
	v ^= v >> 32
	n := v % Space
	var b [HashLen]byte
	for i := HashLen - 1; i >= 0; i-- {
		b[i] = alphabet[n%62]
		n /= 62
	}
	return string(b[:])
}

// Anchor addresses a line by its 1-based number and, optionally, its hash.
type Anchor struct {
	Line int
	Hash string // empty for a plain line number
}

// HasHash reports whether a carries a content hash.
func (a Anchor) HasHash() bool { return a.Hash != "" }

// String formats a as "<n>:<hash>", or "<n>" without a hash.
func (a Anchor) String() string {
	if a.Hash == "" {
		return strconv.Itoa(a.Line)
	}
	return strconv.Itoa(a.Line) + ":" + a.Hash
}

// Of returns the anchor for line n (1-based) with content text.
func Of(n int, text string) Anchor { return Anchor{Line: n, Hash: Hash(text)} }

// Parse parses "<n>:<hash>" or "<n>". Invalid input yields an outcome.Usage error.
func Parse(s string) (Anchor, error) {
	num, h, hasHash := strings.Cut(s, ":")
	n, err := parseLine(num)
	if err != nil {
		return Anchor{}, outcome.New(outcome.Usage, "invalid anchor %q: want <line> or <line>:<hash>", s)
	}
	if hasHash && !ValidHash(h) {
		return Anchor{}, outcome.New(outcome.Usage, "invalid anchor %q: hash must be %d base62 characters", s, HashLen)
	}
	return Anchor{Line: n, Hash: h}, nil
}

func parseLine(s string) (int, error) {
	if s == "" || len(s) > 9 {
		return 0, strconv.ErrSyntax
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, strconv.ErrSyntax
		}
	}
	n, _ := strconv.Atoi(s)
	if n < 1 {
		return 0, strconv.ErrRange
	}
	return n, nil
}

// ValidHash reports whether h is a well-formed line hash.
func ValidHash(h string) bool {
	if len(h) != HashLen {
		return false
	}
	for i := 0; i < len(h); i++ {
		if strings.IndexByte(alphabet, h[i]) < 0 {
			return false
		}
	}
	return true
}

// Prefix returns the per-line prefix printed by read --anchors: "N:hash|".
func Prefix(n int, text string) string {
	return strconv.Itoa(n) + ":" + Hash(text) + "|"
}

// FormatLine returns a full read --anchors line: "N:hash| text".
func FormatLine(n int, text string) string {
	return Prefix(n, text) + " " + strings.TrimSuffix(text, "\r")
}
