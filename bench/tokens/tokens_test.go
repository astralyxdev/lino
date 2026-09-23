package tokens

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskSet(t *testing.T) {
	if n := len(Tasks); n < 15 || n > 30 {
		t.Errorf("%d tasks, want 15-30", n)
	}
	ids := map[string]bool{}
	cats := map[Category]int{}
	for _, task := range Tasks {
		switch {
		case ids[task.ID]:
			t.Errorf("duplicate id %s", task.ID)
		case task.Title == "" || task.Prompt == "" || len(task.Allowed) == 0:
			t.Errorf("%s: missing title, prompt or allowed paths", task.ID)
		case task.check == nil || task.solve == nil:
			t.Errorf("%s: missing check or solution", task.ID)
		}
		ids[task.ID] = true
		cats[task.Category]++
	}
	for _, c := range []Category{FindEdit, LargeFile, Rename, MultiFile, Insert, NewFile, Move, Delete, Config} {
		if cats[c] == 0 {
			t.Errorf("no %s task", c)
		}
	}
	if _, err := Get("nope"); err == nil {
		t.Error("Get(nope) succeeded")
	}
}

// corpus is the pinned archive bench/corpus.sh downloads, or $LINO_TOKENS_CORPUS.
func corpus(t *testing.T) string {
	p := os.Getenv("LINO_TOKENS_CORPUS")
	if p == "" {
		p = filepath.Join("..", ".corpus", "go-"+strings.TrimPrefix(Pin, "golang/go@")+".tar.gz")
	}
	if _, err := os.Stat(p); err != nil {
		t.Skipf("corpus not available (%v); run bench/corpus.sh", err)
	}
	return p
}

// TestTasks checks every task on the pinned tree: the pristine tree fails the
// check, the reference solution passes it, and a stray edit fails it.
func TestTasks(t *testing.T) {
	if testing.Short() {
		t.Skip("copies the 2.4M-line corpus")
	}
	src := corpus(t)
	pristineDir, work := t.TempDir(), t.TempDir()
	pristine, err := Prepare(src, pristineDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(src, work); err != nil {
		t.Fatal(err)
	}
	restore := func() {
		now, err := Scan(work)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range pristine.Diff(now) {
			dst := filepath.Join(work, filepath.FromSlash(p))
			b, err := os.ReadFile(filepath.Join(pristineDir, filepath.FromSlash(p)))
			switch {
			case os.IsNotExist(err):
				err = os.Remove(dst)
			case err == nil:
				err = os.WriteFile(dst, b, 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := range Tasks {
		task := &Tasks[i]
		t.Run(task.ID, func(t *testing.T) {
			defer restore()
			if res, _ := task.Check(work, pristine); res.OK {
				t.Fatal("pristine tree passes the check")
			}
			if err := task.Solve(work); err != nil {
				t.Fatalf("solve: %v", err)
			}
			res, err := task.Check(work, pristine)
			if err != nil || !res.OK {
				t.Fatalf("reference solution fails: %v %q (changed %v)", err, res.Errors, res.Changed)
			}
			if err := os.WriteFile(filepath.Join(work, "go.work"), []byte("go 1.24\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if res, _ := task.Check(work, pristine); res.OK {
				t.Error("a stray file passes the check")
			}
		})
	}
}
