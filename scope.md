# lino — product scope

> A fast indexed file editor for agents, running as a hot process per project.
> Read, edit and search files with fewer tokens, no silent corruption, and faster than grep.
> A Go core with a CLI. Nothing else.

## What it is

A Go package and a CLI on top of it. The CLI starts a live process per project that keeps the
index and recent file versions in memory and watches the files; every other command talks to
that process. Any agent with a shell can use it today. Anything else — MCP servers, HTTP APIs,
harness bindings — is a separate project built on the CLI's `--json` output or on the Go package.

lino treats every file as lines of text. It does not parse code and has no notion of
functions, classes or languages: the model decides what to read and which range to edit.
It knows nothing about models, prompts, tasks or agents, and keeps no per-client state.

## Why

Agents spend most of their tokens finding and re-reading code, and a large share of output
tokens echoing old text to edit it. Grep scans the filesystem on every call and still leaves the
agent reading whole files. Positional edits are cheap but fail silently.

lino fixes exactly this — and nothing more:

- **Edits are verified.** Every edit is checked against the version the agent read. If the
  target changed since, the edit is rejected with the fresh lines — never applied blindly.
- **Edits are cheap.** Line-range edits reference short content anchors instead of echoing
  old text.
- **Writes are atomic.** A crash never leaves a half-written file.
- **Every change can be undone.** Each change — lino's or external — has an id; `rollback`
  reverses one change or returns files to any recorded point, without rewriting history.
- **Search is indexed and warm.** Queries hit a hot index in a live process, not the filesystem.
- **Every text file is supported.** Code, config, docs, data — any UTF-8 file, any language.
- **Own edits are visible immediately.** Edits through lino re-index before the command
  returns. External edits (IDE, git, shell) are picked up by the watcher within about a second;
  anchors and version checks catch anything in between.
- **One writer among lino clients.** All agents using lino go through one process. Other
  tools can still write; lino detects their changes, it does not prevent them.
- **Output is bounded.** Every command has a hard output limit. Truncation is explicit, never
  silent.

## Deliberately excluded

- Parsing and language awareness — no symbols, no definitions, no syntax checks. The model
  reads the lines and chooses the range; anchors and versions make that safe.
- MCP, HTTP, gRPC, harness bindings — separate projects on top of the CLI or package.
- Shell / command execution — the caller owns that.
- Version control — git stays the source of truth. lino's history is a short-term undo buffer
  for agent work, not a replacement for commits.
- Formatters — the caller runs them; lino sees the result as an external edit.
- Embeddings and semantic search.
- Remote access — the process listens on a local socket only.
- Native Windows — Windows users run the Linux build under WSL.
- Per-client state (sessions, stored cursors) — callers hold their own cursors and versions.
- Locks, transactions, auto-merge, per-agent workspaces — see Later.
- Tasks, memory, chat — separate products.

## Modes

**Live (default).** `lino run` starts a process for a project root. It owns the index,
keeps recent file versions in memory, runs the file watcher, and is the only lino writer.
Every other command is a thin client that sends the request over a local socket and prints the
reply.

**Auto-start.** If no live process runs for the root but the root has been initialised
(`.lino/` exists), any command starts one and then answers. The first call after a restart
pays for a reconcile; the output says so. Outside an initialised root, commands fail with
`not_running` and print the `lino init` command.

**Idle exit.** A live process exits after 2 hours without calls (`--idle`, `0` = never).
Auto-start brings it back on the next call, and the startup reconcile catches everything that
changed while it was down.

**Direct (fallback).** `--direct` makes a command open the index itself, check the files it
touches against disk, answer and exit. For CI and one-off scripts. No watcher; history is
still recorded. Direct mode refuses to run (`live_exists`) when a live process exists for the root:
two writers on one index is never allowed. Nothing ever falls back to direct mode silently.

**Socket protocol.** JSON lines over a unix socket, one request and one response per line, with
a protocol version in every message.

## Model

**Root.** One directory, canonicalised (absolute, symlinks resolved). Nothing outside it is
readable or writable; symlinks that escape the root are refused. Nested roots are refused: a
root inside another initialised root, or containing one, cannot be initialised. One repository,
one lino process.

**Ignored paths.** `.gitignore`, then `.linoignore` (same syntax) on top. `.lino/` is always
ignored and never watched; on `init`, lino adds `.lino/` to `.git/info/exclude` if the root
is a git repository.

