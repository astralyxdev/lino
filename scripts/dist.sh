#!/bin/sh
# Builds static lino binaries for every supported platform into dist/
# and writes dist/checksums.txt (SHA-256).
set -eu

cd "$(dirname "$0")/.."

VERSION=${VERSION:-$(git describe --tags --always 2>/dev/null || echo dev)}
COMMIT=${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}
OUT=${OUT:-dist}
PLATFORMS=${PLATFORMS:-"linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"}

rm -rf "$OUT"
mkdir -p "$OUT"

for p in $PLATFORMS; do
	os=${p%/*}
	arch=${p#*/}
	bin="lino_${VERSION}_${os}_${arch}"
	echo "build $bin" >&2
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
		-ldflags "-s -w -X main.version=$VERSION -X main.commit=$COMMIT" \
		-o "$OUT/$bin" ./cmd/lino
done

cd "$OUT"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum lino_* >checksums.txt
else
	shasum -a 256 lino_* >checksums.txt
fi
cat checksums.txt
