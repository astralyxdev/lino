#!/bin/sh
# Run the lino benchmarks on the pinned 1M-line corpus.
#   bench/run.sh                      # all scenarios
#   bench/run.sh -run sample-search   # extra flags go to linobench (-h lists them)
# Results: bench/results/results.{md,json}. Offline: CORPUS="$(go env GOROOT)/src" SUB= bench/run.sh
set -eu

cd "$(dirname "$0")/.."
CORPUS=${CORPUS:-$(bench/corpus.sh)}
SUB=${SUB-src}
export CGO_ENABLED=0
go run ./bench/cmd/linobench -corpus "$CORPUS" -sub "$SUB" "$@"
