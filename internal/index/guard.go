package index

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
)

// The index runs with synchronous=OFF: it is rebuildable, so commits skip
// fsync. A process crash cannot damage it (the OS still holds every write),
// but an OS crash or power loss while it is open can. guard records the boot
// the index was opened in; an open that finds a mark from another boot
// rebuilds the index instead of trusting it.
//
// <index>-open holds the mark and a flock: shared while open, exclusive to
// check the mark on open or clear it on the last close.
type guard struct {
	f  *os.File
	db string
}

// bootID identifies the current boot; tests override it.
var bootID = readBootID

// acquire opens the guard for the index at path; crashed reports that the
// index was left open by a boot that has since ended. The sole opener holds
// the lock exclusively until ready, so a rebuild happens under it.
func acquire(path string) (g *guard, crashed bool, err error) {
	name := path + "-open"
	_, statErr := os.Stat(name)
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, false, err
	}
	g = &guard{f: f, db: path}
	if os.IsNotExist(statErr) {
		if err := syncFile(filepath.Dir(path)); err != nil {
			g.close()
			return nil, false, err
		}
	}
	fd := int(f.Fd())
	if syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) == nil {
		// Sole opener: whatever mark is there was left by a process that died.
		boot := currentBoot()
		mark, err := readAll(f)
		if err == nil && string(mark) != boot {
			err = g.write([]byte(boot + "\n"))
		}
		if err != nil {
			g.close()
			return nil, false, err
		}
		return g, len(mark) > 0 && (string(mark) != boot || boot == unknownBoot), nil
	}
	if err := syscall.Flock(fd, syscall.LOCK_SH); err != nil {
		g.close()
		return nil, false, err
	}
	// The previous holder may have cleared the mark on its last close.
	if mark, err := readAll(f); err == nil && len(mark) == 0 {
		g.write([]byte(currentBoot() + "\n"))
	}
	return g, false, nil
}

// unknownBoot marks an index opened where the boot cannot be identified: any
// unclean exit is then treated as a crash.
const unknownBoot = "unknown"

func currentBoot() string {
	if b := bootID(); b != "" {
		return b
	}
	return unknownBoot
}

// ready downgrades the sole opener's lock so others can open the index too.
func (g *guard) ready() error {
	return syscall.Flock(int(g.f.Fd()), syscall.LOCK_SH)
}

// release is called after the database is closed. The last opener syncs the
// index file, whose last checkpoint was never synced, then clears the mark.
func (g *guard) release() error {
	if g == nil {
		return nil
	}
	defer g.close()
	if syscall.Flock(int(g.f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return nil
	}
	if err := syncFile(g.db); err != nil && !os.IsNotExist(err) {
		return err
	}
	return g.write(nil)
}

func (g *guard) close() { g.f.Close() }

func (g *guard) write(b []byte) error {
	if err := g.f.Truncate(0); err != nil {
		return err
	}
	if _, err := g.f.WriteAt(b, 0); err != nil {
		return err
	}
	return g.f.Sync()
}

func readAll(f *os.File) ([]byte, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	b := make([]byte, fi.Size())
	n, err := f.ReadAt(b, 0)
	if n == len(b) {
		err = nil
	}
	return bytes.TrimSpace(b[:n]), err
}

func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