**Index.** `<root>/.lino/index.db`, SQLite in WAL mode. Rebuildable from the files at any time;
never needs backing up or exporting. Config in `<root>/.lino/config`.

**Process id.** Six characters derived from the canonical root path, e.g. `k3f9qa`. Stable: the
same root always gets the same id, so an id saved by an agent keeps working across restarts.
`--name` adds a human alias; either works wherever an id does.

**Registry.** `~/.lino/run/<id>.json` (pid, root, socket, started, version) and
`~/.lino/run/<id>.sock`. Socket mode `0600`: only the owning user can connect. Stale entries
(dead pid, dead socket) are detected on use and cleaned by `lino ps`.

**Text files.** UTF-8 only; anything else, including files with NUL bytes, is binary: listed,
never read or edited as text. Per file, lino preserves line endings (LF or CRLF), a leading
BOM, presence of a final newline, and file mode. Lines are handled without their `\r`; new
content is written back in the file's own style.

**File version.** Short content hash of the whole file, printed in every `read` header as `v=…`.
Every mutating command takes `--v` with the version the agent last read.

**Version check.** A mutation with `--v V` is accepted when:
- the file is still at version `V`; or
- the file changed since `V`, but the lines the mutation targets are identical in `V` and in
  the current file. Changes elsewhere in the file do not block the edit.

Otherwise it fails with `conflict` and prints the current lines of the target region. Old
versions come from history (below); the live process keeps recently used ones in memory. If `V`
is no longer in history, only an exact version match is accepted.

**Anchor.** `<line>:<hash>` where hash is 3 base62 characters of the line's content hash
(about 1 in 238,000 chance that a changed line keeps its hash). Printed per line by
`read --anchors`. An anchor is valid if the line at that number still has that hash. If not,
lino looks for a unique line with that hash within ±50 lines (content moved, unchanged).
For a range, both anchors must relocate by the same offset and end must stay ≥ start.
Anything else fails with `anchor_mismatch` and prints the current lines of the region.

Identical lines (`}`, `end`, blank lines) share a hash; the line number disambiguates them.
Relocation only succeeds when the match is unique.

**Edit granularity.** Whole lines. No column offsets — models cannot count characters reliably.
The model chooses the range; lino guarantees the range is what the model saw.

**Atomic writes.** Every mutation writes a temp file in the same directory, fsyncs it, and
renames it over the original, preserving mode.

**Change log.** Every file change, from any source, gets an increasing sequence number — its
change id. Callers hold their own position; lino stores none.

**History.** Every change keeps what is needed to undo it:

- change id, time, source (`lino` or `external`), optional author (`--by` / `LINO_BY`), path,
  operation (`edit`, `insert`, `delete`, `replace`, `write`, `mv`, `rm`, `rollback`,
  `external`), version before and after;
- the changed fragments only: for each changed range, its position and the lines before and
  after. An edit of lines 125–152 stores those lines as they were and as they became — not the
  file. Whole-file operations (`write` over an existing file, external edits) are diffed into
  fragments the same way. Fragments are compressed.

Two operations need more than a fragment: `rm` stores the removed file's content (the fragment
is the whole file), and `mv` stores only the two paths.

To diff external edits, the index keeps the current content of every text file. That copy is
also what history reconstructs older versions from: current content plus fragments applied in
reverse.

History lives in `<root>/.lino/history.db`, separate from the index: the index can be rebuilt
from the files, history cannot. Deleting it loses undo, nothing else.

Retention: 14 days or 1 GB, whichever comes first, oldest pruned first. Pruning always removes
from the oldest end, so the chain from any retained change to the current content is never
broken. Configurable in `.lino/config`.

External edits are recorded too — the watcher knows the previous version, so an IDE save or a
`git pull` can be rolled back like any other change. Edits made while no lino process was
running are seen at the next startup as one `external` change per file, diffed against the
content the index last knew: the net fragments are recorded, the intermediate steps are
unknown. Binary files are recorded by hash only and cannot
be rolled back.

**Rollback.** Rolling back never rewrites history. It is a new change of type `rollback` that
restores content, so a rollback can itself be rolled back.

- **Undo one change** (`rollback <id>`): puts that change's old fragments back, like
  `git revert`. Positions are carried through every later change, so the fragment lands exactly
  even if lines were added or removed above it since. If later changes touched the same lines,
  it fails with `conflict` and shows them; later changes elsewhere in the file are kept. `rm` is undone by restoring the file,
  `mv` by moving it back, `write` of a new file by removing it.
