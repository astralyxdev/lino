# lino benchmark results

- Date: 2026-09-23T18:07:04Z
- Machine: Apple M4 Max, 16 CPUs, darwin/arm64, go1.26.1
- lino: lino dev (none), tree 3f5f2b1
- Corpus: golang/go@3901409b5d0fb7c85a3e6730a59943cc93b2835c: 1878 files, 1000911 lines, 29.5 MB, sha256 011e767e31ce

| scenario | metric | value | target | result |
|---|---|---|---|---|
| setup | init wall time (1878 files indexed) | 3.31 s | < 30.0 s | pass |
| edit-reindex | edit end-to-end p95 (archive/tar/reader_test.go, 1676 lines, n=50; p50 11.31 ms) | 18.6 ms |  |  |
| edit-reindex | edit internal errors (of 53 edit calls) | 0.00 calls | < 1.00 calls | pass |
| edit-reindex | edit server-side p95 (write + re-index + history) (lino stats, n=53) | 15.7 ms | < 20.0 ms | pass |
| edit-reindex | re-index hook p95 (lino stats, n=53) | 15.7 ms |  |  |

## lino stats after the run

- Index: 1848 files, 993517 lines, index.db 50.4 MB
- History: 85 changes, history.db 2.0 MB
- Live process: peak RSS 62.1 MB, heap 7.3 MB

| command | calls | p50 | p95 | max |
|---|---|---|---|---|
| edit | 53 | 5.27 ms | 15.7 ms | 19.7 ms |
| read | 1 | 0.130 ms | 0.130 ms | 0.130 ms |
| stats | 1 | 0.960 ms | 0.960 ms | 0.960 ms |
