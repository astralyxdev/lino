package lscmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLsDirectFreshness(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{".lino/config": "", "a.txt": "1\n", "b.txt": "1\n"})
	if _, _, code := run(root, "ls"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	os.Remove(filepath.Join(root, "b.txt"))
	p := filepath.Join(root, "a.txt")
	os.WriteFile(p, []byte("1\n2\n3\n"), 0o644)
	later := time.Now().Add(time.Hour)
	os.Chtimes(p, later, later)
	out, errOut, code := run(root, "ls", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if strings.Contains(out, "b.txt") || !strings.Contains(out, `"lines":3`) {
		t.Fatalf("stale listing: %s", out)
	}
}
