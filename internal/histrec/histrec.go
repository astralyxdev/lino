// Package histrec records lino's own line mutations in history.db, under the
// same id as their change log entry, so every change can be undone.
package histrec

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/astralyx/lino/internal/changelog"
	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/linediff"
	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/textfile"
)

func init() {
	changelog.Recorders = append(changelog.Recorders, Record)
	changelog.ExternalRecorders = append(changelog.ExternalRecorders, RecordExternal)
}

// Record is a changelog.Recorder: it stores the history change for c with
// id seq. Commits Build does not cover are left to other recorders.
func Record(ctx context.Context, root string, seq int64, e changelog.Entry, c *mutate.Commit) (func(), error) {
	ch, ok := Build(e, c)
	if !ok {
		return nil, nil
	}
	ch.ID = seq
	st, err := Store(ctx, root)
	if err != nil {
		return nil, err
	}
	if _, err := st.Add(ctx, ch); err != nil {
		return nil, err
	}
	return func() {
		st.SQL.ExecContext(context.WithoutCancel(ctx), `DELETE FROM changes WHERE id = ?`, seq)
	}, nil
}

// Build returns the history change for a lino mutation. Line changes and
// overwrites store the minimal fragments between old and new lines; a new file
// is one fragment adding every line; rm stores the whole removed content as
// one fragment plus its mode and line style; mv stores only the two paths.
// Binary content (and a removed symlink) is recorded by version only, with
// Extra.Binary (or Extra.Symlink) marking it as not rollbackable.
func Build(e changelog.Entry, c *mutate.Commit) (history.Change, bool) {
	ch := history.Change{
		Time:    e.Time,
		Source:  history.SourceLino,
		Author:  e.Author,
		Path:    e.Path,
		Op:      c.Op,
		VBefore: c.OldV,
		VAfter:  c.NewV,
		Extra:   history.Extra{Undid: c.Undid},
	}
	switch {
	case c.Op == history.OpMv || c.From != "":
		ch.Extra.From = e.From
		if ch.Extra.From == "" {
			ch.Extra.From = c.From
		}
		return ch, true
	case c.Op == history.OpRm || c.Removed:
		ch.VAfter = ""
		ch.Extra.Removed = true
		ch.Extra.Mode = uint32(c.Mode.Perm())
		if c.Before == nil && c.OldV == "" {
			ch.Extra.Symlink = true
			return ch, true
		}
		old, ok := textLines(c.Before, &ch.Extra)
		if ok {
			ch.Fragments = linediff.Diff(old, nil)
			setStyle(&ch.Extra, c.Before)
		}
		return ch, true
	case c.Before == nil && c.OldV == "":
		ch.Extra.Created = true
	}
	var old []string
	if !ch.Extra.Created {
		var ok bool
		if old, ok = textLines(c.Before, &ch.Extra); !ok {
			return ch, true
		}
	}
	var after []string
	switch {
	case c.Doc != nil && textfile.Classify(c.After) == textfile.Text:
		after = c.Doc.Lines
	default:
		var ok bool
		if after, ok = textLines(c.After, &ch.Extra); !ok {
			return ch, true
		}
	}
	ch.Fragments = linediff.Diff(old, after)
	return ch, true
}

// textLines returns the lines of b, or sets x.Binary and ok=false when b is
// not text.
func textLines(b []byte, x *history.Extra) ([]string, bool) {
	if textfile.Classify(b) == textfile.Binary {
		x.Binary = true
		return nil, false
	}
	return textfile.Parse(b).Lines, true
}

// setStyle records the line style of a removed file's content.
func setStyle(x *history.Extra, b []byte) {
	f := textfile.Parse(b).Format
	x.CRLF = f.EOL == textfile.CRLF
	x.BOM = f.BOM
	x.NoFinalNewline = len(b) > 0 && !f.FinalNewline
}

// RecordExternal is a changelog.ExternalRecorder: it stores the history
// change for an external edit (watcher, startup reconcile, stat refresh,
// lino index) with id seq, diffed against the content the index last knew.
func RecordExternal(ctx context.Context, db *index.DB, seq int64, e changelog.Entry, u index.Update) (func(), error) {
	var cur []byte
	curOK := false
	if u.Op == index.Added || u.Op == index.Modified {
		fi, ok, err := db.File(ctx, u.Path)
		if err == nil && ok && fi.Hash == u.Hash && !fi.Binary {
			cur, curOK = []byte(fi.Content), true
		}
	}
	ch := BuildExternal(e, u, cur, curOK)
	ch.ID = seq
	st, err := Store(ctx, filepath.Dir(filepath.Dir(db.Path)))
	if err != nil {
		return nil, err
	}
	if _, err := st.Add(ctx, ch); err != nil {
		return nil, err
	}
	return func() {
		st.SQL.ExecContext(context.WithoutCancel(ctx), `DELETE FROM changes WHERE id = ?`, seq)
	}, nil
}

// BuildExternal returns the history change for an external update: the net
// fragments from the old indexed content to cur (the new content, when
// curOK), the whole old content for a removal, the whole file for an added
// one, and only the paths for a move. Binary content, or new content that is
// no longer known (the file changed again since u), is recorded by version
// only with Extra.Binary set: not rollbackable.
func BuildExternal(e changelog.Entry, u index.Update, cur []byte, curOK bool) history.Change {
	ch := history.Change{
		Time:    e.Time,
		Source:  history.SourceExternal,
		Author:  e.Author,
		Path:    e.Path,
		Op:      history.OpExternal,
		VBefore: e.VBefore,
		VAfter:  e.VAfter,
	}
	switch u.Op {
	case index.Moved:
		ch.Extra.From = u.From
	case index.Removed:
		ch.Extra.Removed = true
		if u.OldBinary {
			ch.Extra.Binary = true
			break
		}
		ch.Fragments = linediff.Diff(textfile.Parse([]byte(u.Old)).Lines, nil)
		setStyle(&ch.Extra, []byte(u.Old))
	case index.Added, index.Modified:
		ch.Extra.Created = u.Op == index.Added
		if u.Binary || u.OldBinary || !curOK {
			ch.Extra.Binary = true
			break
		}
		var old []string
		if u.Op == index.Modified {
			old = textfile.Parse([]byte(u.Old)).Lines
		}
		ch.Fragments = linediff.Diff(old, textfile.Parse(cur).Lines)
	}
	return ch
}

var stores sync.Map // root -> *history.Store

// Store returns the history store of root, kept open for the life of the
// process. A history.db deleted since it was opened is recreated.
func Store(ctx context.Context, root string) (*history.Store, error) {
	if v, ok := stores.Load(root); ok {
		st := v.(*history.Store)
		if _, err := os.Stat(st.Path); !errors.Is(err, fs.ErrNotExist) {
			return st, nil
		}
		if stores.CompareAndDelete(root, st) {
			st.Close()
		}
	}
	st, err := history.Open(ctx, root)
	if err != nil {
		return nil, err
	}
	if v, loaded := stores.LoadOrStore(root, st); loaded {
		st.Close()
		return v.(*history.Store), nil
	}
	return st, nil
}
