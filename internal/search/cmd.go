package search

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/ignore"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
)

func init() { cli.Register(Command) }

// Command is `lino search`.
var Command = &cli.Command{
	Name:    "search",
	Usage:   "<query> [--regex | --words] [--path P ...] [-k N] [-C N]",
	Summary: "find lines containing a string, from the index",
	Accept:  cli.File,
	MinArgs: 1,
	MaxArgs: -1,
	Setup: func(fs *flag.FlagSet) cli.RunFunc {
		words := fs.Bool("words", false, "rank files by BM25 over word tokens")
		regex := fs.Bool("regex", false, "match lines with a Go regexp")
		var paths cli.StringList
		fs.Var(&paths, "path", "limit to paths matching this glob (repeatable)")
		k := fs.Int("k", 0, "maximum hits")
		ctxLines := fs.Int("C", 0, "context lines around each hit")
		anchors := fs.Bool("anchors", false, "not supported; read the hit lines with --anchors")
		return func(ctx context.Context, c *cli.Call) (output.Result, error) {
			if *anchors {
				return output.Result{}, outcome.New(outcome.Usage, "search has no --anchors").
					WithHint("lino read <file> --lines A:B --anchors")
			}
			query, err := joinQuery(c.Args, *words)
			if err != nil {
				return output.Result{}, err
			}
			return Search(ctx, c.Cwd, Request{Query: query, Words: *words, Regex: *regex,
				Paths: paths, K: *k, Context: *ctxLines})
		}
	},
}

// joinQuery builds the query from positional args: --words joins them with
// spaces; literal and --regex take exactly one.
func joinQuery(args []string, words bool) (string, error) {
	if words || len(args) == 1 {
		return strings.Join(args, " "), nil
	}
	return "", outcome.New(outcome.Usage, "search: too many arguments").
		WithHint(`quote a multi-word query: lino search "a b"`)
}

// Request is one search call.
type Request struct {
	Query   string
	Words   bool     // --words: BM25 ranking instead of a literal match
	Regex   bool     // --regex: Go regexp, verified per line
	Paths   []string // --path globs, relative to the caller's directory
	K       int      // -k: maximum hits; 0 = config default
	Context int      // -C: context lines
}

// MaxContext bounds -C so output stays bounded.
const MaxContext = 10

// Data is the search result as printed.
type Data struct {
	Query string `json:"query"`
	Result
}

// WriteText prints hits grouped by file, indentation dropped, then the footer.
func (d Data) WriteText(w io.Writer) error {
	if err := writeHits(w, d.Hits); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w, d.Footer())
	return err
}

// Footer is "N hits in M files (index)".
func (d Data) Footer() string {
	return fmt.Sprintf("%s in %s (%s)", plural(len(d.Hits), "hit"), plural(d.Files, "file"), d.Source)
}

func plural(n int, s string) string {
	if n == 1 {
		return "1 " + s
	}
	return fmt.Sprintf("%d %ss", n, s)
}

// Search runs a literal search in the workspace found from cwd.
func Search(ctx context.Context, cwd string, req Request) (output.Result, error) {
	if req.Query == "" {
		return output.Result{}, outcome.New(outcome.Usage, "empty query")
	}
	ws, err := filecmd.Open(cwd)
	if err != nil {
		return output.Result{}, err
	}
	db, err := OpenIndex(ctx, ws)
	if err != nil {
		return output.Result{}, err
	}
	defer db.Close()
	opt, err := searchOptions(ws, cwd, req)
	if err != nil {
		return output.Result{}, err
	}
	if req.Words && req.Regex {
		return output.Result{}, outcome.New(outcome.Usage, "--regex and --words are exclusive")
	}
	if req.Regex {
		if _, err := CompileRegex(req.Query); err != nil {
			return output.Result{}, err
		}
	}
	find := Literal
	switch {
	case req.Words:
		find = Words
	case req.Regex:
		find = Regex
	}
	r, err := Fresh(ctx, db, ws, func() (Result, error) {
		return find(ctx, db, req.Query, opt)
	})
	if err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
	}
	res := Render(req.Query, r, ws.Config.LineChars)
	if r.More {
		res.Hint = truncHint(req, opt.Limit, ws.Config.SearchMaxHits)
	}
	return res, nil
}

// Render turns a Result into command output: lines cut at lineChars, empty
// outcome on no hits, truncated when more hits exist.
func Render(query string, r Result, lineChars int) output.Result {
	for i := range r.Hits {
		h := &r.Hits[i]
		h.Text = Cut(h.Text, lineChars)
		for j := range h.Before {
			h.Before[j].Text = Cut(h.Before[j].Text, lineChars)
		}
		for j := range h.After {
			h.After[j].Text = Cut(h.After[j].Text, lineChars)
		}
	}
	res := output.Result{Data: Data{Query: query, Result: r}}
	switch {
	case len(r.Hits) == 0:
		res.Outcome = outcome.Empty
		res.Message = "no hits"
	case r.More:
		res.Outcome = outcome.Truncated
		res.Message = fmt.Sprintf("truncated at %d hits", len(r.Hits))
		res.Hint = "narrow the query or raise -k"
	}
	if r.Note != "" {
		msg := r.Note
		if res.Message != "" {
			msg = res.Message + "; " + msg
		}
		res.Message = msg
	}
	return res
}

// OpenIndex opens the workspace index, building it with a full reconcile when
// it was just created.
func OpenIndex(ctx context.Context, ws *filecmd.Workspace) (*index.DB, error) {
	db, err := index.Open(ctx, ws.Root.Path())
	if err != nil {
		return nil, outcome.Wrap(outcome.Internal, err, "")
	}
	if db.Rebuilt {
		rules, err := ignore.NewRules(ws.Root.Path())
		if err == nil {
			_, err = db.Reconcile(ctx, rules, ws.Config.MaxFileSize)
		}
		if err != nil {
			db.Close()
			return nil, outcome.Wrap(outcome.Internal, err, "")
		}
	}
	return db, nil
}
