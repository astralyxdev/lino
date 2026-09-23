package history

import (
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Blob tags: the first byte of an encoded line blob.
const (
	tagRaw   byte = 0
	tagFlate byte = 1
)

// minCompress is the size below which lines are stored raw: flate cannot win
// on a few dozen bytes.
const minCompress = 64

var errCorrupt = errors.New("history: corrupt fragment")

// EncodeLines packs lines (without line terminators) into a blob, compressed
// with flate when that pays off. The line count is stored separately, so an
// empty slice and a single empty line both encode as an empty payload.
func EncodeLines(lines []string) []byte {
	if len(lines) == 0 {
		return nil
	}
	raw := strings.Join(lines, "\n")
	if len(raw) >= minCompress {
		var b bytes.Buffer
		b.WriteByte(tagFlate)
		w, _ := flate.NewWriter(&b, flate.DefaultCompression)
		io.WriteString(w, raw)
		w.Close()
		if b.Len() < len(raw)+1 {
			return b.Bytes()
		}
	}
	out := make([]byte, 0, len(raw)+1)
	out = append(out, tagRaw)
	return append(out, raw...)
}

// DecodeLines reverses EncodeLines; n is the number of lines encoded.
func DecodeLines(b []byte, n int) ([]string, error) {
	if n == 0 {
		if len(b) != 0 {
			return nil, errCorrupt
		}
		return nil, nil
	}
	if len(b) == 0 {
		return nil, errCorrupt
	}
	var raw string
	switch b[0] {
	case tagRaw:
		raw = string(b[1:])
	case tagFlate:
		d, err := io.ReadAll(flate.NewReader(bytes.NewReader(b[1:])))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errCorrupt, err)
		}
		raw = string(d)
	default:
		return nil, errCorrupt
	}
	lines := strings.Split(raw, "\n")
	if len(lines) != n {
		return nil, fmt.Errorf("%w: %d lines, want %d", errCorrupt, len(lines), n)
	}
	return lines, nil
}
