package fileio

import (
	"os"
	"path/filepath"
	"strings"
)

// PruneEmptyDirs removes dir and then each parent that is left empty, up to
// but not including root. It stops at the first directory that still holds
// anything, is outside root, or is the meta directory. Errors are ignored:
// a directory that cannot be removed simply stays.
func PruneEmptyDirs(root, dir string) {
	for {
		rel, err := filepath.Rel(root, dir)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return
		}
		if rel := filepath.ToSlash(rel); rel == MetaDir || strings.HasPrefix(rel, MetaDir+"/") {
			return
		}
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
