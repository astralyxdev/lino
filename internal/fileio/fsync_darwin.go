package fileio

import (
	"os"
	"syscall"
)

// Fsync flushes f to the drive with fsync(2). On macOS os.File.Sync issues
// F_FULLFSYNC, which also drains the drive's cache and costs ~5 ms per call;
// plain fsync keeps writes atomic across process and OS crashes, the same
// trade SQLite makes by default (fullfsync off).
func Fsync(f *os.File) error {
	for {
		err := syscall.Fsync(int(f.Fd()))
		if err != syscall.EINTR {
			if err != nil {
				return &os.PathError{Op: "fsync", Path: f.Name(), Err: err}
			}
			return nil
		}
	}
}
