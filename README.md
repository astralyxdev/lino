# lino

A fast indexed file editor for agents, running as a hot process per project.
Read, edit and search files with fewer tokens, no silent corruption, and faster than grep.

- Edits are checked against the version the agent read; stale edits are rejected with the fresh lines.
- Line-range edits use short content anchors instead of echoing old text.
- Writes are atomic, every change has an id, and `rollback` can undo any of them.
- Search hits a warm trigram index in a live process instead of scanning the disk.

See [scope.md](scope.md) for the full design.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/astralyxdev/lino/main/scripts/install.sh | sh
```

Linux and macOS, amd64 and arm64 (Windows via WSL). Options are in [docs/install.md](docs/install.md).

## Use

```sh
lino init                          # once per project: create .lino/ and build the index
lino run                           # start the live process (commands also auto-start it)

lino ls --depth 2
lino search "ErrInvalidAmount" -C 2
lino read wallet/service.go --lines 12:15 --anchors

lino edit wallet/service.go 13:9c1 15:e42 --v 8c21e0 <<'EOF'
    if amt <= 0 || amt > MaxWithdraw {
        return ErrInvalidAmount
    }
EOF

lino history wallet/ -k 5
lino rollback 1047 --dry-run
```

`lino help` lists every command. `lino help --agent` prints a short instruction block to put
in an agent's system prompt or `CLAUDE.md`. Add `--json` to any command for machine-readable output.

## Build

```sh
make build    # CGO_ENABLED=0 go build ./cmd/lino
make test
make dist     # static binaries for linux/darwin × amd64/arm64 plus checksums.txt
```

## License

MIT — see [LICENSE](LICENSE).
