package index

import (
	"os"
	"strings"
	"testing"
)

// crash drops db the way a killed process would: no checkpoint sync, no
// clearing of the open mark.
func crash(db *DB) {
	db.SQL.Close()
	db.guard.close()
	db.guard = nil
}

func mark(t *testing.T, db *DB) string {
	t.Helper()
	b, err := os.ReadFile(db.Path + "-open")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestGuardReopen(t *testing.T) {
	tests := []struct {
		name        string
		clean       bool
		boot, after string
		rebuilt     bool
	}{
		{"clean close", true, "b1", "b1", false},
		{"clean close, rebooted", true, "b1", "b2", false},
		{"process crash", false, "b1", "b1", false},
		{"os crash", false, "b1", "b2", true},
		{"unknown boot, clean", true, "", "", false},
		{"unknown boot, crash", false, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := bootID
			t.Cleanup(func() { bootID = orig })
			root := newRoot(t)
			bootID = func() string { return tt.boot }
			db := mustOpen(t, root)
			if _, err := db.SQL.Exec(`INSERT INTO files(path, hash, size, mtime, mode, lines, content)
				VALUES ('a.go', 'h', 1, 1, 420, 1, 'x')`); err != nil {
				t.Fatal(err)
			}
			if tt.clean {
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				if m := mark(t, db); m != "" {
					t.Fatalf("mark after clean close = %q", m)
				}
			} else {
				crash(db)
			}
			bootID = func() string { return tt.after }
			db = mustOpen(t, root)
			defer db.Close()
			if db.Rebuilt != tt.rebuilt {
				t.Errorf("Rebuilt = %v, want %v", db.Rebuilt, tt.rebuilt)
			}
			want := 1
			if tt.rebuilt {
				want = 0
			}
			if n := count(t, db, `SELECT count(*) FROM files`); n != want {
				t.Errorf("files = %d, want %d", n, want)
			}
			if m, want := mark(t, db), currentBoot(); m != want {
				t.Errorf("mark while open = %q, want %q", m, want)
			}
		})
	}
}

func TestGuardSharedOpen(t *testing.T) {
	root := newRoot(t)
	a := mustOpen(t, root)
	b := mustOpen(t, root)
	if b.Rebuilt {
		t.Fatal("second open rebuilt the index")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if m := mark(t, b); m == "" {
		t.Fatal("mark cleared while the index is still open")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if m := mark(t, b); m != "" {
		t.Fatalf("mark after last close = %q", m)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestSynchronousOff(t *testing.T) {
	db := mustOpen(t, newRoot(t))
	defer db.Close()
	if n := count(t, db, `PRAGMA synchronous`); n != 0 {
		t.Errorf("synchronous = %d, want 0 (OFF)", n)
	}
}
