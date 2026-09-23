package watch

import (
	"os"
	"strconv"
	"strings"
)

func backend() string { return "inotify" }

// watchLimit is fs.inotify.max_user_watches: one watch per directory.
func watchLimit() int64 {
	b, err := os.ReadFile("/proc/sys/fs/inotify/max_user_watches")
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}
