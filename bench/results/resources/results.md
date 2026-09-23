# lino benchmark results

- Date: 2026-09-23T17:57:19Z
- Machine: Apple M4 Max, 16 CPUs, darwin/arm64, go1.26.1
- lino: lino dev (none), tree 3f5f2b1
- Corpus: golang/go@3901409b5d0fb7c85a3e6730a59943cc93b2835c: 1878 files, 1000911 lines, 29.5 MB, sha256 011e767e31ce

| scenario | metric | value | target | result |
|---|---|---|---|---|
| setup | init wall time (1878 files indexed) | 3.31 s | < 30.0 s | pass |
| index-size | source size (1878 files, 1000911 lines) | 28.1 MB |  |  |
| index-size | index size on disk (index.db with -wal and -shm) | 44.6 MB |  |  |
| index-size | index / source | 1.59 x |  |  |
| history-disk | history growth per 1,000 edits (1000 edits in 100 files, 1016 changes recorded; history.db with -wal and -shm, 0.9 -> 4.5 MB) | 3.55 MB |  |  |
| history-disk | history.db main file growth per 1,000 edits (without -wal and -shm, before any checkpoint of the rest) | 0.469 MB |  |  |
| history-disk | history fragments per 1,000 edits (stored change fragments, from lino stats) | 0.118 MB |  |  |
| history-disk | history total per 1,000 changes (lino stats: whole db over all 1048 changes, fixed overhead included) | 4.23 MB |  |  |
| history-disk | edit wall time, mean (end-to-end, client process included) | 20.6 ms |  |  |
| history-disk | edit internal errors (first: index cmd/compile/internal/escape/call.go: database is locked (5) (SQLITE_BUSY)) | 11.0 calls | < 1.00 calls | FAIL |
| idle-rss | idle RSS (max of 12 samples over 3 s, after 1125 calls) | 135 MB | < 200 MB | pass |
| idle-rss | peak RSS (since start, from lino stats) | 135 MB |  |  |
| idle-rss | Go heap (from lino stats) | 61.6 MB |  |  |

## lino stats after the run

- Index: 1848 files, 993517 lines, index.db 52.5 MB
- History: 1048 changes, history.db 4.7 MB
- Live process: peak RSS 142.0 MB, heap 64.7 MB

| command | calls | p50 | p95 | max |
|---|---|---|---|---|
| edit | 1011 | 13.1 ms | 22.6 ms | 55.6 ms |
| read | 111 | 0.114 ms | 0.237 ms | 0.649 ms |
| stats | 4 | 1.02 ms | 2.13 ms | 2.13 ms |
