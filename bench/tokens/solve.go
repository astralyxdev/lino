package tokens

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Helpers for the reference solutions. Each fails unless the edit matches
// exactly as expected, so a solution cannot silently drift from the pin.

func abs(root, rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

func edit(root, rel string, fn func(string) (string, error)) error {
	p := abs(root, rel)
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	out, err := fn(string(b))
	if err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	return os.WriteFile(p, []byte(out), 0o644)
}

// replace replaces old with new in rel; n is the exact number of matches
// expected, or -1 for at least one.
func replace(root, rel, old, new string, n int) error {
	return edit(root, rel, func(s string) (string, error) {
		if got := strings.Count(s, old); got == 0 || n >= 0 && got != n {
			return "", fmt.Errorf("%d matches of %q, want %d", got, old, n)
		}
		return strings.ReplaceAll(s, old, new), nil
	})
}

// replaceWord is replace for whole-word matches.
func replaceWord(root, rel, old, new string, n int) error {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(old) + `\b`)
	return edit(root, rel, func(s string) (string, error) {
		if got := len(re.FindAllStringIndex(s, -1)); got != n {
			return "", fmt.Errorf("%d matches of %q, want %d", got, old, n)
		}
		return re.ReplaceAllLiteralString(s, new), nil
	})
}

// editLines keeps the lines of rel for which keep is true.
func editLines(root, rel string, keep func(string) bool) error {
	return edit(root, rel, func(s string) (string, error) {
		var out []string
		for _, l := range strings.SplitAfter(s, "\n") {
			if keep(l) {
				out = append(out, l)
			}
		}
		return strings.Join(out, ""), nil
	})
}

func write(root, rel, content string) error {
	p := abs(root, rel)
	if _, err := os.Stat(p); err == nil {
		return fmt.Errorf("%s already exists", rel)
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

func move(root, from, to string) error {
	if _, err := os.Stat(abs(root, to)); err == nil {
		return fmt.Errorf("%s already exists", to)
	}
	return os.Rename(abs(root, from), abs(root, to))
}
