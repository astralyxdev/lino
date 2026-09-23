# Index spike: FTS5 on modernc.org/sqlite

Milestone 2 risk item. Question: can pure-Go SQLite (`modernc.org/sqlite`) with FTS5 serve
`search` under the 10 ms p95 end-to-end target on a 1M-line repository?

**Decision: yes. Keep `modernc.org/sqlite`.** Neither `ncruces/go-sqlite3` nor an in-memory
trigram index is needed. Worst indexed query p95 is 2.0 ms in-process, well under the ~7 ms left
after a ~3 ms client/socket budget.

## Setup

- Corpus: Go 1.26.1 standard library source (`$(go env GOROOT)/src`), lexical walk, capped at
  1M lines: 1,634 files, 1,002,125 lines, 29.4 MB.
- Machine: Apple M4 Max, darwin/arm64, `CGO_ENABLED=0`.
- Build: `spike/run.sh` (part 1, `spike/ftsbuild`). Queries: `spike/query.sh`
  (`spike/ftsquery`).
- One warm connection (as in the live process), `query_only`, 64 MB cache, 256 MB mmap.
  3 warm-up runs, then 50 timed runs per query. Timings include candidate lookup, loading
  content from SQLite, and line verification in Go.

## Build (from part 1)

| layout | detail | build | db size | peak RSS |
|---|---|---|---|---|
| one row per file | full | 2.9 s | 97 MB | 231 MB |
| one row per file | none | 2.0 s | 46 MB | 179 MB |
| 64-line chunks | full | 4.7 s | 147 MB | 253 MB |
| one row per line | full | 11.1 s | 197 MB | 260 MB |

## Query latency, hit limit k=20 (the default `search` limit)

p50 / p95 in ms.

| query | kind | sqlite detail=full | sqlite detail=none | in-memory trigrams |
|---|---|---|---|---|
| `maxConsecutiveEmptyReads` | rare literal | 0.21 / 0.23 | 0.27 / 0.31 | 0.005 / 0.006 |
| `ErrUnexpectedEOF` | rare literal | 0.32 / 0.42 | 0.29 / 0.34 | 0.005 / 0.007 |
| `ReadFull` | medium literal | 0.19 / 0.35 | 0.15 / 0.29 | 0.007 / 0.008 |
| `return err` | common literal | 2.48 / 2.63 | 0.62 / 0.78 | 0.06 / 0.07 |
| `if err != nil` | very common literal | 3.52 / 3.81 | 0.72 / 1.04 | 0.03 / 0.04 |
| `zzqxjv_nothere` | no hits | 0.01 / 0.01 | 0.05 / 0.05 | 0 / 0 |
| `func \w+Reader\(` | regex, literal run | 2.37 / 2.66 | 0.98 / 2.03 | 0.26 / 0.33 |
| `ErrUnexpected\w+` | regex, literal prefix | 0.42 / 0.53 | 0.30 / 0.82 | 0.02 / 0.02 |
| `if err := .*; err != nil` | regex, common | 4.74 / 5.24 | 0.87 / 1.84 | 0.08 / 0.08 |
| `mutex` | words BM25 | 0.46 / 0.64 | 0.41 / 0.59 | (sqlite) |
| `buffer reader` | words BM25 | 0.38 / 0.56 | 0.36 / 0.54 | (sqlite) |
| `checksum crc32` | words BM25 | 0.10 / 0.16 | 0.11 / 0.20 | (sqlite) |
| `ok` | < 3 chars: full scan | 0.26 / 0.34 | 0.26 / 0.81 | 0.02 / 0.03 |
| `\bx\d\b` | regex, no literal: full scan | 16.4 / 18.1 | 15.6 / 16.9 | 14.8 / 15.1 |

Indexed queries only (everything above the two full-scan rows): worst p95 is 3.8 ms / 5.2 ms
with detail=full and **2.0 ms with detail=none**.

## Counting every hit (k=0)

Only relevant if `search` ever reports an exact total. p95 in ms, sqlite detail=none:

| query | files / hits | p95 |
|---|---|---|
| `return err` | 181 / 918 | 8.2 |
| `if err != nil` | 522 / 3,240 | 8.1 |
| `if err := .*; err != nil` | 268 / 1,019 | 7.6 |
| `mutex` (words, top 200 files) | 72 / 113 | 5.1 |
| `ok` (full scan) | 755 / 7,088 | 19.0 |
| `\bx\d\b` (full scan, Go regexp) | 43 / 838 | 352 |

The in-memory index only halves the common cases here (4-5 ms); the cost is verification, not
lookup. Exhaustive counts on common queries cost ~8 ms, which is at the edge of the budget.

## What made the difference

1. **detail=none, query = AND of the literal's trigrams.** detail=none rejects phrase queries,
   so `"return err"` becomes `"ret" AND "etu" AND ... `. Phrase matching with detail=full costs
   2-5 ms on common literals for the same result, because it walks position lists. detail=none
   yields more candidates (462 vs 181 for `return err`) but they are cheap to discard, and the
   index is half the size.
2. **List candidate ids in path order first, load content per file, stop at k.** A first
   version joined content into an `ORDER BY path` query; SQLite then sorts every candidate's
   content, which cost up to 7 ms before a single line was verified.
3. **Verify on the whole content, not line by line.** `strings.Index` / `FindStringIndex` over
   the file, then cut out the hit's line. With (2), this took the no-literal full scan from 22 ms
   to 16 ms at k=20.

## Chosen approach (for I05 and the search task)

- One row per file in `files(id, path, content)`; `tri` = FTS5 external content over `files`,
  `tokenize='trigram'`, `detail=none`; `words` = FTS5 external content, `unicode61`, BM25 via
  `ORDER BY rank`.
- Literal: AND of the query's distinct lowercase trigrams (the tokenizer is case-insensitive),
  verified case-sensitively in Go.
- Regex: literal runs of 3+ characters under the top-level concatenation, each turned into
  trigrams and ANDed (case-folded literals are skipped). No such run → full scan, and the
  output says so, as the scope requires.
- Words: FTS5 BM25 ranks files; show a few matching lines per file.
- Results: candidate ids ordered by path, content loaded lazily, stop at the hit limit with
  outcome `truncated`. Do not compute exact totals by default.
- Driver stays `modernc.org/sqlite`. `ncruces/go-sqlite3` was not tried: there is no gap to
  close.

## Risks and follow-ups

- **Full scans are the only thing over budget.** A regex with no 3-character literal is
  ~16 ms at k=20 and ~350 ms when nothing stops it early (Go's regexp dominates, not SQLite;
  the in-memory index gives the same numbers). Acceptable as a documented, reported fallback;
  if it proves common, keep file content in the live process's memory and scan in parallel.
- The in-memory trigram index is 10-100x faster on indexed queries (build 0.27 s, content held
  in RAM, ~30 MB here plus postings). Kept in `spike/ftsquery/mem.go` as the fallback if a
  larger or slower machine misses the target.
- Not measured here: client process start and socket round trip (assumed ~3 ms; measure with
  milestone 3), Linux numbers, and concurrent writers (WAL readers are not blocked by the
  writer, but re-index latency under query load is untested).
