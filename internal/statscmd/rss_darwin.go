package statscmd

import "syscall"

// rss returns the peak resident set size in bytes; the current size needs
// task_info, which is out of reach without cgo, so cur is 0.
func rss() (cur, peak int64) {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) == nil {
		peak = ru.Maxrss
	}
	return 0, peak
}
