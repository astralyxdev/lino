// Command ftsbuild is the milestone 2 spike: it builds an FTS5 trigram and
// words index over a source tree with modernc.org/sqlite and reports build
// time, database size and peak RSS. One layout per run, so RSS is clean.
package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

type file struct {
	path    string
	content string
	lines   int
}

type report struct {
	Layout      string  `json:"layout"`
	Detail      string  `json:"detail"`
	ChunkLines  int     `json:"chunk_lines,omitempty"`
	Files       int     `json:"files"`
	Lines       int     `json:"lines"`
	SourceBytes int64   `json:"source_bytes"`
	Rows        int     `json:"rows"`
	LoadSec     float64 `json:"load_sec"`
	InsertSec   float64 `json:"insert_sec"`
	OptimizeSec float64 `json:"optimize_sec"`
	TotalSec    float64 `json:"total_sec"`
	DBBytes     int64   `json:"db_bytes"`
	PeakRSS     int64   `json:"peak_rss_bytes"`
}

func main() {
	src := flag.String("src", filepath.Join(runtime.GOROOT(), "src"), "corpus root")
	maxLines := flag.Int("maxlines", 1_000_000, "stop adding files once this many lines are loaded")
	db := flag.String("db", "spike.db", "output database (overwritten)")
	layout := flag.String("layout", "file", "row layout: file | chunk")
	chunk := flag.Int("chunk", 64, "lines per row for -layout chunk")
	detail := flag.String("detail", "full", "trigram FTS5 detail: full | column | none")
	flag.Parse()

	if err := run(*src, *maxLines, *db, *layout, *chunk, *detail); err != nil {
		fmt.Fprintln(os.Stderr, "ftsbuild:", err)
		os.Exit(1)
	}
}

func run(src string, maxLines int, dbPath, layout string, chunk int, detail string) error {
	if layout != "file" && layout != "chunk" {
		return fmt.Errorf("unknown layout %q", layout)
	}
	start := time.Now()
	files, err := load(src, maxLines)
	if err != nil {
		return err
	}
	rep := report{Layout: layout, Detail: detail, Files: len(files)}
	if layout == "chunk" {
		rep.ChunkLines = chunk
	}
	for _, f := range files {
		rep.Lines += f.lines
		rep.SourceBytes += int64(len(f.content))
	}
	rep.LoadSec = time.Since(start).Seconds()

	for _, suf := range []string{"", "-wal", "-shm"} {
		os.Remove(dbPath + suf)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	for _, s := range schema(layout, detail) {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}

	t := time.Now()
	rows, err := insert(db, files, layout, chunk)
	if err != nil {
		return err
	}
	rep.Rows = rows
	rep.InsertSec = time.Since(t).Seconds()

	t = time.Now()
	for _, s := range []string{
		"INSERT INTO tri(tri) VALUES('optimize')",
		"INSERT INTO words(words) VALUES('optimize')",
		"PRAGMA wal_checkpoint(TRUNCATE)",
	} {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	rep.OptimizeSec = time.Since(t).Seconds()
	rep.TotalSec = time.Since(start).Seconds()

	for _, suf := range []string{"", "-wal"} {
		if st, err := os.Stat(dbPath + suf); err == nil {
			rep.DBBytes += st.Size()
		}
	}
	rep.PeakRSS = peakRSS()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

func schema(layout, detail string) []string {
	s := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=-65536",
		"CREATE TABLE files(id INTEGER PRIMARY KEY, path TEXT UNIQUE NOT NULL, content TEXT NOT NULL)",
	}
	table := "files"
	if layout == "chunk" {
		s = append(s, "CREATE TABLE chunks(id INTEGER PRIMARY KEY, file_id INTEGER NOT NULL, start_line INTEGER NOT NULL, content TEXT NOT NULL)")
		table = "chunks"
	}
	return append(s,
		fmt.Sprintf("CREATE VIRTUAL TABLE tri USING fts5(content, content='%s', content_rowid='id', tokenize='trigram', detail=%s)", table, detail),
		fmt.Sprintf("CREATE VIRTUAL TABLE words USING fts5(content, content='%s', content_rowid='id', tokenize='unicode61')", table),
	)
}

func insert(db *sql.DB, files []file, layout string, chunk int) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	insFile, err := tx.Prepare("INSERT INTO files(id, path, content) VALUES(?, ?, ?)")
	if err != nil {
		return 0, err
	}
	insChunk, err := tx.Prepare("INSERT INTO chunks(id, file_id, start_line, content) VALUES(?, ?, ?, ?)")
	if err != nil && layout == "chunk" {
		return 0, err
	}
	insTri, err := tx.Prepare("INSERT INTO tri(rowid, content) VALUES(?, ?)")
	if err != nil {
		return 0, err
	}
	insWords, err := tx.Prepare("INSERT INTO words(rowid, content) VALUES(?, ?)")
	if err != nil {
		return 0, err
	}

	rows := 0
	for i, f := range files {
		id := int64(i + 1)
		if _, err := insFile.Exec(id, f.path, f.content); err != nil {
			return 0, err
		}
		if layout == "file" {
			if err := indexRow(insTri, insWords, id, f.content); err != nil {
				return 0, err
			}
			rows++
			continue
		}
		lines := strings.SplitAfter(f.content, "\n")
		for s := 0; s < len(lines); s += chunk {
			e := min(s+chunk, len(lines))
			text := strings.Join(lines[s:e], "")
			if text == "" {
				continue
			}
			rows++
			if _, err := insChunk.Exec(rows, id, s+1, text); err != nil {
				return 0, err
			}
			if err := indexRow(insTri, insWords, int64(rows), text); err != nil {
				return 0, err
			}
		}
	}
	return rows, tx.Commit()
}

func indexRow(tri, words *sql.Stmt, id int64, text string) error {
	if _, err := tri.Exec(id, text); err != nil {
		return err
	}
	_, err := words.Exec(id, text)
	return err
}

// load walks src in lexical order and returns UTF-8 text files until maxLines
// is reached. Hidden directories and testdata are skipped.
func load(src string, maxLines int) ([]file, error) {
	var paths []string
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != src && (strings.HasPrefix(name, ".") || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	var files []file
	total := 0
	for _, p := range paths {
		if total >= maxLines {
			break
		}
		b, err := os.ReadFile(p)
		if err != nil || len(b) > 4<<20 || bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
			continue
		}
		rel, _ := filepath.Rel(src, p)
		n := bytes.Count(b, []byte{'\n'})
		if len(b) > 0 && b[len(b)-1] != '\n' {
			n++
		}
		files = append(files, file{path: filepath.ToSlash(rel), content: string(b), lines: n})
		total += n
	}
	return files, nil
}

func peakRSS() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss)
	}
	return int64(ru.Maxrss) * 1024
}
