#!/bin/sh
# lino installer.
#
#   curl -fsSL https://raw.githubusercontent.com/astralyx/lino/main/scripts/install.sh | sh
#
# Environment:
#   LINO_VERSION      release tag to install (default: latest release)
#   LINO_INSTALL_DIR  target directory (default: /usr/local/bin as root, else ~/.local/bin)
#   LINO_REPO         GitHub repository (default: astralyx/lino)
#   LINO_BASE_URL     download base holding the assets and checksums.txt
#                     (default: https://github.com/$LINO_REPO/releases/download/$LINO_VERSION)
#
# The binary is verified against checksums.txt (SHA-256) before it is installed.
set -eu

REPO=${LINO_REPO:-astralyx/lino}
VERSION=${LINO_VERSION:-}
BASE_URL=${LINO_BASE_URL:-}
INSTALL_DIR=${LINO_INSTALL_DIR:-}

say() { printf 'lino-install: %s\n' "$*" >&2; }
die() {
	say "error: $*"
	exit 1
}

have() { command -v "$1" >/dev/null 2>&1; }

# fetch URL FILE
fetch() {
	if have curl; then
		curl -fsSL --proto '=https,file' -o "$2" "$1"
	elif have wget; then
		wget -q -O "$2" "$1"
	else
		die "need curl or wget"
	fi
}

latest_version() {
	url="https://github.com/$REPO/releases/latest"
	if have curl; then
		final=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$url")
	elif have wget; then
		final=$(wget -q -S --spider "$url" 2>&1 | sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1 | tr -d '\r')
	else
		die "need curl or wget"
	fi
	tag=${final##*/}
	case $tag in
	"" | latest | releases) die "cannot determine the latest release of $REPO; set LINO_VERSION" ;;
	esac
	printf '%s\n' "$tag"
}

detect_os() {
	case $(uname -s) in
	Linux) echo linux ;; # WSL reports Linux and uses the Linux build
	Darwin) echo darwin ;;
	*) die "unsupported OS $(uname -s): lino runs on Linux and macOS (Windows: use WSL)" ;;
	esac
}

detect_arch() {
	m=$(uname -m)
	# A shell under Rosetta on Apple silicon reports x86_64.
	if [ "$1" = darwin ] && [ "$m" = x86_64 ] && [ "$(sysctl -n hw.optional.arm64 2>/dev/null || echo 0)" = 1 ]; then
		m=arm64
	fi
	case $m in
	x86_64 | amd64) echo amd64 ;;
	aarch64 | arm64) echo arm64 ;;
	*) die "unsupported architecture $m: lino ships amd64 and arm64" ;;
	esac
}

sha256() {
	if have sha256sum; then
		sha256sum "$1" | cut -d ' ' -f 1
	elif have shasum; then
		shasum -a 256 "$1" | cut -d ' ' -f 1
	elif have openssl; then
		openssl dgst -sha256 "$1" | sed 's/^.*= *//'
	else
		die "need sha256sum, shasum or openssl to verify the download"
	fi
}

default_dir() {
	if [ "$(id -u)" = 0 ]; then
		echo /usr/local/bin
	else
		echo "${HOME:?HOME is not set}/.local/bin"
	fi
}

main() {
	os=$(detect_os)
	arch=$(detect_arch "$os")
	[ -n "$VERSION" ] || VERSION=$(latest_version)
	[ -n "$BASE_URL" ] || BASE_URL="https://github.com/$REPO/releases/download/$VERSION"
	[ -n "$INSTALL_DIR" ] || INSTALL_DIR=$(default_dir)
	asset="lino_${VERSION}_${os}_${arch}"

	tmp=$(mktemp -d 2>/dev/null || mktemp -d -t lino)
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 1' HUP INT TERM

	say "downloading $asset ($VERSION)"
	fetch "$BASE_URL/checksums.txt" "$tmp/checksums.txt" || die "cannot download $BASE_URL/checksums.txt"
	fetch "$BASE_URL/$asset" "$tmp/$asset" || die "cannot download $BASE_URL/$asset"

	want=$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1; exit }' "$tmp/checksums.txt")
	[ -n "$want" ] || die "checksums.txt has no entry for $asset"
	got=$(sha256 "$tmp/$asset")
	if [ "$got" != "$want" ]; then
		say "CHECKSUM MISMATCH for $asset"
		say "  expected $want"
		say "  got      $got"
		die "refusing to install; nothing was changed"
	fi
	say "checksum ok ($got)"

	mkdir -p "$INSTALL_DIR" || die "cannot create $INSTALL_DIR"
	[ -w "$INSTALL_DIR" ] || die "$INSTALL_DIR is not writable; set LINO_INSTALL_DIR or run as root"
	chmod 755 "$tmp/$asset"
	# Copy next to the target, then rename, so a running lino is never half-replaced.
	cp "$tmp/$asset" "$INSTALL_DIR/.lino.new.$$" || die "cannot write to $INSTALL_DIR"
	mv -f "$INSTALL_DIR/.lino.new.$$" "$INSTALL_DIR/lino" || {
		rm -f "$INSTALL_DIR/.lino.new.$$"
		die "cannot install into $INSTALL_DIR"
	}

	say "installed $INSTALL_DIR/lino"
	"$INSTALL_DIR/lino" --version >&2 || die "installed binary does not run"
	case ":$PATH:" in
	*":$INSTALL_DIR:"*) ;;
	*) say "note: $INSTALL_DIR is not on your PATH; add it: export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
	esac
}

main "$@"
