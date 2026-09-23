package live

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/astralyx/lino/internal/registry"
)

// nameFile holds the alias a root's process runs under, so a later start
// (plain `lino run` or an auto-start) keeps it.
const nameFile = "name"

func namePath(root string) string { return filepath.Join(root, ".lino", nameFile) }

// StoredName returns the alias saved for root, or "" when none is saved or
// the saved one is not a valid name.
func StoredName(root string) string {
	b, err := os.ReadFile(namePath(root))
	if err != nil {
		return ""
	}
	n := strings.TrimSpace(string(b))
	if !registry.ValidName(n) {
		return ""
	}
	return n
}

// saveName records name as the alias of root.
func saveName(root, name string) error {
	if StoredName(root) == name {
		return nil
	}
	tmp := namePath(root) + ".tmp"
	if err := os.WriteFile(tmp, []byte(name+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, namePath(root))
}
