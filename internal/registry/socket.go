package registry

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	"github.com/astralyx/lino/internal/outcome"
)

// MaxSocketPath is the longest path a unix socket can bind to: sun_path is
// 104 bytes on macOS and the BSDs, 108 on Linux, including the trailing NUL.
var MaxSocketPath = func() int {
	if runtime.GOOS == "linux" {
		return 107
	}
	return 103
}()

// ShortSocketDir holds sockets whose default path under the registry
// directory is too long. It is per user and independent of $TMPDIR.
var ShortSocketDir = filepath.Join("/tmp", "lino-"+strconv.Itoa(os.Getuid()))

// SocketPath returns the socket path for id: <dir>/<id>.sock when it fits the
// unix limit, else <ShortSocketDir>/<id>.sock.
func (r *Registry) SocketPath(id string) string {
	p := filepath.Join(r.Dir, id+".sock")
	if len(p) <= MaxSocketPath {
		return p
	}
	return filepath.Join(ShortSocketDir, id+".sock")
}

// PrepareSocket returns the socket path for id with its directory created
// (mode 0700). The short directory must be a real directory owned by the
// current user; anything else is refused rather than trusted.
func (r *Registry) PrepareSocket(id string) (string, error) {
	p := r.SocketPath(id)
	if len(p) > MaxSocketPath {
		return "", outcome.New(outcome.Refused,
			"socket path %s is %d bytes; unix sockets allow at most %d", p, len(p), MaxSocketPath).
			WithHint("use a shorter HOME")
	}
	dir := filepath.Dir(p)
	if dir == r.Dir {
		return p, os.MkdirAll(dir, 0o700)
	}
	return p, securePrivateDir(dir)
}

// securePrivateDir creates dir with mode 0700, or checks that an existing one
// is a directory (not a symlink) owned by the current user and tightens its mode.
func securePrivateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok {
		return outcome.New(outcome.Refused, "socket directory %s is not a directory", dir).
			WithHint("remove it and retry")
	}
	if int(st.Uid) != os.Getuid() {
		return outcome.New(outcome.Refused, "socket directory %s is owned by uid %d, not by you (uid %d)", dir, st.Uid, os.Getuid()).
			WithHint("remove it and retry")
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("socket directory %s: %w", dir, err)
		}
	}
	return nil
}
