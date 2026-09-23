package search

import (
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/ignore"
	"github.com/astralyx/lino/internal/outcome"
)

// ContextLine is a line shown around a hit by -C.
type ContextLine struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

// MatchPath reports whether the root-relative path p is selected by globs.
// No globs selects everything. A glob selects a path when it matches the path
// or one of its parent directories (so "wallet" and "wallet/**" both select
// wallet/a.go); a glob without '/' matches a single name at any depth, like
// .gitignore ("*.go"). '*' stops at '/', "**" spans directories.
func MatchPath(globs []string, p string) bool {
	if len(globs) == 0 {
		return true
	}
	for _, g := range globs {
		if matchGlob(g, p) {
			return true
		}
	}
	return false
}

func matchGlob(g, p string) bool {
	anchored := strings.Contains(g, "/")
	for s := p; s != "."; s = path.Dir(s) {
		if anchored && ignore.Glob(g, s) || !anchored && ignore.Glob(g, path.Base(s)) {
			return true
		}
		if !strings.Contains(s, "/") {
			break
		}
	}
	return false
}

// NormalizeGlobs makes --path globs root-relative for a caller whose cwd is the
// root-relative directory dir ("." or "" for the root). A leading "/" anchors a
// glob at the root; "./" and trailing "/" are dropped.
func NormalizeGlobs(globs []string, dir string) []string {
	var out []string
	for _, g := range globs {
		switch {
		case strings.HasPrefix(g, "/"):
			g = strings.TrimLeft(g, "/")
		case dir != "" && dir != ".":
			if t := strings.TrimSuffix(g, "/"); strings.Contains(t, "/") || t == "." || t == ".." {
				g = path.Join(dir, g)
			} else {
				g = dir + "/**/" + g
			}
		}
		g = strings.TrimPrefix(strings.TrimSuffix(g, "/"), "./")
		if g == "" || g == "." {
			g = "**"
		}
		out = append(out, g)
	}
	return out
}

// addContext fills Before and After of one file's hits with up to n lines
// each. Lines that are themselves hits are not repeated as context.
func addContext(content string, hits []Hit, n int) {
	if n <= 0 || len(hits) == 0 {
		return
	}
	var lines []string
	EachLine(content, func(_ int, l string) bool {
		lines = append(lines, l)
		return true
	})
	isHit := make(map[int]bool, len(hits))
	for _, h := range hits {
		isHit[h.Line] = true
	}
	for i := range hits {
		h := &hits[i]
		for l := max(1, h.Line-n); l < h.Line; l++ {
			if !isHit[l] {
				h.Before = append(h.Before, ContextLine{l, lines[l-1]})
			}
		}
		for l := h.Line + 1; l <= min(len(lines), h.Line+n); l++ {
			if !isHit[l] {
				h.After = append(h.After, ContextLine{l, lines[l-1]})
			}
		}
	}
}

// writeHits prints hits grouped by file. Hit lines are "  N  text", context
// lines "  N- text"; with context, non-adjacent groups are split by "  --".
func writeHits(w io.Writer, hits []Hit) error {
	last, prevLine := "", 0
	ctx := hasContext(hits)
	emit := func(n int, sep, text string) error {
		if prevLine > 0 && n > prevLine+1 && ctx {
			if _, err := fmt.Fprintln(w, "  --"); err != nil {
				return err
			}
		}
		if n <= prevLine {
			return nil
		}
		prevLine = n
		_, err := fmt.Fprintf(w, "  %-3s %s\n", fmt.Sprint(n)+sep, strings.TrimLeft(text, " \t"))
		return err
	}
	for _, h := range hits {
		if h.Path != last {
			if _, err := fmt.Fprintln(w, h.Path); err != nil {
				return err
			}
			last, prevLine = h.Path, 0
		}
		for _, c := range h.Before {
			if err := emit(c.Line, "-", c.Text); err != nil {
				return err
			}
		}
		if err := emit(h.Line, "", h.Text); err != nil {
			return err
		}
		for _, c := range h.After {
			if err := emit(c.Line, "-", c.Text); err != nil {
				return err
			}
		}
	}
	return nil
}

func hasContext(hits []Hit) bool {
	for _, h := range hits {
		if len(h.Before) > 0 || len(h.After) > 0 {
			return true
		}
	}
	return false
}

// searchOptions validates -k, -C and --path against the workspace config.
func searchOptions(ws *filecmd.Workspace, cwd string, req Request) (Options, error) {
	opt := Options{Limit: ws.Config.SearchHits, Context: req.Context}
	if req.K != 0 {
		if req.K < 1 || req.K > ws.Config.SearchMaxHits {
			return opt, outcome.New(outcome.Usage, "-k must be between 1 and %d", ws.Config.SearchMaxHits)
		}
		opt.Limit = req.K
	}
	if req.Context < 0 || req.Context > MaxContext {
		return opt, outcome.New(outcome.Usage, "-C must be between 0 and %d", MaxContext)
	}
	if len(req.Paths) > 0 {
		dir, _, err := ws.Root.Resolve(cwd, "")
		if err != nil {
			dir = "."
		}
		opt.Paths = NormalizeGlobs(req.Paths, dir)
	}
	return opt, nil
}

// truncHint suggests the next call after a truncated search.
func truncHint(req Request, limit, maxHits int) string {
	if limit < maxHits {
		return fmt.Sprintf("-k %d, or narrow with --path", min(limit*2, maxHits))
	}
	return "narrow the query or add --path"
}