- **Undo the latest change** (`rollback` with no id): the most recent change by the same author
  when `--by` / `LINO_BY` is set, otherwise the most recent change overall. It always prints
  which change it undid. With several agents working, pass an id — "the latest" may be someone
  else's.
- **Return to a point** (`rollback --to <id>`): restores files to their state right after change
  `<id>` by applying the fragments of every later change in reverse, newest first, optionally
  limited with `--path`. Later changes are
  overwritten by design; they stay in history and can be restored the same way.

`--dry-run` shows what a rollback would do without doing it.

## Addressing a process

Every command except `init`, `run` and `ps` needs to know which process to talk to. Resolution
order:

1. `-i ID` / `--id ID` flag
2. `LINO_ID` environment variable
3. The process whose root contains the current directory

An agent working inside a project never passes an id. Paths are always relative to the root.

## CLI

```
lino init [dir]                              # create .lino/ and build the index
lino run [dir] [--name N] [--idle D] [--foreground]   # start (or find) the process; prints id
lino ps                                      # running processes: id, name, root, pid, uptime
lino stop                                    # stop the process
lino status                                  # index and watcher state, pending re-index
lino index                                   # force a full reconcile

lino read <file> [--lines A:B] [--anchors]
lino edit <file> <start> <end> --v V         # replace range; new lines from stdin
lino insert <file> (--before A | --after A | --at-start | --at-end) --v V   # stdin
lino delete <file> <start> <end> --v V       # delete range
lino replace <file> --v V                    # exact unique match; old and new from stdin
lino write <file> [--v V | --force]          # create, or overwrite with --v / --force
lino mv <from> <to>
lino rm <file> [--v V | --force]

lino search <query> [--regex | --words] [--path P ...] [-k N] [-C N]
lino ls [path] [--depth N]

lino changes --since N [--path P ...] [--wait DURATION]
lino stats

lino history [path] [--since N] [--by A] [-k N]   # list changes, newest first
lino show <id>                                 # one change as a diff
lino rollback [<id>] [--dry-run]               # undo one change, or the latest
lino rollback --to <id> [--path P ...] [--dry-run]   # restore state right after change <id>

lino help --agent                            # instruction block for an agent's system prompt
```

All commands after `run` accept `-i ID` and `--json`; file commands also accept `--direct`.

`run` is idempotent: if a process already runs for that root it prints the existing id and
exits 0. By default it detaches and returns once the index is ready; `--foreground` stays
attached for systemd, Docker or debugging.

**Multiline input always comes from stdin**, so content never fights shell quoting. Deletion is
an explicit command, never "empty stdin" — a forgotten heredoc must not wipe code.

```
lino edit wallet/service.go 13:9c1 15:e42 --v 8c21e0 <<'EOF'
    if amt <= 0 || amt > MaxWithdraw {
        return ErrInvalidAmount
    }
EOF

lino insert wallet/service.go --after 15:e42 --v 8c21e0 <<'EOF'
    log.Debug("withdraw", "id", id, "amt", amt)
EOF
```

**`replace` reads old then new text from stdin**, split by a separator line
(default `<<<lino>>>`, change with `--sep` if the content contains it):

```
lino replace api/handlers.go --v 41bd07 <<'EOF'
err := wallet.Withdraw(ctx, req.ID, req.Amount)
<<<lino>>>
err := wallet.Withdraw(ctx, req.ID, req.Amount, req.Currency)
EOF
```

**`search` modes:**

- **literal** (default) — exact substring, via trigram candidates, verified line by line.
- **`--regex`** — Go regexp syntax; trigram candidates from the literal parts, verified line
  by line.
- **`--words`** — BM25 ranking over word tokens, for "which files talk about X".

`-C N` adds N context lines around each hit. Results are grouped by file, one line per hit.

Trigram limits: queries shorter than 3 characters, and regexes with no literal run of 3+
characters, cannot use the index and fall back to a full scan of indexed files. The output says
so.

**`ls`** lists the tree with line counts and sizes, so the model can choose what to open
without reading anything.

**`changes`** returns every change with sequence number > N, and the next position to pass.
`--path` (repeatable, globs allowed) filters to the areas the caller cares about. `--wait` holds
the call until a matching change arrives or the duration expires. This is how an agent learns
that someone touched its files before it tries to edit.

