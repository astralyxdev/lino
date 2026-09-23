package textfile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"github.com/astralyx/lino/internal/outcome"
)

// Kind classifies file content.
type Kind int

const (
	Text Kind = iota
	Binary
)

func (k Kind) String() string {
	if k == Text {
		return "text"
	}
	return "binary"
}

// BOM is the UTF-8 byte order mark.
var BOM = []byte{0xEF, 0xBB, 0xBF}

// PrefixSize is how much of a file is checked before the full validation.
const PrefixSize = 8 << 10

// Classify reports Text for valid UTF-8 without NUL bytes (a leading BOM is
// allowed) and Binary otherwise.
func Classify(b []byte) Kind {
	b = bytes.TrimPrefix(b, BOM)
	if bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
		return Binary
	}
	return Text
}

// classifyPrefix is Classify for a leading chunk of a longer stream: a rune
// cut at the end of the chunk is not an error.
func classifyPrefix(b []byte) Kind {
	for i := 1; i < utf8.UTFMax && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			if !utf8.FullRune(b[len(b)-i:]) {
				b = b[:len(b)-i]
			}
			break
		}
	}
	return Classify(b)
}

// TooLarge returns a refused error for a file of size bytes over max. max <= 0
// disables the check.
func TooLarge(path string, size, max int64) error {
	if max > 0 && size > max {
		return outcome.New(outcome.Refused, "%s: too large (%d bytes, limit %d)", path, size, max)
	}
	return nil
}

// ReadFile reads a file and classifies it. Files over max bytes are refused.
// Large files are rejected from their first PrefixSize bytes when those
// already show binary content, without reading the rest. Data is nil for Binary.
func ReadFile(path string, max int64) ([]byte, Kind, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, Binary, outcome.Wrap(outcome.NotFound, err, fmt.Sprintf("%s: no such file", path))
		}
		return nil, Binary, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, Binary, err
	}
	if !st.Mode().IsRegular() {
		return nil, Binary, outcome.New(outcome.Refused, "%s: not a regular file", path)
	}
	if err := TooLarge(path, st.Size(), max); err != nil {
		return nil, Binary, err
	}
	if st.Size() > PrefixSize {
		buf := make([]byte, PrefixSize)
		n, err := io.ReadFull(f, buf)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, Binary, err
		}
		if classifyPrefix(buf[:n]) == Binary {
			return nil, Binary, nil
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, Binary, err
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, maxOrInf(max)+1))
	if err != nil {
		return nil, Binary, err
	}
	if err := TooLarge(path, int64(len(data)), max); err != nil {
		return nil, Binary, err
	}
	if Classify(data) == Binary {
		return nil, Binary, nil
	}
	return data, Text, nil
}

func maxOrInf(max int64) int64 {
	if max <= 0 {
		return 1<<63 - 2
	}
	return max
}
