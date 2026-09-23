package index

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"
	"time"

	"github.com/astralyx/lino/internal/textfile"
)

// Hash is the content hash stored in files.hash: full sha256, lowercase hex.
// Its first version.Len characters are the file's version.
func Hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Op is what an index update did to one file.
type Op uint8

const (
	Unchanged Op = iota
	Added
	Modified
	Removed
	Moved
)

func (o Op) String() string {
	return [...]string{"unchanged", "added", "modified", "removed", "moved"}[o]
}

// Update reports the effect of IndexFile, IndexData or RemoveFile.
type Update struct {
	Path    string
	From    string // previous path when Op is Moved
	Op      Op
	OldHash string // "" when the file was not indexed
	Hash    string // "" when removed
	Binary  bool
	// Old is the indexed content before a Modified or Removed update, so hooks
	// can diff external edits; the new content is the current row (DB.File).
	// OldBinary reports that the previous row was binary (Old is then "").
	Old       string
	OldBinary bool
}

// FileInfo is one row of the files table.
type FileInfo struct {
	Path    string
	Hash    string
	Size    int64
	ModTime time.Time
	Mode    fs.FileMode
	Lines   int
	Binary  bool
	Content string // raw text as on disk; "" for binary files
}

// Stat is the metadata part of a file to index.
type Stat struct {
	Size    int64
	ModTime time.Time
	Mode    fs.FileMode
}

// IndexFile indexes the file at abs under the root-relative path rel. An
// unchanged hash only refreshes size, mtime and mode. A missing or
// non-regular file is removed from the index. Files over maxSize (> 0) are
// recorded by hash only, like binary files.
func (d *DB) IndexFile(ctx context.Context, rel, abs string, maxSize int64) (Update, error) {
	f, err := os.Open(abs)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) { // ENOTDIR: a parent became a file
		return d.RemoveFile(ctx, rel)
	}
	if err != nil {
		return Update{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Update{}, err
	}
	if !st.Mode().IsRegular() {
		return d.RemoveFile(ctx, rel)
	}
	s := Stat{Size: st.Size(), ModTime: st.ModTime(), Mode: st.Mode().Perm()}
	if maxSize > 0 && st.Size() > maxSize {
		h := sha256.New()
		n, err := io.Copy(h, f)
		if err != nil {
			return Update{}, err
		}
		s.Size = n
		return d.upsert(ctx, rel, hex.EncodeToString(h.Sum(nil)), s, nil, true)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return Update{}, err
	}
	s.Size = int64(len(data))
	return d.IndexData(ctx, rel, data, s)
}

// IndexData indexes content already in memory, e.g. right after lino wrote it.
func (d *DB) IndexData(ctx context.Context, rel string, data []byte, s Stat) (Update, error) {
	return d.upsert(ctx, rel, Hash(data), s, data, textfile.Classify(data) == textfile.Binary)
}

func (d *DB) upsert(ctx context.Context, rel, hash string, s Stat, data []byte, binary bool) (Update, error) {
	var u Update
	err := inTx(ctx, d.SQL, func(tx *sql.Tx) error {
		var err error
		u, err = upsertTx(ctx, tx, rel, hash, s, data, binary)
		return err
	})
	if err != nil {
		return Update{}, fmt.Errorf("index %s: %w", rel, err)
	}
	return u, nil
}

func upsertTx(ctx context.Context, tx *sql.Tx, rel, hash string, s Stat, data []byte, binary bool) (Update, error) {
	u := Update{Path: rel, Hash: hash, Binary: binary}
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id, hash FROM files WHERE path = ?`, rel).Scan(&id, &u.OldHash)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		u.Op = Added
	case err != nil:
		return u, err
	case u.OldHash == hash:
		u.Op = Unchanged
		_, err := tx.ExecContext(ctx, `UPDATE files SET size = ?, mtime = ?, mode = ? WHERE id = ?`,
			s.Size, s.ModTime.UnixNano(), int64(s.Mode), id)
		return u, err
	default:
		u.Op = Modified
		var oldBin int
		if err := tx.QueryRowContext(ctx, `SELECT binary, content FROM files WHERE id = ?`, id).Scan(&oldBin, &u.Old); err != nil {
			return u, err
		}
		u.OldBinary = oldBin != 0
	}
	content, lines := "", 0
	if !binary {
		content = string(data)
		lines = len(textfile.Parse(data).Lines)
	}
	if u.Op == Added {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO files(path, hash, size, mtime, mode, lines, binary, content) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			rel, hash, s.Size, s.ModTime.UnixNano(), int64(s.Mode), lines, boolInt(binary), content)
	} else {
		_, err = tx.ExecContext(ctx,
			`UPDATE files SET hash = ?, size = ?, mtime = ?, mode = ?, lines = ?, binary = ?, content = ? WHERE id = ?`,
			hash, s.Size, s.ModTime.UnixNano(), int64(s.Mode), lines, boolInt(binary), content, id)
	}
	return u, err
}

// RemoveFile drops rel from the index. Op is Unchanged when it was not indexed.
func (d *DB) RemoveFile(ctx context.Context, rel string) (Update, error) {
	var u Update
	err := inTx(ctx, d.SQL, func(tx *sql.Tx) error {
		var err error
		u, err = removeTx(ctx, tx, rel)
		return err
	})
	if err != nil {
		return Update{}, fmt.Errorf("unindex %s: %w", rel, err)
	}
	return u, nil
}

func removeTx(ctx context.Context, tx *sql.Tx, rel string) (Update, error) {
	u := Update{Path: rel}
	var bin int
	err := tx.QueryRowContext(ctx, `DELETE FROM files WHERE path = ? RETURNING hash, binary, content`, rel).Scan(&u.OldHash, &bin, &u.Old)
	if errors.Is(err, sql.ErrNoRows) {
		return u, nil
	}
	if err != nil {
		return u, err
	}
	u.Op, u.Binary, u.OldBinary = Removed, bin != 0, bin != 0
	return u, nil
}

// File returns the indexed row for rel; ok is false when it is not indexed.
func (d *DB) File(ctx context.Context, rel string) (fi FileInfo, ok bool, err error) {
	var mtime, mode int64
	var bin int
	err = d.SQL.QueryRowContext(ctx,
		`SELECT path, hash, size, mtime, mode, lines, binary, content FROM files WHERE path = ?`, rel).
		Scan(&fi.Path, &fi.Hash, &fi.Size, &mtime, &mode, &fi.Lines, &bin, &fi.Content)
	if errors.Is(err, sql.ErrNoRows) {
		return FileInfo{}, false, nil
	}
	if err != nil {
		return FileInfo{}, false, err
	}
	fi.ModTime, fi.Mode, fi.Binary = time.Unix(0, mtime), fs.FileMode(mode), bin != 0
	return fi, true, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