```
$ lino changes --since 1042 --path 'wallet/**' --wait 30s
1043  modified  wallet/service.go   v=8c21e0→5f02aa  lines 12-18
1044  added     wallet/validate.go  v=77c1d3
next 1044
```

**`history`, `show` and `rollback`:**

```
$ lino history wallet/ -k 3
1047  12:41:07  lino      agent-2  edit      wallet/service.go   lines 13-15
1044  12:39:52  lino      agent-1  write     wallet/validate.go  created
1043  12:39:10  external  -        external  wallet/service.go   lines 12-18

$ lino rollback 1047 --dry-run
would undo 1047 (edit wallet/service.go lines 13-15 by agent-2)
- 13:     if amt <= 0 || amt > MaxWithdraw {
+ 13:     if amt <= 0 {
no conflicts

$ lino rollback 1047
1051  rollback  wallet/service.go  undid 1047  v=5f02aa→8c21e0
```

Mutating commands accept `--by NAME` (or `LINO_BY`) to record an author. It is a label for
history and rollback, not an identity: nothing is checked against it.

## Output

**Default: compact text for agents.** One fact per line. `read` is plain by default; add
`--anchors` when you intend to edit — anchors cost tokens on every line.

```
$ lino read wallet/service.go --lines 12:15 --anchors
wallet/service.go v=8c21e0 lines 12-15 of 240
12:a3f| func Withdraw(ctx context.Context, id, amt int64) error {
13:9c1|     if amt <= 0 {
14:07b|         return ErrInvalidAmount
15:e42|     }

$ lino search "ErrInvalidAmount" -k 3
wallet/service.go
  14  return ErrInvalidAmount
wallet/errors.go
  7   ErrInvalidAmount = errors.New("invalid amount")
3 hits in 2 files (index)
```

**Output limits** (defaults, set in `.lino/config`):

- `read`: 500 lines per call.
- `search`: 20 hits (`-k` up to 200), lines cut at 200 characters.
- `ls`: 200 entries.
- `changes`: 500 events.

Over a limit, output is cut with outcome `truncated` and a hint for the next call
(`--lines 501:1000`, `--since 1543`).

**`--json`** for programs: the same data with `ok` and `outcome` fields. This is the contract
that MCP servers and harness bindings build on; it is versioned.

**Messages go to stderr, data to stdout.**

## Outcomes and exit codes

| `outcome` | Exit | When |
|---|---|---|
| `ok` · `updated` · `created` | 0 | success |
| `empty` | 0 | nothing found or no changes — not an error |
| `truncated` | 0 | output cut at its limit; continuation hint printed |
| `usage` | 2 | bad arguments |
| `not_found` | 3 | no such file or change id, or change pruned from history |
| `anchor_mismatch` | 4 | lino stale; current lines of the region printed |
| `ambiguous` | 5 | `replace` or id matched more than once; candidates printed |
| `conflict` | 6 | target changed since `--v`, or `--v` missing on overwrite; current lines printed |
| `refused` | 7 | outside root, binary, too large, nested root |
| `not_running` | 8 | no initialised root and no live process; `lino init` command printed |
| `live_exists` | 9 | `--direct` while a live process runs for the root |
| `internal` | 1 | anything else |

Scripts branch on exit codes; the model reads the message.

## Indexing and freshness

- **Store:** SQLite, WAL. Tables for files, trigrams (FTS5 trigram tokenizer), words (FTS5),
  change log.
- **Incremental:** content hash per file; unchanged files are skipped. Moved files are matched
  by hash, not re-indexed from scratch.
- **Own edits** re-index synchronously before the command returns.
- **Watcher:** batched and debounced; each batch is reconciled against disk by hash, not replayed
  event by event. Bulk changes (branch switch, `git pull`, overflow) trigger a full reconcile.
  `.lino/` is never watched.
- **Startup:** full reconcile against disk, so nothing that changed while the process was down
  is missed.
- **Direct mode:** stats the files a call touches and re-indexes any whose mtime or size changed.

## Agent instructions

`lino help --agent` prints a short block for an agent's system prompt or CLAUDE.md: which
commands replace `cat`, `grep`, `find` and `sed`, how anchors and `--v` work, and what to do on
each outcome. Adoption by the model is the product's biggest risk — if the agent falls back to
`cat` and `grep` out of habit, nothing else matters. The block is versioned and tested like code.

