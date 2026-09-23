#!/bin/sh
# Tests scripts/install.sh against a local test release for this machine's
# platform: a clean install must put lino and lino-core in place, and a tampered
# or missing binary, a wrong checksum or a missing checksum entry must fail with
# nothing installed.
#
#   scripts/test-install.sh             # builds the release with dist.sh
#   DIST=path scripts/test-install.sh   # uses an existing release directory
set -eu

cd "$(dirname "$0")/.."
REPO_DIR=$(pwd)
VERSION=${VERSION:-v0.0.0-installtest}

work=$(mktemp -d 2>/dev/null || mktemp -d -t lino-install-test)
trap 'rm -rf "$work"; [ -z "${srv_pid:-}" ] || kill "$srv_pid" 2>/dev/null || true' EXIT

if [ -z "${DIST:-}" ]; then
	DIST="$work/release"
	OUT="$DIST" VERSION="$VERSION" COMMIT=test sh scripts/dist.sh >/dev/null
fi
DIST=$(cd "$DIST" && pwd)
VERSION=$(sed -n 's/^[0-9a-f]* [ *]*lino_\(.*\)_[a-z]*_[a-z0-9]*$/\1/p' "$DIST/checksums.txt" | head -n 1)
[ -n "$VERSION" ] || {
	echo "no lino_<version>_<os>_<arch> entries in $DIST/checksums.txt" >&2
	exit 1
}

failures=0
pass() { printf 'ok   %s\n' "$1"; }
fail() {
	printf 'FAIL %s\n' "$1"
	[ ! -s "$work/err" ] || sed 's/^/     | /' "$work/err"
	failures=$((failures + 1))
}

# run_install NAME BASE_URL [ENV...]: runs the installer into a fresh dir.
# Sets $dir and $code; stderr is kept in $work/err.
run_install() {
	name=$1 base=$2
	shift 2
	dir="$work/bin-$name"
	code=0
	env LINO_VERSION="$VERSION" LINO_BASE_URL="$base" LINO_INSTALL_DIR="$dir" "$@" \
		sh "$REPO_DIR/scripts/install.sh" >"$work/out" 2>"$work/err" || code=$?
}

expect_ok() {
	if [ "$code" = 0 ] && [ -x "$dir/lino" ] && [ -x "$dir/lino-core" ] &&
		"$dir/lino" --version | grep -q "lino $VERSION" &&
		"$dir/lino-core" --version | grep -q "lino $VERSION" &&
		"$dir/lino" help 2>&1 | grep -q "usage: lino"; then
		pass "$1"
	else
		fail "$1 (exit $code)"
	fi
}

# expect_refused NAME PATTERN: exit 1, message on stderr, nothing installed.
expect_refused() {
	if [ "$code" = 1 ] && grep -q "$2" "$work/err" && [ ! -e "$dir/lino" ] && [ -z "$(ls -A "$dir" 2>/dev/null)" ]; then
		pass "$1"
	else
		fail "$1 (exit $code, want 1 and '$2' with nothing installed)"
	fi
}

# release NAME: copies the test release so a case can corrupt it.
release() {
	cp -R "$DIST" "$work/$1"
	echo "$work/$1"
}

asset_of() {
	os=$(uname -s | tr '[:upper:]' '[:lower:]')
	arch=$(uname -m)
	if [ "$os" = darwin ] && [ "$arch" = x86_64 ] && [ "$(sysctl -n hw.optional.arm64 2>/dev/null || echo 0)" = 1 ]; then
		arch=arm64
	fi
	case $arch in
	x86_64) arch=amd64 ;;
	aarch64) arch=arm64 ;;
	esac
	echo "lino_${VERSION}_${os}_${arch}"
}
asset=$(asset_of)
core="lino-core${asset#lino}"
for a in "$asset" "$core"; do
	[ -f "$DIST/$a" ] || {
		echo "test release has no $a" >&2
		exit 1
	}
done

run_install clean "file://$DIST"
expect_ok "clean install ($asset)"

run_install reinstall "file://$DIST"
run_install reinstall "file://$DIST"
expect_ok "reinstall over an existing binary"

r=$(release tampered)
printf 'x' >>"$r/$asset"
run_install tampered "file://$r"
expect_refused "tampered binary rejected" "CHECKSUM MISMATCH"

r=$(release tamperedcore)
printf 'x' >>"$r/$core"
run_install tamperedcore "file://$r"
expect_refused "tampered lino-core rejected" "CHECKSUM MISMATCH"

r=$(release nocore)
rm "$r/$core"
run_install nocore "file://$r"
expect_refused "missing lino-core rejected" "cannot download"

r=$(release wrongsum)
awk -v f="$asset" '{ if ($2 == f || $2 == "*" f) $1 = "0000000000000000000000000000000000000000000000000000000000000000"; print }' \
	"$DIST/checksums.txt" >"$r/checksums.txt"
run_install wrongsum "file://$r"
expect_refused "wrong checksum rejected" "CHECKSUM MISMATCH"

r=$(release noentry)
grep -v " \**$asset\$" "$DIST/checksums.txt" >"$r/checksums.txt" || true
run_install noentry "file://$r"
expect_refused "missing checksum entry rejected" "no entry for"

r=$(release nochecksums)
rm "$r/checksums.txt"
run_install nochecksums "file://$r"
expect_refused "missing checksums.txt rejected" "cannot download"

run_install badversion "file://$DIST" LINO_VERSION=v9.9.9-missing
expect_refused "unknown version rejected" "cannot download"

# The wget path, over HTTP, with curl hidden from PATH.
if command -v wget >/dev/null 2>&1 && command -v python3 >/dev/null 2>&1; then
	shim="$work/shim"
	mkdir "$shim"
	for c in sh env uname sysctl mktemp rm awk sha256sum shasum openssl cut mkdir chmod cp mv id wget sed tr cat grep ls head; do
		p=$(command -v "$c" 2>/dev/null) && ln -s "$p" "$shim/$c"
	done
	port=$((20000 + $$ % 20000))
	python3 -m http.server "$port" --bind 127.0.0.1 --directory "$DIST" >/dev/null 2>&1 &
	srv_pid=$!
	i=0
	until wget -q -O /dev/null "http://127.0.0.1:$port/checksums.txt" 2>/dev/null || [ $i -ge 50 ]; do
		sleep 0.1
		i=$((i + 1))
	done
	run_install wget "http://127.0.0.1:$port" PATH="$shim"
	expect_ok "install with wget (no curl)"
else
	echo "skip install with wget (needs wget and python3)"
fi

if [ "$failures" -ne 0 ]; then
	echo "$failures installer test(s) failed" >&2
	exit 1
fi
echo "installer tests passed"
