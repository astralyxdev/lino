package statscmd

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// rss returns the current and peak resident set size in bytes.
func rss() (cur, peak int64) {
	if b, err := os.ReadFile("/proc/self/statm"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 1 {
			n, _ := strconv.ParseInt(f[1], 10, 64)
			cur = n * int64(os.Getpagesize())
		}
	}
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) == nil {
		peak = ru.Maxrss * 1024
	}
	return cur, peak
}
