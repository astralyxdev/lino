#!/bin/sh
# Run the token benchmark: every bench/tokens task in the grep, rg and lino
# arms with Claude Code (claude -p, bash tool only). This calls a paid API:
# 19 tasks x 3 arms x REPS runs on the full go1.24.0 src/ tree.
#   bench/tokens/run.sh -model claude-opus-5 -reps 3
#   bench/tokens/run.sh -tasks T01,T11 -arms grep,lino
#   bench/tokens/run.sh -dry          # offline: scripted agent, validates the harness
# Results: bench/results/tokens/results.{md,json}; per-run transcripts in the work dir.
set -eu
cd "$(dirname "$0")/../.."
CORPUS=${CORPUS:-$(bench/corpus.sh)}
export CGO_ENABLED=0
exec go run ./bench/tokens/cmd/tokenbench -corpus "$CORPUS" "$@"
