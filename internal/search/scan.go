package search

import "fmt"

// Notes explaining why a search scanned every indexed file instead of using
// the trigram index. They are printed on stderr.
var (
	ShortQueryNote = fmt.Sprintf("query shorter than %d characters: scanned all indexed files", MinTrigram)
	RegexScanNote  = fmt.Sprintf("regex has no literal run of %d+ characters: scanned all indexed files", MinTrigram)
	scanNote       = "no trigram in query: scanned all indexed files"
)

// noteFor returns the scan note for a search that fell back to a scan.
func noteFor(opt Options) string {
	if opt.ScanNote != "" {
		return opt.ScanNote
	}
	return scanNote
}
