package live

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/registry"
)

func TestWatcherKeepsIndexFresh(t *testing.T) {
	root := initRoot(t, map[string]string{"a.txt": "a\n"})
	reg := &registry.Registry{Dir: filepath.Join(shortTemp(t, "lw"), "run")}
	got := make(chan []index.Update, 16)
	OnExternal = func(_ context.Context, _ *Process, ups []index.Update) { got <- ups }
	t.Cleanup(func() { OnExternal = nil })
	p, err := Start(context.Background(), Options{Dir: root, Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p.Stop(ctx)
	})
	if !p.Watching() {
		t.Fatal("watcher not running")
	}
	if state, _ := p.WatchState(); strings.HasPrefix(state, "failed") || state == "not started" {
		t.Fatalf("state %q", state)
	}

	start := time.Now()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case ups := <-got:
		if len(ups) != 1 || ups[0].Path != "a.txt" || ups[0].Op != index.Modified {
			t.Fatalf("updates %+v", ups)
		}
	case <-time.After(time.Second):
		t.Fatal("external edit not applied within 1s")
	}
	fi, ok, err := p.DB.File(context.Background(), "a.txt")
	if err != nil || !ok || fi.Content != "changed\n" {
		t.Fatalf("indexed %+v ok=%v err=%v", fi, ok, err)
	}
	t.Logf("applied after %v", time.Since(start))
	if _, pending := p.WatchState(); pending != 0 {
		t.Fatalf("pending %d", pending)
	}
}
