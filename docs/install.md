# Installing lino

```sh
curl -fsSL https://raw.githubusercontent.com/astralyxdev/lino/main/scripts/install.sh | sh
```

`scripts/install.sh` is POSIX sh. It detects the OS (`linux`, `darwin`; WSL counts as Linux)
and architecture (`amd64`, `arm64`; a Rosetta shell on Apple silicon gets `arm64`), downloads
`lino_<version>_<os>_<arch>`, `lino-core_<version>_<os>_<arch>` and `checksums.txt` from the
release, verifies both SHA-256 sums and only then installs `lino` and `lino-core` side by side
(`lino` is the thin client and execs `lino-core` from its own directory). On a mismatch it
prints both hashes, exits 1 and changes nothing.

| Variable | Default |
|---|---|
| `LINO_VERSION` | latest release (resolved from `github.com/<repo>/releases/latest`) |
| `LINO_INSTALL_DIR` | `/usr/local/bin` as root, `~/.local/bin` otherwise |
| `LINO_REPO` | `astralyxdev/lino` |
| `LINO_BASE_URL` | `https://github.com/$LINO_REPO/releases/download/$LINO_VERSION` |

Needs `curl` or `wget`, and `sha256sum`, `shasum` or `openssl`.

## Manual verification (2026-09-23)

Release assets built locally with `make dist` (VERSION=dev) and served with
`LINO_BASE_URL=file://<dir>`. `shellcheck -s sh` (koalaman/shellcheck:stable): clean.

| Platform | Case | Result |
|---|---|---|
| macOS 27, arm64, non-root | install | `~/.local/bin/lino`, `lino dev (none)`, PATH note printed |
| macOS 27, arm64 | one byte appended to the binary | `CHECKSUM MISMATCH`, exit 1, nothing installed |
| macOS 27, arm64 | unknown version | download error, exit 1 |
| Linux arm64, Debian stable-slim, root | install | `/usr/local/bin/lino`, runs |
| Linux arm64, Alpine (musl), root | install | `/usr/local/bin/lino`, runs |
| Linux amd64, Debian stable-slim (emulated), non-root | install | `~/.local/bin/lino`, runs |

Reproduce:

```sh
make dist
LINO_VERSION=dev LINO_BASE_URL="file://$PWD/dist" LINO_INSTALL_DIR=/tmp/lino-bin sh scripts/install.sh
```

Not yet verified: resolving the latest release and downloading from GitHub, since no
release has been published.

## Automated tests

`scripts/test-install.sh` builds a test release (or uses `DIST=<dir>`) and runs the installer
for the current platform: clean install (both binaries, `lino help` reaching `lino-core`),
reinstall, and refusal with nothing installed for a tampered `lino` or `lino-core`, a missing
`lino-core`, a wrong checksum, a missing checksum entry, a missing `checksums.txt` and an
unknown version. When `wget` and `python3` are available it also installs over HTTP with
`curl` hidden from `PATH`.

CI: `.github/workflows/install.yml` runs it on linux/amd64, linux/arm64 (qemu) and
darwin/arm64. `release.yml` calls it on the real release artifacts before publishing.
