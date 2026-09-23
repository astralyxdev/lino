package index

import (
	"context"
	"database/sql"
	"strings"
)

// ChunkLines is how many lines one trigram row covers. An edit re-tokenises
// only the chunks it touches; later chunks just shift their start line.
var ChunkLines = 256

// MatchFiles is SQL selecting the files.id of every file with a chunk
// matching the trigram expression bound to its one parameter.
const MatchFiles = `SELECT c.file_id FROM tri JOIN chunks c ON c.id = tri.rowid WHERE tri MATCH ?`

// chunk is one row of the chunks table: lines [start, start+lines) of a file,
// 0-based.
type chunk struct {
	id, start, lines int
}

// lineStarts returns the byte offset of every line of s; a final line without
// "\n" counts.
func lineStarts(s string) []int {
	offs := make([]int, 0, strings.Count(s, "\n")+1)
	for i := 0; i < len(s); {
		offs = append(offs, i)
		j := strings.IndexByte(s[i:], '\n')
		if j < 0 {
			break
		}
		i += j + 1
	}
	return offs
}

// span returns lines [from, to) of s given its line starts.
func span(s string, offs []int, from, to int) string {
	end := len(s)
	if to < len(offs) {
		end = offs[to]
	}
	return s[offs[from]:end]
}

// insertChunks tokenises lines [from, to) of content as chunks of ChunkLines,
// with one statement per table. Chunk ids grow monotonically, which keeps
// FTS5 deletes of recent chunks out of the large segments.
func insertChunks(ctx context.Context, tx *sql.Tx, fileID int64, content string, offs []int, from, to int) error {
	// Stay well under SQLite's bound variable limit.
	const perStmt = 1000
	for from < to {
		end := min(to, from+perStmt*ChunkLines)
		var args []any
		for s := from; s < end; s += ChunkLines {
			args = append(args, fileID, s, min(ChunkLines, end-s))
		}
		n := len(args) / 3
		rows, err := tx.QueryContext(ctx, `INSERT INTO chunks(file_id, start, lines) VALUES (?, ?, ?)`+
			strings.Repeat(", (?, ?, ?)", n-1)+` RETURNING id, start, lines`, args...)
		if err != nil {
			return err
		}
		texts := make([]any, 0, 2*n)
		for rows.Next() {
			var id, s, l int
			if err := rows.Scan(&id, &s, &l); err != nil {
				rows.Close()
				return err
			}
			texts = append(texts, id, span(content, offs, s, s+l))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tri(rowid, content) VALUES (?, ?)`+
			strings.Repeat(", (?, ?)", n-1), texts...); err != nil {
			return err
		}
		from = end
	}
	return nil
}

// writeChunks brings fileID's chunks from old to content, re-tokenising only
// the chunks that overlap the lines that changed. old == "" rewrites all.
func writeChunks(ctx context.Context, tx *sql.Tx, fileID int64, old, content string) error {
	newOffs := lineStarts(content)
	if old == "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE file_id = ?`, fileID); err != nil {
			return err
		}
		return insertChunks(ctx, tx, fileID, content, newOffs, 0, len(newOffs))
	}
	chunks, err := loadChunks(ctx, tx, fileID)
	if err != nil {
		return err
	}
	oldOffs := lineStarts(old)
	lo, hi := changedLines(old, content, oldOffs)
	delta := len(newOffs) - len(oldOffs)

	// The affected chunks overlap [lo, hi), or contain lo for a pure insert.
	first, last := -1, -1
	for i, c := range chunks {
		end := c.start + c.lines
		if end > lo && c.start < max(hi, lo+1) {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	from, to := lo, lo // old lines re-chunked; lo is past the last chunk when none is affected
	if first >= 0 {
		from, to = chunks[first].start, chunks[last].start+chunks[last].lines
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE file_id = ? AND start >= ? AND start < ?`,
			fileID, from, to); err != nil {
			return err
		}
	}
	if delta != 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE chunks SET start = start + ? WHERE file_id = ? AND start >= ?`,
			delta, fileID, to); err != nil {
			return err
		}
	}
	return insertChunks(ctx, tx, fileID, content, newOffs, from, to+delta)
}

func loadChunks(ctx context.Context, tx *sql.Tx, fileID int64) ([]chunk, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, start, lines FROM chunks WHERE file_id = ? ORDER BY start`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []chunk
	for rows.Next() {
		var c chunk
		if err := rows.Scan(&c.id, &c.start, &c.lines); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// changedLines returns the old lines [lo, hi) outside the common line prefix
// and suffix of old and cur; lines outside it are byte-identical in both.
func changedLines(old, cur string, oldOffs []int) (lo, hi int) {
	p := 0
	for n := min(len(old), len(cur)); p < n && old[p] == cur[p]; p++ {
	}
	s := 0
	for n := min(len(old), len(cur)) - p; s < n && old[len(old)-1-s] == cur[len(cur)-1-s]; s++ {
	}
	lo = strings.Count(old[:p], "\n")
	// The suffix covers whole lines from the first line start at or after
	// len(old)-s; the same lines end cur, so their text is unchanged.
	hi = len(oldOffs)
	for hi > lo && oldOffs[hi-1] >= len(old)-s && suffixLineStart(cur, len(cur)-(len(old)-oldOffs[hi-1])) {
		hi--
	}
	return lo, hi
}

// suffixLineStart reports whether offset i in s starts a line.
func suffixLineStart(s string, i int) bool {
	return i == 0 || (i > 0 && s[i-1] == '\n')
}
