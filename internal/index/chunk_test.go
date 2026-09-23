package index

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
)

func TestChangedLines(t *testing.T) {
	tests := []struct {
		name, old, cur string
		lo, hi         int
	}{
		{"same", "a\nb\nc\n", "a\nb\nc\n", 3, 3},
		{"middle", "a\nb\nc\n", "a\nx\nc\n", 1, 2},
		{"insert", "a\nc\n", "a\nb\nc\n", 1, 1},
		{"delete", "a\nb\nc\n", "a\nc\n", 1, 2},
		{"append", "a\n", "a\nb\n", 1, 1},
		{"no final newline", "a\nb", "a\nbc", 1, 2},
		{"add final newline", "a\nb", "a\nb\n", 1, 2},
		{"repeated lines", "a\na\na\n", "a\na\n", 2, 3},
		{"all", "a\n", "b\n", 0, 1},
		{"to empty", "a\nb\n", "", 0, 2},
		{"partial suffix line", "xa\n", "ya\n", 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lo, hi := changedLines(tt.old, tt.cur, lineStarts(tt.old))
			if lo != tt.lo || hi != tt.hi {
				t.Errorf("changedLines = [%d, %d), want [%d, %d)", lo, hi, tt.lo, tt.hi)
			}
		})
	}
}

// TestChunksFollowEdits applies random line edits and checks after each one
// that the chunks tile the file and that every line is found in its chunk,
// while text only removed lines had is found nowhere.
func TestChunksFollowEdits(t *testing.T) {
	for _, size := range []int{1, 3, 8} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			saved := ChunkLines
			ChunkLines = size
			t.Cleanup(func() { ChunkLines = saved })
			ctx := context.Background()
			db := mustOpen(t, newRoot(t))
			defer db.Close()
			rng := rand.New(rand.NewSource(int64(size)))
			var lines []string
			next := 0
			line := func() string { next++; return fmt.Sprintf("%d:%08x", next, rng.Uint32()) }
			for range 20 {
				lines = append(lines, line())
			}
			var gone []string
			for step := range 120 {
				at := rng.Intn(len(lines) + 1)
				switch op := rng.Intn(4); {
				case op == 0 || len(lines) == 0 || at == len(lines):
					ins := make([]string, rng.Intn(2*size+2))
					for i := range ins {
						ins[i] = line()
					}
					lines = append(lines[:at], append(ins, lines[at:]...)...)
				case op == 1:
					n := min(rng.Intn(2*size+2)+1, len(lines)-at)
					gone = append(gone, lines[at:at+n]...)
					lines = append(lines[:at], lines[at+n:]...)
				default:
					gone = append(gone, lines[at])
					lines[at] = line()
				}
				content := strings.Join(lines, "\n")
				if step%2 == 0 && len(lines) > 0 {
					content += "\n"
				}
				if _, err := db.IndexData(ctx, "f.txt", []byte(content), Stat{Size: int64(len(content)), ModTime: time.Now()}); err != nil {
					t.Fatal(err)
				}
				checkChunks(t, db, lines, gone, step)
				if t.Failed() {
					return
				}
			}
		})
	}
}

func checkChunks(t *testing.T, db *DB, lines, gone []string, step int) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRow(`SELECT id FROM files WHERE path = 'f.txt'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	chunks, err := loadChunks(ctx, tx, id)
	if err != nil {
		t.Fatal(err)
	}
	at := 0
	for _, c := range chunks {
		if c.start != at || c.lines < 1 || c.lines > ChunkLines {
			t.Fatalf("step %d: chunks %v do not tile %d lines", step, chunks, len(lines))
		}
		for _, l := range lines[c.start : c.start+c.lines] {
			if n := count(t, db, `SELECT count(*) FROM tri WHERE rowid = ? AND tri MATCH ?`, c.id, allTrigrams(l)); n != 1 {
				t.Fatalf("step %d: %q not in its chunk %v", step, l, c)
			}
		}
		at += c.lines
	}
	if at != len(lines) {
		t.Fatalf("step %d: chunks cover %d lines, want %d", step, at, len(lines))
	}
	for _, l := range gone[max(0, len(gone)-20):] {
		if n := count(t, db, `SELECT count(*) FROM tri WHERE tri MATCH ?`, allTrigrams(l)); n != 0 {
			t.Fatalf("step %d: removed %q still indexed", step, l)
		}
	}
}

// allTrigrams is an FTS5 query for every trigram of s.
func allTrigrams(s string) string {
	var q []string
	for i := 0; i+3 <= len(s); i++ {
		q = append(q, `"`+strings.ToLower(s[i:i+3])+`"`)
	}
	return strings.Join(q, " AND ")
}

// TestManyChunks inserts more chunks than one statement takes.
func TestManyChunks(t *testing.T) {
	saved := ChunkLines
	ChunkLines = 1
	t.Cleanup(func() { ChunkLines = saved })
	db := mustOpen(t, newRoot(t))
	defer db.Close()
	var b strings.Builder
	for i := range 2500 {
		fmt.Fprintf(&b, "row%d\n", i)
	}
	data := []byte(b.String())
	if _, err := db.IndexData(context.Background(), "big.txt", data, Stat{Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM chunks`); n != 2500 {
		t.Errorf("chunks = %d, want 2500", n)
	}
	if n := count(t, db, `SELECT count(*) FROM tri WHERE tri MATCH ?`, allTrigrams("row2499")); n != 1 {
		t.Errorf("last row found in %d chunks, want 1", n)
	}
}
