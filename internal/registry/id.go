package registry

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/astralyx/lino/internal/paths"
)

// IDLen is the length of a process id.
const IDLen = 6

const idAlphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

// IDFor returns the process id for an already canonical root path.
func IDFor(canonicalRoot string) string {
	sum := sha256.Sum256([]byte(canonicalRoot))
	n := binary.BigEndian.Uint64(sum[:8])
	var b [IDLen]byte
	for i := IDLen - 1; i >= 0; i-- {
		b[i] = idAlphabet[n%36]
		n /= 36
	}
	return string(b[:])
}

// IDForDir canonicalises dir and returns its process id.
func IDForDir(dir string) (string, error) {
	c, err := paths.Canonical(dir)
	if err != nil {
		return "", err
	}
	return IDFor(c), nil
}

// ValidID reports whether s has the syntax of a process id.
func ValidID(s string) bool {
	if len(s) != IDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}
