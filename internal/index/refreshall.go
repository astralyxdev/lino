package index

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"golang.org/x/sys/unix"
)

// statWorkers bounds the parallel stat pass; more contend in the kernel on macOS.
const statWorkers = 8

// RefreshAll stats every indexed file under root in parallel and re-indexes
// those whose size or mtime differ from the index; vanished files are removed.
// It is direct mode's pre-query pass: files the index does not know yet are
// not discovered (that takes a walk: Reconcile).
func (d *DB) RefreshAll(ctx context.Context, root string, maxSize int64) (changed []Update, err error) {
	stale, err := d.staleFiles(ctx, root)
	if err != nil {
		return nil, err
	}
	defer func() { d.ReportExternal(ctx, changed) }()
	for _, p := range stale {
		u, err := d.IndexFile(ctx, p, filepath.Join(root, filepath.FromSlash(p)), maxSize)
		if err != nil {
			return changed, err
		}
		if u.Op != Unchanged {
			changed = append(changed, u)
		}
	}
	return changed, nil
}

type statRow struct {
	path        string
	size, mtime int64
}

// staleFiles returns, sorted, the indexed paths whose file is gone, not
// regular, or differs in size or mtime. Rows stream from the covering
// files_stat index in path order; each run of rows in one directory is
// statted by a worker against an open directory fd while the scan goes on.
func (d *DB) staleFiles(ctx context.Context, root string) ([]string, error) {
	var (
		mu    sync.Mutex
		stale []string
		first error
		wg    sync.WaitGroup
	)
	batches := make(chan []statRow, statWorkers)
	for range min(statWorkers, runtime.GOMAXPROCS(0)) {
		wg.Go(func() {
			var local []string
			var err error
			for b := range batches {
				if err == nil {
					local, err = statBatch(root, b, local)
				}
			}
			mu.Lock()
			stale = append(stale, local...)
			if first == nil {
				first = err
			}
			mu.Unlock()
		})
	}

	scanErr := func() error {
		rs, err := d.SQL.QueryContext(ctx, `SELECT path, size, mtime FROM files INDEXED BY files_stat`)
		if err != nil {
			return err
		}
		defer rs.Close()
		var batch []statRow
		dir := ""
		for rs.Next() {
			var r statRow
			if err := rs.Scan(&r.path, &r.size, &r.mtime); err != nil {
				return err
			}
			if dd := path.Dir(r.path); dd != dir || len(batch) >= 256 {
				if len(batch) > 0 {
					batches <- batch
				}
				batch, dir = nil, dd
			}
			batch = append(batch, r)
		}
		if len(batch) > 0 {
			batches <- batch
		}
		return rs.Err()
	}()
	close(batches)
	wg.Wait()
	if err := errors.Join(scanErr, first); err != nil {
		return nil, err
	}
	sort.Strings(stale)
	return stale, nil
}

// statBatch stats rows, all in one directory, relative to that directory's
// fd and appends the stale ones to out.
func statBatch(root string, rows []statRow, out []string) ([]string, error) {
	dir := path.Dir(rows[0].path)
	fd, err := unix.Open(filepath.Join(root, filepath.FromSlash(dir)), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ENOTDIR) {
		for _, r := range rows {
			out = append(out, r.path)
		}
		return out, nil
	}
	if err != nil {
		return out, &fs.PathError{Op: "open", Path: dir, Err: err}
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	for _, r := range rows {
		err := unix.Fstatat(fd, path.Base(r.path), &st, 0)
		switch {
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ENOTDIR):
			out = append(out, r.path)
		case err != nil:
			return out, &fs.PathError{Op: "stat", Path: r.path, Err: err}
		case uint32(st.Mode)&unix.S_IFMT != unix.S_IFREG || st.Size != r.size || st.Mtim.Nano() != r.mtime:
			out = append(out, r.path)
		}
	}
	return out, nil
}
