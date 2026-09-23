package search

import (
	"context"

	"github.com/astralyx/lino/internal/index"
)

// FromWords marks results ranked by BM25 over the words index.
const FromWords Source = "words"

// wordsPerFile is how many of a file's best lines --words shows.
const wordsPerFile = 3

// Words ranks files by BM25 for the words of q (see index.WordsText for the
// tokenisation) and returns up to wordsPerFile best lines per file, files in
// rank order. Limit bounds the total number of lines; Paths and Context
// apply as in the other modes.
func Words(ctx context.Context, db *index.DB, q string, opt Options) (Result, error) {
	wo := index.WordsOptions{Limit: opt.Limit, PerFile: wordsPerFile}
	if len(opt.Paths) > 0 {
		wo.Match = func(p string) bool { return MatchPath(opt.Paths, p) }
	}
	wr, err := db.SearchWords(ctx, q, wo)
	if err != nil {
		return Result{}, err
	}
	r := Result{Hits: []Hit{}, Files: len(wr.Files), Source: FromWords, More: wr.More}
	for _, f := range wr.Files {
		start := len(r.Hits)
		for _, h := range f.Hits {
			r.Hits = append(r.Hits, Hit{Path: f.Path, Line: h.Line, Text: h.Text})
		}
		if opt.Context > 0 {
			fi, ok, err := db.File(ctx, f.Path)
			if err != nil {
				return Result{}, err
			}
			if ok {
				addContext(fi.Content, r.Hits[start:], opt.Context)
			}
		}
	}
	return r, nil
}
