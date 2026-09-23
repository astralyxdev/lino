package version

import (
	"crypto/sha256"
	"encoding/hex"
)

// Len is the number of hex characters in a version.
//
// 24 bits: versions are only ever compared between states of the same file,
// so the chance that an edited file keeps its version is about 1 in 16.7M per
// change. That is below the anchor hash risk and keeps --v short to type.
const Len = 6

// Of returns the version of the whole file content b, bytes as on disk.
func Of(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:Len/2])
}

// Valid reports whether s has the syntax of a version.
func Valid(s string) bool {
	if len(s) != Len {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
