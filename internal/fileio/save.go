package fileio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/textfile"
)

// DefaultMode is the permission of files lino creates.
const DefaultMode fs.FileMode = 0o644

// beforeRename is a test hook run after the temp file is complete.
var beforeRename func(tmp string) error

// SaveFile renders lines in format f and writes them atomically to real (an
// absolute path already checked by paths.Root.Jail) with permission mode.
func SaveFile(real string, lines []string, f textfile.Format, mode fs.FileMode) error {
	return WriteAtomic(real, textfile.Join(lines, f), mode)
}

// SaveDoc writes d atomically, keeping per-line endings of mixed files.
func SaveDoc(real string, d *textfile.Doc, mode fs.FileMode) error {
	return WriteAtomic(real, d.Bytes(), mode)
}

// WriteAtomic writes data to a temp file in real's directory, fsyncs it, sets
// mode, renames it over real and fsyncs the directory. Missing parent
// directories are created. mode 0 means DefaultMode. On error the original
// file is untouched and no temp file is left behind.
func WriteAtomic(real string, data []byte, mode fs.FileMode) (err error) {
	if mode == 0 {
		mode = DefaultMode
	}
	dir := filepath.Dir(real)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return writeErr(real, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(real)+".lino-*")
	if err != nil {
		return writeErr(real, err)
	}
	name := tmp.Name()
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(name)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return writeErr(real, err)
	}
	if err = tmp.Sync(); err != nil {
		return writeErr(real, err)
	}
	if err = tmp.Chmod(mode.Perm()); err != nil {
		return writeErr(real, err)
	}
	if err = tmp.Close(); err != nil {
		return writeErr(real, err)
	}
	if beforeRename != nil {
		if err = beforeRename(name); err != nil {
			return writeErr(real, err)
		}
	}
	if err = os.Rename(name, real); err != nil {
		return writeErr(real, err)
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return writeErr(dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return writeErr(dir, err)
	}
	return nil
}

func writeErr(p string, err error) error {
	msg := err
	var pe *fs.PathError
	var le *os.LinkError
	switch {
	case errors.As(err, &pe):
		msg = pe.Err
	case errors.As(err, &le):
		msg = le.Err
	}
	return outcome.Wrap(outcome.Internal, err, "write "+filepath.Base(p)+": "+msg.Error())
}
