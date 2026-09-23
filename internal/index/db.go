package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	_ "modernc.org/sqlite"
)

// SchemaVersion is the current index schema. Bump it when the schema changes;
// add a Migrations entry to upgrade in place, otherwise the index is rebuilt.
const SchemaVersion = 2

// Migration upgrades an index from version from to from+1 inside tx.
type Migration func(ctx context.Context, tx *sql.Tx) error

// Migrations maps a schema version to the step that upgrades it by one.
var Migrations = map[int]Migration{
	1: func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, filesStatIndex)
		return err
	},
}

// filesStatIndex covers RefreshAll's scan so it never reads file content pages.
const filesStatIndex = `CREATE INDEX IF NOT EXISTS files_stat ON files(path, size, mtime)`

// schema v2. files holds the current content of every text file (empty for
// binary files); tri is an external-content FTS5 table over it and words a
// contentless one, both kept in sync by triggers. Updating only path/mtime/mode does not re-tokenise.
var schema = []string{
	`CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	`CREATE TABLE files(
		id      INTEGER PRIMARY KEY,
		path    TEXT UNIQUE NOT NULL,
		hash    TEXT NOT NULL,
		size    INTEGER NOT NULL,
		mtime   INTEGER NOT NULL,
		mode    INTEGER NOT NULL,
		lines   INTEGER NOT NULL,
		binary  INTEGER NOT NULL DEFAULT 0,
		content TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX files_hash ON files(hash)`,
	filesStatIndex,
	`CREATE VIRTUAL TABLE tri USING fts5(content, content='files', content_rowid='id', tokenize='trigram', detail=none)`,
	// words is contentless: it indexes lino_words(content) (see words.go),
	// which adds camelCase parts, so it cannot read back from files.
	`CREATE VIRTUAL TABLE words USING fts5(content, content='', contentless_delete=1, tokenize='unicode61')`,
	`CREATE TRIGGER files_ai AFTER INSERT ON files BEGIN
		INSERT INTO tri(rowid, content) VALUES (new.id, new.content);
		INSERT INTO words(rowid, content) VALUES (new.id, lino_words(new.content));
	END`,
	`CREATE TRIGGER files_ad AFTER DELETE ON files BEGIN
		INSERT INTO tri(tri, rowid, content) VALUES ('delete', old.id, old.content);
		DELETE FROM words WHERE rowid = old.id;
	END`,
	`CREATE TRIGGER files_au AFTER UPDATE OF content ON files BEGIN
		INSERT INTO tri(tri, rowid, content) VALUES ('delete', old.id, old.content);
		DELETE FROM words WHERE rowid = old.id;
		INSERT INTO tri(rowid, content) VALUES (new.id, new.content);
		INSERT INTO words(rowid, content) VALUES (new.id, lino_words(new.content));
	END`,
}

var pragmas = []string{
	"busy_timeout(5000)",
	"journal_mode(WAL)",
	"synchronous(NORMAL)",
	"cache_size(-65536)",
	"mmap_size(268435456)",
	"temp_store(MEMORY)",
}

// DB is an open index.
type DB struct {
	SQL  *sql.DB
	Path string
	// Rebuilt is true when the index was created empty on this open (new,
	// unreadable, or schema mismatch without a migration): run a full reconcile.
	Rebuilt bool
}

// Path returns the index path for a root.
func Path(root string) string { return filepath.Join(root, ".lino", "index.db") }

// Open opens <root>/.lino/index.db. The .lino directory must exist.
func Open(ctx context.Context, root string) (*DB, error) {
	return OpenPath(ctx, Path(root))
}

// OpenPath opens or creates the index at path, migrating or rebuilding it
// when its schema version differs from SchemaVersion.
func OpenPath(ctx context.Context, path string) (*DB, error) {
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("index dir: %w", err)
	}
	db, err := open(path)
	if err != nil {
		return rebuild(ctx, path)
	}
	v, empty, err := version(ctx, db)
	switch {
	case err == nil && empty:
		if err := create(ctx, db); err != nil {
			db.Close()
			return nil, err
		}
		return &DB{SQL: db, Path: path, Rebuilt: true}, nil
	case err == nil && v == SchemaVersion:
		return &DB{SQL: db, Path: path}, nil
	case err == nil && v < SchemaVersion:
		if err := migrate(ctx, db, v); err == nil {
			return &DB{SQL: db, Path: path}, nil
		}
	}
	db.Close()
	return rebuild(ctx, path)
}

// Close closes the database.
func (d *DB) Close() error { return d.SQL.Close() }

// Version returns the schema version recorded in the index.
func (d *DB) Version(ctx context.Context) (int, error) {
	v, _, err := version(ctx, d.SQL)
	return v, err
}

func open(path string) (*sql.DB, error) {
	q := url.Values{}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// version reads the stored schema version; empty is true for a database
// with no tables at all.
func version(ctx context.Context, db *sql.DB) (v int, empty bool, err error) {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master`).Scan(&n); err != nil {
		return 0, false, err
	}
	if n == 0 {
		return 0, true, nil
	}
	var s string
	err = db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&s)
	if err != nil {
		return 0, false, fmt.Errorf("index schema version: %w", err)
	}
	v, err = strconv.Atoi(s)
	if err != nil || v < 0 {
		return 0, false, fmt.Errorf("index schema version %q invalid", s)
	}
	return v, false, nil
}

func create(ctx context.Context, db *sql.DB) error {
	return inTx(ctx, db, func(tx *sql.Tx) error {
		for _, s := range schema {
			if _, err := tx.ExecContext(ctx, s); err != nil {
				return fmt.Errorf("index schema: %w", err)
			}
		}
		return setVersion(ctx, tx, SchemaVersion)
	})
}

func migrate(ctx context.Context, db *sql.DB, from int) error {
	return inTx(ctx, db, func(tx *sql.Tx) error {
		for v := from; v < SchemaVersion; v++ {
			m, ok := Migrations[v]
			if !ok {
				return fmt.Errorf("no index migration from version %d", v)
			}
			if err := m(ctx, tx); err != nil {
				return fmt.Errorf("index migration %d: %w", v, err)
			}
		}
		return setVersion(ctx, tx, SchemaVersion)
	})
}

func rebuild(ctx context.Context, path string) (*DB, error) {
	for _, p := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale index: %w", err)
		}
	}
	db, err := open(path)
	if err != nil {
		return nil, err
	}
	if err := create(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{SQL: db, Path: path, Rebuilt: true}, nil
}

func setVersion(ctx context.Context, tx *sql.Tx, v int) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES ('schema_version', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, strconv.Itoa(v))
	return err
}

func inTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
