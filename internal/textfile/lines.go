package textfile

import (
	"bytes"
	"strings"
)

// EOL is a line ending style.
type EOL uint8

const (
	LF EOL = iota
	CRLF
)

func (e EOL) String() string {
	if e == CRLF {
		return "crlf"
	}
	return "lf"
}

func (e EOL) bytes() string {
	if e == CRLF {
		return "\r\n"
	}
	return "\n"
}

// Format is the per-file layout preserved across edits.
type Format struct {
	EOL          EOL
	BOM          bool
	FinalNewline bool
}

// Doc is a text file as lines without their endings.
//
// Mixed line endings: EOL is the dominant style (LF on a tie or when the file
// has no line breaks). Existing lines keep their own ending, so an unedited
// mixed file round-trips byte for byte; new lines get the dominant style.
// A lone \r is content, not a line break.
type Doc struct {
	Lines  []string
	Format Format
	eols   []EOL // per-line endings; nil unless the file mixes styles
}

// Parse splits text content into a Doc.
func Parse(b []byte) *Doc {
	d := &Doc{}
	if bytes.HasPrefix(b, BOM) {
		d.Format.BOM = true
		b = b[len(BOM):]
	}
	if len(b) == 0 {
		return d
	}
	s := string(b)
	n := strings.Count(s, "\n")
	d.Lines = make([]string, 0, n+1)
	eols := make([]EOL, 0, n+1)
	var crlf int
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			break
		}
		line, e := s[:i], LF
		if i > 0 && s[i-1] == '\r' {
			line, e = s[:i-1], CRLF
			crlf++
		}
		d.Lines = append(d.Lines, line)
		eols = append(eols, e)
		s = s[i+1:]
	}
	if s == "" {
		d.Format.FinalNewline = true
	} else {
		d.Lines = append(d.Lines, s)
		eols = append(eols, LF)
	}
	if crlf*2 > n {
		d.Format.EOL = CRLF
	}
	if crlf > 0 && crlf < n {
		if !d.Format.FinalNewline {
			eols[len(eols)-1] = d.Format.EOL
		}
		d.eols = eols
	}
	return d
}

// Mixed reports whether the file uses both LF and CRLF.
func (d *Doc) Mixed() bool { return d.eols != nil }

// Bytes renders the Doc in its own format.
func (d *Doc) Bytes() []byte {
	size := len(d.Lines) * 2
	if d.Format.BOM {
		size += len(BOM)
	}
	for _, l := range d.Lines {
		size += len(l)
	}
	var buf bytes.Buffer
	buf.Grow(size)
	if d.Format.BOM {
		buf.Write(BOM)
	}
	for i, l := range d.Lines {
		buf.WriteString(l)
		if i == len(d.Lines)-1 && !d.Format.FinalNewline {
			break
		}
		e := d.Format.EOL
		if d.eols != nil {
			e = d.eols[i]
		}
		buf.WriteString(e.bytes())
	}
	return buf.Bytes()
}

// Replace removes n lines starting at 0-based index start and inserts lines
// there. Inserted lines take the dominant ending. Indexes must be in range.
func (d *Doc) Replace(start, n int, lines []string) {
	d.Lines = splice(d.Lines, start, n, lines)
	if d.eols != nil {
		add := make([]EOL, len(lines))
		for i := range add {
			add[i] = d.Format.EOL
		}
		d.eols = splice(d.eols, start, n, add)
	}
}

func splice[T any](s []T, start, n int, add []T) []T {
	out := make([]T, 0, len(s)-n+len(add))
	out = append(out, s[:start]...)
	out = append(out, add...)
	return append(out, s[start+n:]...)
}

// Join renders lines in format f.
func Join(lines []string, f Format) []byte {
	return (&Doc{Lines: lines, Format: f}).Bytes()
}

// SplitInput splits new content (e.g. from stdin) into lines, dropping LF or
// CRLF endings and a leading BOM. A trailing newline does not add an empty
// line; empty input yields no lines.
func SplitInput(b []byte) []string {
	b = bytes.TrimPrefix(b, BOM)
	if len(b) == 0 {
		return nil
	}
	s := strings.TrimSuffix(string(b), "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}
