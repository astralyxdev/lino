#!/bin/sh
# Milestone 2 spike, part 1: build the FTS5 index in each row layout over a
# ~1M-line corpus and print one JSON report per variant.
#
# Corpus: the Go standard library source ($(go env GOROOT)/src), walked in
# lexical order and capped at MAXLINES lines. It is real code, pinned by the Go
# version, and needs no network. Override with SRC=/path/to/repo.
set -eu

cd "$(dirname "$0")"
SRC=${SRC:-$(go env GOROOT)/src}
MAXLINES=${MAXLINES:-1000000}
OUT=${OUT:-${TMPDIR:-/tmp}/lino-spike}
mkdir -p "$OUT"

CGO_ENABLED=0 go build -o "$OUT/ftsbuild" ./ftsbuild

for v in "file full" "file none" "chunk full 64" "chunk none 64" "chunk full 1"; do
	set -- $v
	layout=$1 detail=$2 chunk=${3:-0}
	db="$OUT/$layout-$detail${3:+-$chunk}.db"
	echo "== $layout detail=$detail${3:+ chunk=$chunk} -> $db" >&2
	"$OUT/ftsbuild" -src "$SRC" -maxlines "$MAXLINES" -db "$db" \
		-layout "$layout" -detail "$detail" -chunk "${chunk:-64}"
done
