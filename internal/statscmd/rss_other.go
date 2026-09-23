//go:build !linux && !darwin

package statscmd

func rss() (cur, peak int64) { return 0, 0 }
