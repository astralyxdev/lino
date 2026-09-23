// Package vcheck is the range-aware --v check of line mutations. It registers
// itself as filecmd.Check on import.
//
// A mutation based on version V is accepted when the file is still at V, or
// when V is in history and the lines the mutation targets are identical in V
// and in the current file. For ops addressed by line numbers of V (edit,
// delete, insert --before/--after), the addressed range of V must map through
// the V→current diff, untouched, onto exactly the resolved target: plain line
// numbers that no longer point at the same lines conflict, relocated anchors
// that do are accepted. For content-addressed ops (replace) the target must
// be untouched by that diff. Insertions at the start or end target no lines.
// An unknown or pruned V accepts only an exact match.
package vcheck

import (
	"context"
	"path/filepath"

	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/fileio"
	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/histrec"
	"github.com/astralyx/lino/internal/linediff"
	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/paths"
	"github.com/astralyx/lino/internal/reindex"
	"github.com/astralyx/lino/internal/version"
)

func init() { filecmd.Check = Check }

// Check is a mutate.VersionCheck backed by the root's history.
func Check(ctx context.Context, f *fileio.File, v string, op mutate.Op, target mutate.Range) error {
	if version.Of(f.Data) == v {
		return nil
	}
	old, ok := lookup(ctx, f, v)
	if !ok {
		return mutate.Conflict(f, v, target)
	}
	return Compare(f, v, old, op, target)
}

// Compare decides the check for f against old, the lines of f at version v.
func Compare(f *fileio.File, v string, old []string, op mutate.Op, target mutate.Range) error {
	cur := f.Lines()
	if a, ok := op.(mutate.Addresser); ok {
		if r, ok := a.Addressed(); ok {
			if r.Start < 1 || r.End > len(old) || r.End < r.Start-1 {
				return mutate.Conflict(f, v, target)
			}
			s, e, hit := history.MapFragments(linediff.Diff(old, cur), r.Start, r.End)
			if !hit && s == target.Start && e == target.End {
				return nil
			}
			return mutate.Conflict(f, v, mutate.Range{Start: s, End: e})
		}
	}
	if target.Empty() {
		return nil
	}
	if _, _, hit := history.MapFragments(linediff.Diff(cur, old), target.Start, target.End); !hit {
		return nil
	}
	return mutate.Conflict(f, v, target)
}

// lookup returns the lines of f at version v from the root's history.
func lookup(ctx context.Context, f *fileio.File, v string) ([]string, bool) {
	root, ok := findRoot(f.Real)
	if !ok {
		return nil, false
	}
	t, release, err := reindex.Open(ctx, root)
	if err != nil || t == nil {
		return nil, false
	}
	defer release()
	ver, err := histrec.Reconstruct(ctx, t.DB, f.Path, v)
	if err != nil || ver.Doc == nil {
		return nil, false
	}
	return ver.Doc.Lines, true
}

func findRoot(p string) (string, bool) {
	for dir := filepath.Dir(p); ; {
		if paths.IsRoot(dir) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
