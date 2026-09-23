//go:build !linux

package watch

import "syscall"

func backend() string { return "kqueue" }

// watchLimit is the open-file limit: kqueue holds one descriptor per watched
// directory and per file inside it.
func watchLimit() int64 {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
		return 0
	}
	return int64(rl.Cur)
}
