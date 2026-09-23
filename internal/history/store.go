package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/astralyx/lino/internal/linediff"
	_ "modernc.org/sqlite"
)

// SchemaVersion is the current history schema. History cannot be rebuilt from
// the files, so a newer or unreadable history.db is replaced by an empty one
// only as a last resort; bump with a migration whenever possible.
const SchemaVersion = 1

// Source of a change.
const (
	SourceLino     = "lino"
	SourceExternal = "external"
)

// Operations recorded in history.
const (
	OpEdit     = "edit"
	OpInsert   = "insert"
	OpDelete   = "delete"
	OpReplace  = "replace"
	OpWrite    = "write"
	OpMv       = "mv"
	OpRm       = "rm"
	OpRollback = "rollback"
	OpExternal = "external"
)

// ErrNotFound reports an unknown or pruned change id.
var ErrNotFound = errors.New("history: change not found")

// Extra holds per-operation details that do not fit the fixed columns.
type Extra struct {
	From    string `json:"from,omitempty"`    // mv: source path (Path is the target)
	Undid   int64  `json:"undid,omitempty"`   // rollback: the change it undid
	Created bool   `json:"created,omitempty"` // write/external: file did not exist before
	Removed bool   `json:"removed,omitempty"` // rm/external: file no longer exists
	Binary  bool   `json:"binary,omitempty"`  // recorded by hash only; not rollbackable
	Mode    uint32 `json:"mode,omitempty"`    // rm: file mode to restore
	Symlink bool   `json:"symlink,omitempty"` // rm of a symlink: not rollbackable
	// Line style of a removed file, so a restore writes it back as it was.
	CRLF           bool `json:"crlf,omitempty"`
	BOM            bool `json:"bom,omitempty"`
	NoFinalNewline bool `json:"no_final_newline,omitempty"`
}

// Change is one recorded change. Fragments are positioned in the version
// before the change (linediff semantics: Apply to before yields after).
type Change struct {
	ID        int64
	Time      time.Time
	Source    string
	Author    string
	Path      string
	Op        string
	VBefore   string
	VAfter    string
	Extra     Extra
	Fragments []linediff.Fragment
}

