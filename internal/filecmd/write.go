package filecmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/astralyx/lino/internal/cli"
	"github.com/astralyx/lino/internal/fileio"
	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/textfile"
	"github.com/astralyx/lino/internal/version"
)

func init() { cli.Register(WriteCommand) }

// WriteCommand is `lino write`.
var WriteCommand = &cli.Command{
	Name:    "write",
	Usage:   "<file> [--v V | --force]",
	Summary: "create a file, or overwrite it; content from stdin",
	Accept:  cli.Mutating,
	Stdin:   true,
	MinArgs: 1,
	MaxArgs: 1,
	Setup: func(fs *flag.FlagSet) cli.RunFunc {
		v := fs.String("v", "", "version last read (required to overwrite)")
		force := fs.Bool("force", false, "overwrite without a version check")
		return func(ctx context.Context, c *cli.Call) (output.Result, error) {
			content, err := c.ReadStdin()
			if err != nil {
				return output.Result{}, err
			}
			return Write(ctx, c.Cwd, WriteRequest{Path: c.Arg(0), V: *v, Force: *force, By: c.By, Content: content})
		}
	},
}

// WriteRequest is one write call.
type WriteRequest struct {
	Path    string
	V       string
	Force   bool
	By      string
	Content []byte
}

// WriteData is the result of a write.
type WriteData struct {
	mutate.Result
	Created bool `json:"created,omitempty"`
}

// WriteText prints e.g. "updated a.go v=8c21e0→5f02aa lines 1-12".
func (d WriteData) WriteText(w io.Writer) error {
	var err error
	switch {
	case d.Created:
		_, err = fmt.Fprintf(w, "created %s v=%s lines %d\n", d.Path, d.NewV, d.Total)
	case d.Unchanged:
		_, err = fmt.Fprintf(w, "unchanged %s v=%s\n", d.Path, d.NewV)
	default:
		_, err = fmt.Fprintf(w, "updated %s v=%s→%s lines %d\n", d.Path, d.OldV, d.NewV, d.Total)
	}
	return err
}

// conflictLines caps the current lines printed when a write conflicts.
const conflictLines = 20

// Write creates or overwrites a file in the workspace found from cwd.
func Write(ctx context.Context, cwd string, req WriteRequest) (output.Result, error) {
	if req.Force && req.V != "" {
		return output.Result{}, outcome.New(outcome.Usage, "write: --v and --force are exclusive")
	}
	if req.V != "" && !version.Valid(req.V) {
		return output.Result{}, mutate.ValidateV(req.Path, req.V)
	}
	if textfile.Classify(req.Content) == textfile.Binary {
		return output.Result{}, outcome.New(outcome.Refused, "%s: content is not UTF-8 text", req.Path)
	}
	ws, err := Open(cwd)
	if err != nil {
		return output.Result{}, err
	}
	if err := textfile.TooLarge(req.Path, int64(len(req.Content)), ws.Config.MaxFileSize); err != nil {
		return output.Result{}, err
	}
	rel, real, err := ws.Root.Jail(cwd, req.Path)
	if err != nil {
		return output.Result{}, err
	}
	if rel == fileio.MetaDir || strings.HasPrefix(rel, fileio.MetaDir+"/") {
		return output.Result{}, outcome.New(outcome.Refused, "%s: inside %s/", rel, fileio.MetaDir)
	}
	if err := ws.Prepare(ctx, cwd, req.Path); err != nil {
		return output.Result{}, err
	}
	var res output.Result
	p := ws.Pipeline()
	err = p.Exclusive(func() error {
		var err error
		res, err = writeLocked(ctx, ws, p, cwd, rel, real, req)
		return err
	})
	return res, err
}

func writeLocked(ctx context.Context, ws *Workspace, p *mutate.Pipeline, cwd, rel, real string, req WriteRequest) (output.Result, error) {
	_, statErr := os.Lstat(real)
	if errors.Is(statErr, fs.ErrNotExist) {
		if req.V != "" {
			return output.Result{}, outcome.New(outcome.Conflict, "%s no longer exists (read at v=%s)", rel, req.V).
				WithHint("recreate it with lino write " + rel)
		}
		doc := &textfile.Doc{Lines: textfile.SplitInput(req.Content), Format: textfile.Format{FinalNewline: true}}
		return commitWrite(ctx, p, rel, real, nil, doc, fileio.DefaultMode, req.By, true)
	}

	f, err := ws.Load(cwd, req.Path)
	if err != nil {
		return output.Result{}, err
	}
	if !req.Force {
		if err := mutate.ValidateV(f.Path, req.V); err != nil {
			e, _ := outcome.As(err)
			e.Hint = "pass --v from lino read " + f.Path + ", or --force to overwrite"
			return output.Result{}, e
		}
		if cur := version.Of(f.Data); cur != req.V {
			return output.Result{}, mutate.Conflict(f, req.V, mutate.Range{Start: 1, End: min(len(f.Lines()), conflictLines)})
		}
	}
	if len(req.Content) == 0 {
		return output.Result{}, outcome.New(outcome.Usage, "no content on stdin").
			WithHint("pass the new content with a heredoc; use lino rm to remove the file")
	}
	format := f.Format()
	if len(f.Lines()) == 0 {
		format.FinalNewline = true
	}
	doc := &textfile.Doc{Lines: textfile.SplitInput(req.Content), Format: format}
	return commitWrite(ctx, p, f.Path, f.Real, f.Data, doc, f.Mode, req.By, false)
}

func commitWrite(ctx context.Context, p *mutate.Pipeline, rel, real string, before []byte, doc *textfile.Doc, mode fs.FileMode, by string, created bool) (output.Result, error) {
	after := doc.Bytes()
	res := mutate.Result{
		Path:    rel,
		Op:      "write",
		By:      by,
		NewV:    version.Of(after),
		Changed: mutate.Range{Start: 1, End: len(doc.Lines)},
		Total:   len(doc.Lines),
	}
	if !created {
		res.OldV = version.Of(before)
		res.Target = mutate.Range{Start: 1, End: len(textfile.Parse(before).Lines)}
	}
	data := WriteData{Result: res, Created: created}
	if !created && string(after) == string(before) {
		data.Unchanged = true
		return output.Result{Outcome: outcome.OK, Data: data}, nil
	}
	if err := fileio.WriteAtomic(real, after, mode); err != nil {
		return output.Result{}, err
	}
	o := outcome.Updated
	if created {
		o = outcome.Created
	}
	c := &mutate.Commit{Result: res, Real: real, Before: before, After: after, Doc: doc}
	if err := p.RunHooks(ctx, c); err != nil {
		return output.Result{Outcome: o, Data: data}, err
	}
	return output.Result{Outcome: o, Data: data}, nil
}
