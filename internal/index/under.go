package index

import "context"

// PathsUnder returns the indexed paths inside directory dir (root-relative,
// slash-separated), sorted. dir "" returns nothing.
func (d *DB) PathsUnder(ctx context.Context, dir string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	// '0' sorts right after '/', so [dir/, dir0) is exactly the subtree.
	rows, err := d.SQL.QueryContext(ctx, `SELECT path FROM files WHERE path >= ? AND path < ? ORDER BY path`, dir+"/", dir+"0")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
