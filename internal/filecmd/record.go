package filecmd

import (
	"context"
	"os"
	"path/filepath"

	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/textfile"
	"github.com/astralyx/lino/internal/version"
)

// RecordCreated runs Hooks for a text file lino itself created outside the
// file commands (the default .linoignore), so it is indexed and logged as a
// lino write by by rather than as an external edit. rel is root-relative.
func RecordCreated(ctx context.Context, root, rel, by string) error {
	ws, err := Open(root)
	if err != nil {
		return err
	}
	real := filepath.Join(ws.Root.Path(), filepath.FromSlash(rel))
	data, err := os.ReadFile(real)
	if err != nil {
		return err
	}
	doc := textfile.Parse(data)
	n := len(doc.Lines)
	c := &mutate.Commit{
		Result: mutate.Result{
			Path: rel, Op: "write", By: by, NewV: version.Of(data),
			Changed: mutate.Range{Start: 1, End: n}, Total: n,
		},
		Real: real, After: data, Doc: doc,
	}
	p := ws.Pipeline()
	return p.Exclusive(func() error { return p.RunHooks(ctx, c) })
}
