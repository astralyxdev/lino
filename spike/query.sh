#!/bin/sh
# Milestone 2 spike, part 2: query latency on the databases built by run.sh.
# Prints one JSON report per engine/detail/limit combination.
set -eu

cd "$(dirname "$0")"
OUT=${OUT:-${TMPDIR:-/tmp}/lino-spike}
ITERS=${ITERS:-50}

CGO_ENABLED=0 go build -o "$OUT/ftsquery" ./ftsquery

for v in "sqlite full 20" "sqlite none 20" "mem none 20" "sqlite full 0" "sqlite none 0" "mem none 0"; do
	set -- $v
	echo "== engine=$1 detail=$2 k=$3" >&2
	"$OUT/ftsquery" -engine "$1" -db "$OUT/file-$2.db" -detail "$2" -k "$3" -iters "$ITERS"
done
