#!/bin/sh
# Fetch the benchmark corpus: the golang/go repository at a pinned commit
# (tag go1.24.0), as a GitHub archive kept unextracted in bench/.corpus
# (gitignored), so Go tooling and gofmt never see its files. Prints the
# archive path. The harness copies the first 1M lines of its src/ directory,
# in lexical order, into a fresh root for every run; the report records the
# pin and a sha256 digest of exactly what was copied.
#
# Offline alternative: linobench -corpus "$(go env GOROOT)/src".
set -eu

REPO=golang/go
COMMIT=${COMMIT:-3901409b5d0fb7c85a3e6730a59943cc93b2835c} # go1.24.0
DIR=${DIR:-$(cd "$(dirname "$0")" && pwd)/.corpus}
ARCHIVE="$DIR/go-$COMMIT.tar.gz"

if [ ! -f "$ARCHIVE" ]; then
	mkdir -p "$DIR"
	echo "fetching $REPO@$COMMIT" >&2
	curl -fsSL -o "$ARCHIVE.tmp" "https://codeload.github.com/$REPO/tar.gz/$COMMIT"
	mv "$ARCHIVE.tmp" "$ARCHIVE"
	echo "$REPO@$COMMIT" >"$ARCHIVE.pin"
fi
echo "$ARCHIVE"
