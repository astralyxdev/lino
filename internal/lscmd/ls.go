// Package lscmd implements `lino ls`: the indexed tree with line counts and
// sizes, so a model can choose what to open without reading anything.
package lscmd

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/ignore"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
)

func init() { cli.Register(Command) }

// Command is `lino ls`.
var Command = &cli.Command{
	Name:    "ls",
	Usage:   "[path] [--depth N]",
	Summary: "list indexed files with line counts and sizes",
	Accept:  cli.File,
	MinArgs: 0,
	MaxArgs: 1,
	Setup: func(fs *flag.FlagSet) cli.RunFunc {
		depth := fs.Int("depth", 0, "levels below path to list (0 = all)")
		return func(ctx context.Context, c *cli.Call) (output.Result, error) {
			return Run(ctx, c.Cwd, Request{Path: c.Arg(0), Depth: *depth})
		}
	},
}

// Request is one ls call. Depth 0 lists everything below Path.
type Request struct {
	Path  string
	Depth int
}

// Entry is one listed file or directory. Path is root-relative; Depth is 1
// for direct children of the listed path. Files is set for directories only.
type Entry struct {
	Path   string `json:"path"`
	Depth  int    `json:"depth"`
	Dir    bool   `json:"dir,omitempty"`
	Files  int    `json:"files,omitempty"`
	Lines  int    `json:"lines"`
	Size   int64  `json:"size"`
	Binary bool   `json:"binary,omitempty"`
}

// Data is the result of ls: the listed path with its totals, then its
// entries in tree order. Total counts entries before truncation.
type Data struct {
	Path    string  `json:"path"`
	Dir     bool    `json:"dir"`
	Files   int     `json:"files"`
	Lines   int     `json:"lines"`
	Size    int64   `json:"size"`
	Binary  bool    `json:"binary,omitempty"`
	Entries []Entry `json:"entries"`
	Total   int     `json:"total"`
}

// WriteText prints the listed path, then one indented line per entry.
func (d Data) WriteText(w io.Writer) error {
	head := d.Path
	if d.Dir {
		head = dirName(head)
	}
	if _, err := io.WriteString(w, line(head, d.Dir, d.Files, d.Lines, d.Size, d.Binary)); err != nil {
		return err
	}
	for _, e := range d.Entries {
		name := e.Path[strings.LastIndexByte(e.Path, '/')+1:]
		if e.Dir {
			name += "/"
		}
		s := strings.Repeat("  ", e.Depth) + line(name, e.Dir, e.Files, e.Lines, e.Size, e.Binary)
		if _, err := io.WriteString(w, s); err != nil {
			return err
		}
	}
	return nil
}

func dirName(p string) string {
	if p == "." {
		return "./"
	}
	return p + "/"
}

func line(name string, dir bool, files, lines int, size int64, binary bool) string {
	var b strings.Builder
	b.WriteString(name)
	b.WriteString("  ")
	switch {
	case dir:
		b.WriteString(plural(files, "file"))
		b.WriteString("  ")
		b.WriteString(plural(lines, "line"))
	case binary:
		b.WriteString("binary")
	default:
		b.WriteString(plural(lines, "line"))
	}
	b.WriteString("  ")
	b.WriteString(FormatSize(size))
	b.WriteByte('\n')
	return b.String()
}

func plural(n int, word string) string {
	s := strconv.Itoa(n) + " " + word
	if n != 1 {
		s += "s"
	}
	return s
}

// FormatSize renders a byte count compactly: 310B, 7.6K, 312K, 1.2M.
func FormatSize(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + "B"
	}
	f := float64(n)
	for _, u := range []string{"K", "M", "G", "T"} {
		f /= 1024
		if f < 1024 || u == "T" {
			if f < 10 {
				return strconv.FormatFloat(f, 'f', 1, 64) + u
			}
			return strconv.FormatFloat(f, 'f', 0, 64) + u
		}
	}
	panic("unreachable")
}

// Run lists the tree under req.Path (relative to cwd) from the index of the
// root found from cwd. A missing index is built first.
func Run(ctx context.Context, cwd string, req Request) (output.Result, error) {
	if req.Depth < 0 {
		return output.Result{}, outcome.New(outcome.Usage, "--depth must be >= 0")
	}
	ws, err := filecmd.Open(cwd)
	if err != nil {
		return output.Result{}, err
	}
	rel, abs, err := ws.Root.Jail(cwd, req.Path)
	if err != nil {
		return output.Result{}, err
	}
	db, err := openIndex(ctx, ws)
	if err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
	}
	defer db.Close()
	rows, err := query(ctx, db.SQL, rel)
	if err == nil && !index.Watched(ctx) {
		rows, err = refresh(ctx, db, ws, rel, rows)
	}
	if err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
	}
	if len(rows) == 0 {
		if st, err := os.Stat(abs); err == nil {
			msg := "no indexed files under " + dirName(rel)
			if rules, err := ignore.NewRules(ws.Root.Path()); err == nil && rel != "." && rules.Ignored(rel, st.IsDir()) {
				msg = rel + " is ignored (.gitignore or .linoignore)"
			} else if !st.IsDir() {
				msg = rel + " is not indexed"
			}
			res := output.Result{Outcome: outcome.Empty, Message: msg}
			if st.IsDir() {
				res.Data = Data{Path: rel, Dir: true, Entries: []Entry{}}
			}
			return res, nil
		}
		return output.Result{}, outcome.New(outcome.NotFound, "%s is not an indexed file or directory", rel)
	}
	return List(rel, rows, req.Depth, ws.Config.LsEntries), nil
}

