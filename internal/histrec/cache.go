package histrec

import (
	"context"
	"path/filepath"

	"github.com/astralyx/lino/internal/changelog"
	"github.com/astralyx/lino/internal/history"
	"github.com/astralyx/lino/internal/index"
	"github.com/astralyx/lino/internal/mutate"
	"github.com/astralyx/lino/internal/textfile"
	"github.com/astralyx/lino/internal/vcache"
	"github.com/astralyx/lino/internal/version"
)

// A version stops being current the moment a change replaces it; that is when
// its content is cached, together with the change id, so a later --v check or
// rollback needs no walk through history. Current versions are not cached:
// the index already holds them.
func init() {
	changelog.Recorders = append(changelog.Recorders, cacheOwn)
	changelog.ExternalRecorders = append(changelog.ExternalRecorders, cacheExternal)
}

func cacheOwn(_ context.Context, root string, seq int64, e changelog.Entry, c *mutate.Commit) (func(), error) {
	if c.Op == "mv" || c.From != "" || c.Before == nil {
		return nil, nil
	}
	return cachePut(vcache.For(root), e.Path, e.VBefore, c.Before, seq), nil
}

func cacheExternal(_ context.Context, db *index.DB, seq int64, e changelog.Entry, u index.Update) (func(), error) {
	if (u.Op != index.Modified && u.Op != index.Removed) || u.OldBinary {
		return nil, nil
	}
	root := filepath.Dir(filepath.Dir(db.Path))
	return cachePut(vcache.For(root), u.Path, e.VBefore, []byte(u.Old), seq), nil
}

func cachePut(vc *vcache.Cache, path, v string, data []byte, seq int64) func() {
	if vc == nil || v == "" || version.Of(data) != v {
		return nil
	}
	vc.PutMeta(path, v, data, vcache.Meta{Path: path, Next: seq, AsOf: seq})
	return func() { vc.Remove(path, v) }
}

// cached returns path at v from the root's version cache when history since
// the entry was stored cannot have changed the answer Reconstruct would give.
func cached(ctx context.Context, vc *vcache.Cache, st *history.Store, path, v string) (history.Version, bool) {
	data, m, ok := vc.GetMeta(path, v)
	if !ok {
		return history.Version{}, false
	}
	// Reconstruct returns the newest point where the file was at v. A later
	// change leaving v moves Next; a later move or binary change breaks the
	// chain the entry was built on.
	var next, barrier, latest int64
	err := st.SQL.QueryRowContext(ctx, `
		SELECT ifnull(max(CASE WHEN v_before = ? THEN id END), 0),
		       ifnull(max(op = 'mv' OR ifnull(json_extract(nullif(extra, ''), '$.from'), '') != '' OR ifnull(json_extract(nullif(extra, ''), '$.binary'), 0)
		                  OR ifnull(json_extract(nullif(extra, ''), '$.symlink'), 0)), 0),
		       ifnull(max(id), 0)
		FROM changes
		WHERE id > ? AND (path = ? OR json_extract(nullif(extra, ''), '$.from') = ?)`,
		v, m.AsOf, path, path).Scan(&next, &barrier, &latest)
	if err != nil || barrier != 0 {
		return history.Version{}, false
	}
	next = max(next, m.Next)
	if next == 0 {
		return history.Version{}, false
	}
	if latest > m.AsOf {
		vc.PutMeta(path, v, data, vcache.Meta{Path: m.Path, Next: next, AsOf: latest})
	}
	return history.Version{Path: m.Path, Content: data, Doc: textfile.Parse(data), Next: next}, true
}

// remember caches a version Reconstruct found in history.
func remember(vc *vcache.Cache, path, v string, ver history.Version, asOf int64) {
	if ver.Next == 0 || ver.Path != path {
		return
	}
	vc.PutMeta(path, v, ver.Content, vcache.Meta{Path: ver.Path, Next: ver.Next, AsOf: asOf})
}