## Targets

To be verified by benchmark, not assumed:

- Live mode, `search`: p95 under 10 ms end-to-end, client process included, on a 1M-line
  repository.
- Live mode, re-index of one edited file: under 20 ms, history write included.
- History disk use: measured and reported per 1,000 edits on a real repository.
- Reconstructing a version 100 changes back: under 50 ms.
- External edit visible to `search` and `changes`: under 1 s.
- Direct mode, cold call overhead: under 15 ms.
- First `init` on a 1M-line repository: index ready in under 30 s.
- Idle memory of a live process on a 1M-line repository: under 200 MB.
- Index size on disk: measured and reported; trigram indexes are typically several times the
  size of the source.
- Tokens, measured on a fixed task set against grep + cat + sed and against ripgrep:
  total tokens per task, not per call. `--anchors` reads cost more than plain reads; the claim
  holds only if cheaper edits outweigh that.
- Fallback rate: share of agent file operations done with `cat` / `grep` / `sed` instead of
  lino, with the `help --agent` block in place.

Note: ripgrep is already fast on small repos. The win is on large repos, repeated queries,
edit safety between agents, and the size of what comes back.

## Milestones

1. **Files.** `read`, `edit`, `insert`, `delete`, `replace`, `write`, `mv`, `rm`; anchors,
   versions, atomic writes, text-file rules, root jail, outcomes, exit codes — in direct mode.
   Useful alone.
2. **Index.** Starts with a one-day spike: FTS5 trigram search on a 1M-line repository with
   the pure-Go SQLite, measured against the 10 ms target. Then trigrams and words; `search`,
   `ls`; `.linoignore`.
3. **Live process.** `init`, `run`, `ps`, `stop`, `status`; registry, socket, id resolution,
   auto-start, idle exit; client forwarding; watcher and reconcile.
4. **Changes and history.** Change log, `changes --since --path --wait`; history store,
   retention, `history`, `show`, `rollback` (one change, latest, `--to`), range-aware version
   check backed by history.
5. **Release.** Static binaries for Linux (amd64, arm64) and macOS (amd64, arm64), built with
   plain `go build`; one-line installer with checksum verification.
6. **Proof.** `help --agent`, `stats`, benchmarks against grep + cat + sed and ripgrep,
   fallback rate — published with losses included.

## Later

Considered and deferred until measurements show a need:

- **Leases (locks)** on files or line ranges, with TTL renewed by edits — if agents often
  collide on long multi-step changes.
- **Auto-merge** — 3-way merge of an edit whose range changed, applied when hunks don't overlap
  — if `conflict` retries turn out frequent.
- **Transactions** — multi-file changes that others see all at once — if parallel agents do
  multi-file refactors.

## Decisions

Made so the scope has no open questions. Each is reversible, but not for free.

- **Language: Go, pure — no cgo.** One static binary per platform from a plain `go build`,
  trivial cross-compilation, no C toolchain for contributors. Rewriting in C++ or Rust would not
  move latency, and Go keeps memory safety in the code that handles your files.
- **No parsing.** The model reads lines and chooses ranges; anchors and versions make that
  safe for every text file in every language. Nothing in the product depends on a grammar.
- **SQLite: `modernc.org/sqlite` (pure Go).** Slower than the C build on heavy queries, but
  search is index lookups on small result sets, where the gap matters least. Verified by the
  spike in milestone 2. If it misses the target: `ncruces/go-sqlite3` (SQLite compiled to
  WebAssembly, also pure Go) next; an in-memory trigram index in the live process after that.
  Going back to cgo is the last resort, not the first.
- **Platforms: Linux and macOS. No native Windows in v1** — Windows developers doing agent work
  largely use WSL, where the Linux build runs.
- **Formatter: never run by lino.** A formatter rewrites lines the agent didn't touch, which
  breaks other agents' anchors and versions.
- **History: changed fragments only, not file versions.** An edit of 20 lines in a 5,000-line
  file stores 20 lines, twice. Older versions are reconstructed from the current content by
  applying fragments in reverse; the cost grows with how far back you go, which is fine for an
  undo buffer. 14 days / 1 GB retention by default.
- **Rollback = new change, never rewrite.** History stays append-only, so every rollback is
  auditable and reversible.
- **Socket protocol: JSON lines, versioned.**
- **Lifecycle: auto-start in initialised roots, idle exit after 2 hours.**

