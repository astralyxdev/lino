package history

import (
	"context"
	"fmt"
	"slices"

	"github.com/astralyx/lino/internal/linediff"
	"github.com/astralyx/lino/internal/textfile"
	fver "github.com/astralyx/lino/internal/version"
)

// CurrentFunc returns the current content of a text file; ok is false when
// no file exists at path. A binary file should report ErrNotFound.
type CurrentFunc func(ctx context.Context, path string) (content []byte, ok bool, err error)

// Version is a reconstructed file version.
type Version struct {
	Path    string // where the file was at that version (differs after a mv)
	Content []byte // bytes as they were on disk
	Doc     *textfile.Doc
	// Next is the id of the oldest change undone to reach the version, i.e.
	// the first change made after it; 0 when the version is current.
	Next int64
}

// pageSize is how many changes Reconstruct loads per query.
const pageSize = 64

// state is one file as the walk back through history sees it.
type state struct {
	path   string
	exists bool
	doc    *textfile.Doc
}

// Reconstruct returns the content path had at version v: starting from its
// current content, it undoes later changes newest first until a change's
// version before is v, following mv chains back to the source path. An
// unknown or pruned version, or one behind a binary change, is ErrNotFound.
func (s *Store) Reconstruct(ctx context.Context, cur CurrentFunc, path, v string) (Version, error) {
	b, ok, err := cur(ctx, path)
	if err != nil {
		return Version{}, err
	}
	st := state{path: path, exists: ok}
	if ok {
		st.doc = textfile.Parse(b)
		if fver.Of(b) == v {
			return Version{Path: path, Content: b, Doc: st.doc}, nil
		}
	}
	point := int64(0)
	for {
		cs, err := s.List(ctx, Filter{Path: st.path, Before: point, Limit: pageSize})
		if err != nil {
			return Version{}, err
		}
		if len(cs) == 0 {
			return Version{}, ErrNotFound
		}
		moved := false
		for _, c := range cs {
			point = c.ID
			from := st.path
			if st, err = s.undo(ctx, cur, st, c, true); err != nil {
				return Version{}, err
			}
			if st.exists && c.VBefore == v {
				if b, ok := match(st.doc, v); ok {
					return Version{Path: st.path, Content: b, Doc: st.doc, Next: c.ID}, nil
				}
			}
			if st.path != from {
				moved = true
				break
			}
		}
		if !moved && len(cs) < pageSize {
			return Version{}, ErrNotFound
		}
	}
}

func current(ctx context.Context, cur CurrentFunc, path string) (state, error) {
	b, ok, err := cur(ctx, path)
	if err != nil || !ok {
		return state{path: path}, err
	}
	return state{path: path, exists: true, doc: textfile.Parse(b)}, nil
}

// at returns path as it was right after change id.
func (s *Store) at(ctx context.Context, cur CurrentFunc, path string, id int64) (state, error) {
	st, err := current(ctx, cur, path)
	if err != nil {
		return st, err
	}
	cs, err := s.List(ctx, Filter{Path: path, Since: id})
	if err != nil {
		return st, err
	}
	for _, c := range cs {
		if st, err = s.undo(ctx, cur, st, c, false); err != nil {
			return st, err
		}
	}
	return st, nil
}

// undo returns st as it was before c. With follow, undoing a move into
// st.path continues at the move's source (the same file); otherwise st.path
// simply did not exist before the move.
func (s *Store) undo(ctx context.Context, cur CurrentFunc, st state, c Change, follow bool) (state, error) {
	broken := func(why string) (state, error) {
		return st, fmt.Errorf("%w: change %d on %s: %s", ErrNotFound, c.ID, c.Path, why)
	}
	if c.Extra.From != "" { // lino mv or external move: Path is the target
		if c.Path == st.path {
			if !st.exists {
				return broken("moved file missing")
			}
			if follow {
				st.path = c.Extra.From
				return st, nil
			}
			return state{path: st.path}, nil
		}
		// st.path is the source: before the move it held what the target
		// held right after it.
		prev, err := s.at(ctx, cur, c.Path, c.ID)
		if err != nil {
			return st, err
		}
		prev.path = st.path
		return prev, nil
	}
	if c.Extra.Binary || c.Extra.Symlink {
		return broken("binary content not recorded")
	}
	if c.Extra.Removed {
		if st.exists {
			return broken("removed file exists")
		}
		st.exists = true
		st.doc = &textfile.Doc{Format: textfile.Format{BOM: c.Extra.BOM, FinalNewline: !c.Extra.NoFinalNewline}}
		if c.Extra.CRLF {
			st.doc.Format.EOL = textfile.CRLF
		}
	} else if !st.exists {
		return broken("file missing")
	}
	frags, err := s.Fragments(ctx, c.ID)
	if err != nil {
		return st, err
	}
	d, err := unapply(st.doc, frags)
	if err != nil {
		return broken(err.Error())
	}
	st.doc = d
	if c.Extra.Created {
		return state{path: st.path}, nil
	}
	return st, nil
}

// unapply undoes frags on a copy of d, keeping each untouched line's ending.
func unapply(d *textfile.Doc, frags []linediff.Fragment) (*textfile.Doc, error) {
	out := &textfile.Doc{}
	*out = *d
	out.Lines = slices.Clone(d.Lines)
	rev := linediff.Reverse(frags)
	for i := len(rev) - 1; i >= 0; i-- {
		f := rev[i]
		if f.Pos < 0 || f.Pos+len(f.Old) > len(out.Lines) || !slices.Equal(out.Lines[f.Pos:f.Pos+len(f.Old)], f.Old) {
			return nil, fmt.Errorf("%w at line %d", linediff.ErrMismatch, f.Pos)
		}
		if i > 0 && rev[i-1].Pos+len(rev[i-1].Old) > f.Pos {
			return nil, fmt.Errorf("%w: overlapping fragments", linediff.ErrMismatch)
		}
		out.Replace(f.Pos, len(f.Old), f.New)
	}
	return out, nil
}

// match renders d and reports whether it has version v. Changes to the final
// newline, BOM or line ending style leave no fragments, so the other styles
// are tried too; on a match d takes that style.
func match(d *textfile.Doc, v string) ([]byte, bool) {
	if b := d.Bytes(); fver.Of(b) == v {
		return b, true
	}
	own := d.Format
	for _, eol := range []textfile.EOL{textfile.LF, textfile.CRLF} {
		for _, bom := range []bool{false, true} {
			for _, fnl := range []bool{true, false} {
				f := textfile.Format{EOL: eol, BOM: bom, FinalNewline: fnl}
				if f == own {
					continue
				}
				var b []byte
				if eol == own.EOL {
					d.Format = f
					b = d.Bytes()
				} else {
					b = textfile.Join(d.Lines, f)
				}
				if fver.Of(b) == v {
					if eol != own.EOL {
						*d = *textfile.Parse(b)
					}
					return b, true
				}
				d.Format = own
			}
		}
	}
	return nil, false
}
