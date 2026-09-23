package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/registry"
)

// longHome returns a HOME exactly n bytes long, too long for ~/.lino/run/<id>.sock.
func longHome(t *testing.T, n int) string {
	t.Helper()
	base := shortHome(t)
	home := base + "/" + strings.Repeat("h", n-len(base)-1)
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestLongHome(t *testing.T) {
	h := New(t)
	h.Home = longHome(t, 150)
	h.Write("a.txt", "one\ntwo\n")
	h.must(0, Cmd{Args: []string{"init"}})
	stopOnCleanup(t, h)

	if r := h.must(0, Cmd{Args: []string{"read", "a.txt"}}); !strings.Contains(r.Stdout, "two") {
		t.Fatalf("read:\n%s", h.Transcript(r))
	}
	h.must(0, Cmd{Args: []string{"edit", "a.txt", "2", "2", "--v", h.fileV("a.txt")}, Stdin: "TWO\n"})
	if got := h.Read("a.txt"); got != "one\nTWO\n" {
		t.Errorf("after edit: %q", got)
	}

	id := registry.IDFor(h.Root)
	e, ok := liveEntry(t, h)
	if !ok {
		t.Fatal("no live entry")
	}
	if want := filepath.Join(registry.ShortSocketDir, id+".sock"); e.Socket != want {
		t.Errorf("entry socket %q, want %q", e.Socket, want)
	}
	for _, c := range []struct {
		path string
		mode os.FileMode
	}{
		{filepath.Dir(e.Socket), os.ModeDir | 0o700},
		{e.Socket, os.ModeSocket | 0o600},
	} {
		fi, err := os.Lstat(c.path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode() != c.mode {
			t.Errorf("%s mode %v, want %v", c.path, fi.Mode(), c.mode)
		}
	}

	h.must(0, Cmd{Args: []string{"stop"}})
	if _, err := os.Lstat(e.Socket); !os.IsNotExist(err) {
		t.Errorf("socket left after stop: %v", err)
	}
}