func openIndex(ctx context.Context, ws *filecmd.Workspace) (*index.DB, error) {
	root := ws.Root.Path()
	db, err := index.Open(ctx, root)
	if err != nil {
		return nil, err
	}
	if db.Rebuilt {
		rules, err := ignore.NewRules(root)
		if err == nil {
			_, err = db.Reconcile(ctx, rules, ws.Config.MaxFileSize)
		}
		if err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

// File is one indexed file as ls needs it.
type File struct {
	Path   string
	Lines  int
	Size   int64
	Binary bool
}

// query returns the indexed files at rel or below it, sorted by path.
func query(ctx context.Context, db *sql.DB, rel string) ([]File, error) {
	q := `SELECT path, lines, size, binary FROM files`
	var args []any
	if rel != "." {
		// '0' is the byte after '/', so the range is exactly rel's subtree.
		q += ` WHERE path = ? OR (path >= ? AND path < ?)`
		args = []any{rel, rel + "/", rel + "0"}
	}
	rows, err := db.QueryContext(ctx, q+` ORDER BY path`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fs []File
	for rows.Next() {
		var f File
		var bin int
		if err := rows.Scan(&f.Path, &f.Lines, &f.Size, &bin); err != nil {
			return nil, err
		}
		f.Binary = bin != 0
		fs = append(fs, f)
	}
	return fs, rows.Err()
}

type node struct {
	entry    Entry
	children map[string]*node
}

// List builds the listing of rel from its files (rel itself, or files below
// it). depth 0 is unlimited; limit > 0 caps the number of entries.
func List(rel string, files []File, depth, limit int) output.Result {
	if len(files) == 1 && files[0].Path == rel {
		f := files[0]
		return output.Result{Data: Data{Path: rel, Files: 1, Lines: f.Lines, Size: f.Size, Binary: f.Binary, Entries: []Entry{}}}
	}
	top := &node{entry: Entry{Path: rel, Dir: true}, children: map[string]*node{}}
	prefix := rel + "/"
	if rel == "." {
		prefix = ""
	}
	for _, f := range files {
		if f.Path == rel {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(f.Path, prefix), "/")
		n := top
		n.add(f)
		for i, p := range parts {
			c := n.children[p]
			if c == nil {
				c = &node{entry: Entry{Path: joinPath(n.entry.Path, p), Depth: i + 1}}
				if i < len(parts)-1 {
					c.entry.Dir = true
					c.children = map[string]*node{}
				}
				n.children[p] = c
			}
			if c.entry.Dir {
				c.add(f)
			} else {
				c.entry.Lines, c.entry.Size, c.entry.Binary = f.Lines, f.Size, f.Binary
			}
			n = c
		}
	}
	d := Data{Path: rel, Dir: true, Files: top.entry.Files, Lines: top.entry.Lines, Size: top.entry.Size, Entries: []Entry{}}
	perDepth := map[int]int{}
	top.walk(func(e Entry) bool {
		if depth > 0 && e.Depth > depth {
			return false
		}
		d.Total++
		perDepth[e.Depth]++
		if limit <= 0 || len(d.Entries) < limit {
			d.Entries = append(d.Entries, e)
		}
		return true
	})
	res := output.Result{Data: d}
	if limit > 0 && d.Total > limit {
		res.Outcome = outcome.Truncated
		res.Message = fmt.Sprintf("truncated at %d of %d entries", limit, d.Total)
		res.Hint = hint(rel, top, perDepth, limit)
	}
	return res
}

func (n *node) add(f File) {
	n.entry.Files++
	n.entry.Lines += f.Lines
	n.entry.Size += f.Size
}

// walk visits entries depth-first in name order; fn returning false skips
// the entry's children.
func (n *node) walk(fn func(Entry) bool) {
	names := make([]string, 0, len(n.children))
	for k := range n.children {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		c := n.children[k]
		if fn(c.entry) && c.entry.Dir {
			c.walk(fn)
		}
	}
}

// hint suggests the deepest --depth that fits the limit, or, when even the
// direct children do not fit, listing the largest subdirectory.
func hint(rel string, top *node, perDepth map[int]int, limit int) string {
	best, sum := 0, 0
	for d := 1; perDepth[d] > 0; d++ {
		sum += perDepth[d]
		if sum > limit {
			break
		}
		best = d
	}
	if best > 0 {
		if rel == "." {
			return fmt.Sprintf("lino ls --depth %d", best)
		}
		return fmt.Sprintf("lino ls %s --depth %d", rel, best)
	}
	var big *node
	for _, c := range top.children {
		if c.entry.Dir && (big == nil || c.entry.Files > big.entry.Files ||
			c.entry.Files == big.entry.Files && c.entry.Path < big.entry.Path) {
			big = c
		}
	}
	if big == nil {
		return "narrow the path"
	}
	return fmt.Sprintf("lino ls %s --depth 1", big.entry.Path)
}

func joinPath(dir, name string) string {
	if dir == "." {
		return name
	}
	return dir + "/" + name
}
