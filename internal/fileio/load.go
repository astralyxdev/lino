package fileio

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/paths"
	"github.com/astralyx/lino/internal/textfile"
)

// MetaDir is the per-root lino directory; it is never read or written as a file.
const MetaDir = ".lino"

// File is a text file loaded through the root jail.
type File struct {
	Path    string // root-relative display path, slash-separated
	Real    string // absolute symlink-resolved path to read or write
	Data    []byte // raw bytes as on disk
	Doc     *textfile.Doc
	Mode    fs.FileMode // permission bits
	Size    int64
	ModTime time.Time
}

// Lines returns the file's lines without line endings.
func (f *File) Lines() []string { return f.Doc.Lines }

// Format returns the file's line ending, BOM and final-newline style.
func (f *File) Format() textfile.Format { return f.Doc.Format }

// LoadFile loads the text file p (relative to cwd, or to the root when cwd is
// empty or outside it) through the root jail. Files over maxSize bytes are
// refused; maxSize <= 0 disables the limit.
//
// Outcomes: refused for paths outside the root or inside .lino/, directories,
// non-regular files, binary files and files over maxSize; not_found for
// missing files.
func LoadFile(root *paths.Root, cwd, p string, maxSize int64) (*File, error) {
	rel, real, err := root.Jail(cwd, p)
	if err != nil {
		return nil, err
	}
	if inMeta(rel) || inMeta(relOrEmpty(root, real)) {
		return nil, outcome.New(outcome.Refused, "%s: inside %s/", rel, MetaDir)
	}
	st, err := os.Stat(real)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, outcome.Wrap(outcome.NotFound, err, rel+": no such file")
		}
		return nil, outcome.Wrap(outcome.Internal, err, rel+": "+err.Error())
	}
	switch {
	case st.IsDir():
		return nil, outcome.New(outcome.Refused, "%s: is a directory", rel).WithHint("lino ls " + rel)
	case !st.Mode().IsRegular():
		return nil, outcome.New(outcome.Refused, "%s: not a regular file", rel)
	}
	if err := textfile.TooLarge(rel, st.Size(), maxSize); err != nil {
		return nil, err
	}
	data, kind, err := textfile.ReadFile(real, maxSize)
	if err != nil {
		if e, ok := outcome.As(err); ok {
			switch e.Outcome {
			case outcome.NotFound:
				return nil, outcome.Wrap(outcome.NotFound, err, rel+": no such file")
			case outcome.Refused:
				return nil, outcome.Wrap(outcome.Refused, err, strings.Replace(e.Message, real, rel, 1))
			}
			return nil, err
		}
		return nil, outcome.Wrap(outcome.Internal, err, rel+": "+err.Error())
	}
	if kind == textfile.Binary {
		return nil, outcome.New(outcome.Refused, "%s: binary file, not readable as text", rel)
	}
	return &File{
		Path:    rel,
		Real:    real,
		Data:    data,
		Doc:     textfile.Parse(data),
		Mode:    st.Mode().Perm(),
		Size:    int64(len(data)),
		ModTime: st.ModTime(),
	}, nil
}

func relOrEmpty(root *paths.Root, abs string) string {
	rel, _ := root.Rel(abs)
	return rel
}

func inMeta(rel string) bool {
	return rel == MetaDir || strings.HasPrefix(rel, MetaDir+"/")
}
