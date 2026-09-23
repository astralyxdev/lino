package histprune

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/config"
	_ "github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/histrec"
	"github.com/astralyx/lino/internal/initcmd"
	"github.com/astralyx/lino/internal/live"
	"github.com/astralyx/lino/internal/version"
)

func initRoot(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := initcmd.Init(context.Background(), root, root); err != nil {
		t.Fatal(err)
	}
	return root
}

var edits int

func edit(t *testing.T, root string, n int) {
	t.Helper()
	for range n {
		edits++
		b, err := os.ReadFile(filepath.Join(root, "f.txt"))
		if err != nil {
			t.Fatal(err)
		}
		var o, e bytes.Buffer
		args := []string{"edit", "f.txt", "1", "1", "--v", version.Of(b), "--direct"}
		code := cli.Default.Main(context.Background(), args,
			cli.Env{Cwd: root, Stdin: strings.NewReader(strings.Repeat("x", edits) + "\n"), Getenv: func(string) string { return "" }}, &o, &e)
		if code != 0 {
			t.Fatalf("lino %v: exit %d\n%s%s", args, code, o.String(), e.String())
		}
	}
}

// age makes every recorded change older than the default retention and
// forgets the last prune.
func age(t *testing.T, root string) {
	t.Helper()
	st, err := histrec.Store(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour).UnixNano()
	if _, err := st.SQL.Exec(`UPDATE changes SET time = ?`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SQL.Exec(`DELETE FROM meta WHERE key = 'last_prune'`); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, root string) int {
	t.Helper()
	st, err := histrec.Store(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.SQL.QueryRow(`SELECT count(*) FROM changes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDirectModePrunes(t *testing.T) {
	root := initRoot(t)
	edit(t, root, 3)
	age(t, root)
	edit(t, root, 1) // prunes the 3 old changes; keeps its own
	if n := count(t, root); n != 1 {
		t.Fatalf("%d changes after direct prune, want 1", n)
	}
	age(t, root)
	st, _ := histrec.Store(context.Background(), root)
	if _, err := st.SQL.Exec(`INSERT INTO meta(key, value) VALUES ('last_prune', ?)`, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	edit(t, root, 1) // pruned recently: nothing removed
	if n := count(t, root); n != 2 {
		t.Fatalf("%d changes, want 2: direct prune must be rate-limited", n)
	}
}

func TestLivePrunesPeriodically(t *testing.T) {
	root := initRoot(t)
	edit(t, root, 2)
	age(t, root)
	defer func(d time.Duration) { Every = d }(Every)
	Every = 10 * time.Millisecond
	p := &live.Process{Root: root, Config: config.Default()}
	start(p)
	deadline := time.Now().Add(5 * time.Second)
	for count(t, root) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("live prune did not run")
		}
		time.Sleep(5 * time.Millisecond)
	}
	edit(t, root, 1)
	age(t, root)
	for count(t, root) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("periodic prune did not run again")
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop(p)
	if len(byProc) != 0 {
		t.Fatal("process not forgotten on stop")
	}
}

func TestRetentionFromConfig(t *testing.T) {
	c, err := config.Parse(strings.NewReader("history.retention = 3d\nhistory.max_size = 10MB\n"), "config")
	if err != nil {
		t.Fatal(err)
	}
	r := Retention(c)
	if r.MaxAge != 72*time.Hour || r.MaxSize != 10<<20 {
		t.Fatalf("retention %+v", r)
	}
}