var schema = []string{
	`CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	`CREATE TABLE changes(
		id       INTEGER PRIMARY KEY,
		time     INTEGER NOT NULL,
		source   TEXT NOT NULL,
		author   TEXT NOT NULL DEFAULT '',
		path     TEXT NOT NULL,
		op       TEXT NOT NULL,
		v_before TEXT NOT NULL DEFAULT '',
		v_after  TEXT NOT NULL DEFAULT '',
		extra    TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX changes_path ON changes(path, id)`,
	`CREATE INDEX changes_author ON changes(author, id)`,
	`CREATE INDEX changes_time ON changes(time)`,
	`CREATE TABLE fragments(
		change_id INTEGER NOT NULL REFERENCES changes(id) ON DELETE CASCADE,
		seq       INTEGER NOT NULL,
		pos       INTEGER NOT NULL,
		old_n     INTEGER NOT NULL,
		new_n     INTEGER NOT NULL,
		old_lines BLOB,
		new_lines BLOB,
		PRIMARY KEY(change_id, seq)
	) WITHOUT ROWID`,
}

var pragmas = []string{
	"busy_timeout(5000)",
	"journal_mode(WAL)",
	"synchronous(NORMAL)",
	"foreign_keys(ON)",
}

// Store is an open history database.
type Store struct {
	SQL  *sql.DB
	Path string
	// Reset is true when an existing but unusable history.db was replaced by
	// an empty one on this open (undo history lost).
	Reset bool
}

// Path returns the history path for a root.
func Path(root string) string { return filepath.Join(root, ".lino", "history.db") }

// Open opens <root>/.lino/history.db. The .lino directory must exist.
func Open(ctx context.Context, root string) (*Store, error) {
	return OpenPath(ctx, Path(root))
}

// OpenPath opens or creates the history at path. A missing file is created
// empty; a file that is not a history database of a known version is replaced.
func OpenPath(ctx context.Context, path string) (*Store, error) {
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("history dir: %w", err)
	}
	db, err := open(path)
	if err != nil {
		return reset(ctx, path)
	}
	v, empty, err := version(ctx, db)
	switch {
	case err == nil && empty:
		if err := create(ctx, db); err != nil {
			db.Close()
			return nil, err
		}
		return &Store{SQL: db, Path: path}, nil
	case err == nil && v == SchemaVersion:
		return &Store{SQL: db, Path: path}, nil
	}
	db.Close()
	return reset(ctx, path)
}

// Close closes the database.
func (s *Store) Close() error { return s.SQL.Close() }

// Add records c. A zero c.ID takes the next id; a zero c.Time takes now.
// It returns the id used.
func (s *Store) Add(ctx context.Context, c Change) (int64, error) {
	tx, err := s.SQL.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := AddTx(ctx, tx, c)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// AddTx records c inside tx, for callers that write several rows at once.
func AddTx(ctx context.Context, tx *sql.Tx, c Change) (int64, error) {
	if c.Time.IsZero() {
		c.Time = time.Now()
	}
	extra := ""
	if c.Extra != (Extra{}) {
		b, err := json.Marshal(c.Extra)
		if err != nil {
			return 0, err
		}
		extra = string(b)
	}
	var id any
	if c.ID != 0 {
		id = c.ID
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO changes(id, time, source, author, path, op, v_before, v_after, extra)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, c.Time.UnixNano(), c.Source, c.Author, c.Path, c.Op, c.VBefore, c.VAfter, extra)
	if err != nil {
		return 0, fmt.Errorf("history: add change: %w", err)
	}
	cid, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	ins, err := tx.PrepareContext(ctx,
		`INSERT INTO fragments(change_id, seq, pos, old_n, new_n, old_lines, new_lines) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer ins.Close()
	for i, f := range c.Fragments {
		if _, err := ins.ExecContext(ctx, cid, i, f.Pos, len(f.Old), len(f.New), EncodeLines(f.Old), EncodeLines(f.New)); err != nil {
			return 0, fmt.Errorf("history: add fragment: %w", err)
		}
	}
	return cid, nil
}

// Get returns change id with its fragments, or ErrNotFound.
func (s *Store) Get(ctx context.Context, id int64) (Change, error) {
	cs, err := s.list(ctx, `WHERE id = ?`, []any{id})
	if err != nil {
		return Change{}, err
	}
	if len(cs) == 0 {
		return Change{}, ErrNotFound
	}
	c := cs[0]
	c.Fragments, err = s.Fragments(ctx, id)
	return c, err
}

// Fragments returns the fragments of change id in order.
func (s *Store) Fragments(ctx context.Context, id int64) ([]linediff.Fragment, error) {
	rows, err := s.SQL.QueryContext(ctx,
		`SELECT pos, old_n, new_n, old_lines, new_lines FROM fragments WHERE change_id = ? ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []linediff.Fragment
	for rows.Next() {
		var f linediff.Fragment
		var on, nn int
		var ob, nb []byte
		if err := rows.Scan(&f.Pos, &on, &nn, &ob, &nb); err != nil {
			return nil, err
		}
		if f.Old, err = DecodeLines(ob, on); err != nil {
			return nil, fmt.Errorf("change %d: %w", id, err)
		}
		if f.New, err = DecodeLines(nb, nn); err != nil {
			return nil, fmt.Errorf("change %d: %w", id, err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Filter selects changes for List. Zero fields do not filter.
type Filter struct {
	Path   string // exact path, or a directory prefix when it ends in "/"
	Since  int64  // only ids greater than Since
	Before int64  // only ids less than Before
	By     string // author
	Limit  int
	Asc    bool // oldest first; default newest first
}

// List returns matching changes without fragments.
func (s *Store) List(ctx context.Context, f Filter) ([]Change, error) {
	var where []string
	var args []any
	switch {
	case strings.HasSuffix(f.Path, "/"):
		// Also match mv sources under the prefix.
		where = append(where, `(instr(path, ?) = 1 OR instr(json_extract(nullif(extra, ''), '$.from'), ?) = 1)`)
		args = append(args, f.Path, f.Path)
	case f.Path != "":
		where = append(where, `(path = ? OR json_extract(nullif(extra, ''), '$.from') = ?)`)
		args = append(args, f.Path, f.Path)
	}
	if f.Since > 0 {
		where = append(where, `id > ?`)
		args = append(args, f.Since)
	}
	if f.Before > 0 {
		where = append(where, `id < ?`)
		args = append(args, f.Before)
	}
	if f.By != "" {
		where = append(where, `author = ?`)
		args = append(args, f.By)
	}
	q := ""
	if len(where) > 0 {
		q = "WHERE " + strings.Join(where, " AND ")
	}
	if f.Asc {
		q += " ORDER BY id"
	} else {
		q += " ORDER BY id DESC"
	}
	if f.Limit > 0 {
		q += " LIMIT " + strconv.Itoa(f.Limit)
	}
	return s.list(ctx, q, args)
}

// Latest returns the highest recorded change id, 0 if none.
func (s *Store) Latest(ctx context.Context) (int64, error) {
	var id sql.NullInt64
	err := s.SQL.QueryRowContext(ctx, `SELECT max(id) FROM changes`).Scan(&id)
	return id.Int64, err
}

func (s *Store) list(ctx context.Context, clause string, args []any) ([]Change, error) {
	rows, err := s.SQL.QueryContext(ctx,
		`SELECT id, time, source, author, path, op, v_before, v_after, extra FROM changes `+clause, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Change
	for rows.Next() {
		var c Change
		var t int64
		var extra string
		if err := rows.Scan(&c.ID, &t, &c.Source, &c.Author, &c.Path, &c.Op, &c.VBefore, &c.VAfter, &extra); err != nil {
			return nil, err
		}
		c.Time = time.Unix(0, t)
		if extra != "" {
			if err := json.Unmarshal([]byte(extra), &c.Extra); err != nil {
				return nil, fmt.Errorf("change %d extra: %w", c.ID, err)
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func open(path string) (*sql.DB, error) {
	q := url.Values{}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	// Transactions read before they write; a deferred one cannot upgrade
	// after another connection committed and fails with SQLITE_BUSY without
	// waiting. BEGIN IMMEDIATE takes the write lock up front, under busy_timeout.
	q.Set("_txlock", "immediate")
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

func version(ctx context.Context, db *sql.DB) (v int, empty bool, err error) {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master`).Scan(&n); err != nil {
		return 0, false, err
	}
	if n == 0 {
		return 0, true, nil
	}
	var s string
	if err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&s); err != nil {
		return 0, false, fmt.Errorf("history schema version: %w", err)
	}
	v, err = strconv.Atoi(s)
	if err != nil {
		return 0, false, fmt.Errorf("history schema version %q invalid", s)
	}
	return v, false, nil
}

func create(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Another process may have created it since we looked; the immediate
	// transaction serialises the check.
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	for _, s := range schema {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("history schema: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES ('schema_version', ?)`, strconv.Itoa(SchemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

func reset(ctx context.Context, path string) (*Store, error) {
	for _, p := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove unusable history: %w", err)
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
	return &Store{SQL: db, Path: path, Reset: true}, nil
}
