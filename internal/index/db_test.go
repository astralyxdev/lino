package index

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func newRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".lino"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func mustOpen(t *testing.T, root string) *DB {
	t.Helper()
	db, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func count(t *testing.T, db *DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.SQL.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOpenCreateReopen(t *testing.T) {
	ctx := context.Background()
	root := newRoot(t)
	db := mustOpen(t, root)
	if !db.Rebuilt {
		t.Error("new index: Rebuilt = false")
	}
	if v, err := db.Version(ctx); err != nil || v != SchemaVersion {
		t.Fatalf("version = %d, %v", v, err)
	}
	var mode string
	if err := db.SQL.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v", mode, err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO files(path, hash, size, mtime, mode, lines, content)
		VALUES ('a.go', 'h1', 10, 1, 420, 2, 'func Withdraw()
return ErrInvalidAmount')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db = mustOpen(t, root)
	defer db.Close()
	if db.Rebuilt {
		t.Error("reopen: Rebuilt = true")
	}
	if n := count(t, db, `SELECT count(*) FROM files`); n != 1 {
		t.Errorf("files after reopen = %d", n)
	}
}

func TestFTSSync(t *testing.T) {
	db := mustOpen(t, newRoot(t))
	defer db.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.SQL.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO files(path, hash, size, mtime, mode, lines, content) VALUES ('a.go', 'h1', 1, 1, 420, 1, 'alpha ErrInvalidAmount')`)
	tri := `SELECT count(*) FROM tri WHERE tri MATCH ?`
	words := `SELECT count(*) FROM words WHERE words MATCH ?`

	tests := []struct {
		name  string
		setup string
		q     string
		query string
		want  int
	}{
		{"trigram after insert", "", tri, `"Inv" AND "nva"`, 1},
		{"words after insert", "", words, "alpha", 1},
		{"path-only update keeps rows", `UPDATE files SET path = 'b.go' WHERE path = 'a.go'`, tri, `"Inv"`, 1},
		{"content update replaces old", `UPDATE files SET content = 'beta' WHERE path = 'b.go'`, words, "alpha", 0},
		{"content update adds new", "", words, "beta", 1},
		{"delete removes", `DELETE FROM files`, words, "beta", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != "" {
				exec(tt.setup)
			}
			if n := count(t, db, tt.q, tt.query); n != tt.want {
				t.Errorf("count = %d, want %d", n, tt.want)
			}
		})
	}
	if _, err := db.SQL.Exec(`INSERT INTO tri(tri) VALUES ('integrity-check')`); err != nil {
		t.Errorf("tri integrity: %v", err)
	}
}

func TestSchemaMismatch(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name        string
		storedVer   string
		migrations  map[int]Migration
		wantRebuilt bool
		wantFiles   int
	}{
		{name: "newer version rebuilds", storedVer: "99", wantRebuilt: true},
		{name: "older without migration rebuilds", storedVer: "0", wantRebuilt: true},
		{name: "garbage version rebuilds", storedVer: "x", wantRebuilt: true},
		{name: "negative version rebuilds", storedVer: "-1", wantRebuilt: true},
		{
			name:      "older with migration migrates",
			storedVer: strconv.Itoa(SchemaVersion - 1),
			migrations: map[int]Migration{SchemaVersion - 1: func(ctx context.Context, tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `CREATE TABLE migrated(x)`)
				return err
			}},
			wantFiles: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRoot(t)
			db := mustOpen(t, root)
			if _, err := db.SQL.Exec(`INSERT INTO files(path, hash, size, mtime, mode, lines) VALUES ('a', 'h', 0, 0, 0, 0)`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.SQL.Exec(`UPDATE meta SET value = ? WHERE key = 'schema_version'`, tt.storedVer); err != nil {
				t.Fatal(err)
			}
			db.Close()

			saved := Migrations
			Migrations = tt.migrations
			defer func() { Migrations = saved }()

			db = mustOpen(t, root)
			defer db.Close()
			if db.Rebuilt != tt.wantRebuilt {
				t.Errorf("Rebuilt = %v, want %v", db.Rebuilt, tt.wantRebuilt)
			}
			if v, err := db.Version(ctx); err != nil || v != SchemaVersion {
				t.Errorf("version = %d, %v", v, err)
			}
			if n := count(t, db, `SELECT count(*) FROM files`); n != tt.wantFiles {
				t.Errorf("files = %d, want %d", n, tt.wantFiles)
			}
		})
	}
}

func TestOpenCorruptRebuilds(t *testing.T) {
	root := newRoot(t)
	if err := os.WriteFile(Path(root), []byte("this is not a sqlite database, just junk bytes....."), 0o644); err != nil {
		t.Fatal(err)
	}
	db := mustOpen(t, root)
	defer db.Close()
	if !db.Rebuilt {
		t.Error("Rebuilt = false")
	}
}

func TestOpenMissingDir(t *testing.T) {
	if _, err := Open(context.Background(), t.TempDir()); err == nil {
		t.Fatal("want error without .lino dir")
	}
}

func TestMigrateV1AddsStatIndex(t *testing.T) {
	root := newRoot(t)
	db := mustOpen(t, root)
	if _, err := db.SQL.Exec(`DROP INDEX files_stat`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE meta SET value = '1' WHERE key = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO files(path, hash, size, mtime, mode, lines) VALUES ('a', 'h', 0, 0, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db = mustOpen(t, root)
	defer db.Close()
	if db.Rebuilt {
		t.Fatal("rebuilt instead of migrated")
	}
	if n := count(t, db, `SELECT count(*) FROM sqlite_master WHERE name = 'files_stat'`); n != 1 {
		t.Fatalf("files_stat index missing")
	}
	if n := count(t, db, `SELECT count(*) FROM files`); n != 1 {
		t.Fatalf("files = %d", n)
	}
}
