//go:build !darwin

package fileio

import "os"

// Fsync flushes f to the drive (fsync(2)).
func Fsync(f *os.File) error { return f.Sync() }
