# ars

`ars` is a full-screen Claude Code and Codex session navigator for the current
computer and explicitly configured SSH peers. Selecting a row creates or
attaches to a provider process in an ARS-owned tmux server. `Ctrl+Q` detaches
the terminal client, keeps that provider process alive on its original host,
and returns to the same refreshed TUI.

ARS is one local Go binary. Peers need no `ars` install, daemon, database,
cache, index, or background helper. Collection uses a private, one-shot,
version-matched ARS/2 helper over the user's existing OpenSSH configuration.

## Install

Install the latest release from npm:

```sh
npm install -g @baleen37/ars
```

The npm package includes native ars binaries for Apple Silicon, Linux x86-64,
and Linux arm64. It does not download an executable during installation. For a
Node-free install, download the matching archive from GitHub Releases, verify
it against `SHA256SUMS`, and place `ars` on `PATH`.

Prerequisites:

- Go as declared by `go.mod`, for building from source
- OpenSSH `ssh` locally and an OpenSSH-compatible server on each remote host
- `tmux` on every managed computer, including the current one
- a POSIX `/bin/sh` plus `uname`, `mkdir`, `cat`, `chmod`, `rm`, and `rmdir` on
  each remote host
- `claude` and/or `codex` on each node's `PATH`, with native session metadata
  under that user's home directory

`fzf` is not used or required.

Build the three collector assets and the local executable:

```sh
go run ./cmd/ars-build
install -m 0755 ./ars ~/.local/bin/ars
```

`ars-build --assets-only` creates exactly these embedded collectors before any
local `ars` build: `darwin/arm64`, `linux/amd64`, and `linux/arm64`. Generated
collector blobs and the root `ars` build artifact are local build outputs and
must not be committed.

## Automatic update check

Interactive runs check GitHub Releases for a newer version with a 1.5
second budget; any failure is ignored and ars starts normally. When a
newer release exists, ars offers it before the TUI starts: press Enter to
update and continue on the new version, or any other key to skip. npm
installs update through `npm install -g @baleen37/ars`; standalone
binaries are verified against `SHA256SUMS` and replaced in place. Source
builds skip the check.

## Localhost and remote inventory

The default inventory is `${XDG_CONFIG_HOME}/ars/hosts` when
`XDG_CONFIG_HOME` is set, otherwise `~/.config/ars/hosts`. The current computer
is always included as `localhost`; it requires no configuration. The hosts file
contains only OpenSSH peers. A missing hosts file is a valid local-only
configuration. Put one peer target on each line, in the order it should be
reported:

```text
# ~/.config/ars/hosts
devbox
deploy@example.internal
agent-mac
```

`ars localhost` opens only current-computer sessions. `localhost` is reserved
and cannot be added with `ars remote add`.

When upgrading an existing installation, read the exact value from its
`${XDG_CONFIG_HOME}/ars/local-host` file, or `~/.config/ars/local-host` when
`XDG_CONFIG_HOME` is unset. Remove that exact entry and any literal `localhost`
entry from `hosts`, leaving only SSH peers. ARS does not read or delete the
legacy `local-host` file.

Blank lines and comments in `hosts` are ignored. Targets may use names and
aliases from the user's SSH config. Duplicates, whitespace, control characters,
a leading dash, and targets over 255 bytes make the entire inventory invalid.

`ars remote add <host>` creates the inventory and its parent directory when
missing, preserves existing comments, entries, and order, and rejects invalid
or duplicate targets. It also rejects the reserved `localhost` target. A target
beginning with `#` is rejected because inventory loading would interpret it as
a comment. The command does not edit `~/.ssh/config`.

## Commands

The supported command forms are:

```sh
ars                    # search localhost and every configured SSH peer
ars localhost          # search only current-computer sessions
ars devbox             # search one configured SSH peer
ars list --json        # return all hosts, sessions, and errors as JSON
ars remote add devbox  # add an SSH target to the ARS inventory
ars --help             # show all command forms
ars remote --help      # show remote command help
```

`ars` and `ars <host>` require a TTY. Use `ars list --json` for a headless,
one-shot result.

## TUI

The screen has one line per session:

```text
state | title | provider | location | project | runtime | activity age
```

The location column is blank for current-computer sessions and shows the
configured SSH target for peers.

`Active` contains ARS-owned runtimes: `attached(n)` has one or more terminal
clients and `running` has none. `Recent` contains saved provider histories with
no ARS runtime. The footer shows the selected row's full CWD, native ID, and
exact update time. Search is a Unicode-aware, case-insensitive substring match;
there is no fuzzy ranker.

Keys:

- `Up`, `Down`, `j`, `k`: move
- `/`: search
- `Enter`: start or attach
- `r`: refresh
- `q`, `Ctrl+C`: quit ARS
- `Ctrl+Q`: detach only while inside an attached ARS tmux client

The screen collects at startup, on `r`, and after attach returns. Rows appear
immediately from the last collection, cached per host under
`${XDG_CACHE_HOME:-~/.cache}/ars/hosts/`, marked `cached` until that host's
live refresh lands; each host updates independently, so a slow peer does not
hold up the others (hosts are collected up to four at a time). It does not
poll, watch, or collect in the background, and
peers still store nothing. Canonical host/provider/native-ID data, never
rendered row text, determines the attach command.

## Session inclusion

`ars` compiles in only the Claude Code and Codex adapters:

- Claude reads direct regular files at
  `~/.claude/projects/<project>/*.jsonl`. It includes canonical root session
  IDs with an absolute CWD, uses only native custom/AI/agent titles, and
  excludes internal, sidechain, and agent histories. It never derives a title
  from prompt text.
- Codex recursively reads regular `.jsonl` files below `~/.codex/sessions`.
  It includes only one valid `session_meta` with `thread_source=user` and
  `source=cli` or `source=vscode`. Exec, subagent, and unknown sources are
  excluded. Codex titles are empty in schema version 1.

Both adapters require their provider executable on the remote `PATH`, use the
metadata file modification time as `updated_at`, validate canonical UUIDs, and
deduplicate by host, provider, and native ID. A missing executable or metadata
tree is a healthy absent-provider result, not a host failure.

## Persistent provider runtime

ARS uses a dedicated versioned, per-user tmux server (`ars-v1`) with
`-f /dev/null`. It does not use, configure, rename, attach, or bind keys in the
user's default tmux server. It creates exactly one hashed runtime for the
selected `(provider, native ID)` and rechecks after a concurrent create instead
of starting a duplicate provider process.

On first selection, ARS starts the adapter's fixed native resume command in the
saved CWD. Later selections attach that same runtime. `Ctrl+Q` is a prefix-free
binding in the ARS server and runs `detach-client`; it is not delivered to
Claude or Codex. When another computer attaches with `ssh -tt`, tmux detaches
the previous client back to its TUI and hands the same provider process to the
new client.

If the provider exits, its tmux session ends and the native history remains in
`Recent`. A host reboot or loss of the ARS tmux server ends the live process but
does not remove native history. ARS does not discover or adopt providers that
were started outside its own tmux server.

## JSON schema version 1

Public JSON uses dedicated DTOs and always ends with a newline:

```json
{
  "schema_version": 1,
  "hosts": [
    {"target": "localhost", "status": "ok"},
    {"target": "down", "status": "error"}
  ],
  "sessions": [
    {
      "host": "localhost",
      "provider": "claude",
      "native_id": "123e4567-e89b-42d3-a456-426614174000",
      "updated_at": "2026-07-19T01:02:03Z",
      "cwd": "/work/app",
      "title": "Fix login"
    }
  ],
  "errors": [
    {"host": "down", "code": "ssh_failed", "message": "SSH collection failed"}
  ]
}
```

`schema_version` is `1`. Timestamps are UTC-capable RFC3339Nano strings.
`hosts` records every selected target exactly once. `status=ok` with
an empty `sessions` array distinguishes a healthy empty host from an
unreachable host. Stable error codes are `ssh_timeout`, `ssh_failed`,
`unsupported_target`, `protocol_error`, and `resource_limit`.

## SSH and partial-failure behavior

Collection and attach deliberately use separate SSH modes:

- Collection is non-interactive (`BatchMode=yes`, no agent/X11/port
  forwarding), uses separate probe and upload invocations that each make one
  connection attempt, enforces existing host keys with
  `StrictHostKeyChecking=yes`, uses a 5-second connect timeout, and gives the
  full probe/upload/protocol flow 60 seconds per host. Unknown hosts fail; ars
  never accepts or rewrites `known_hosts` entries.
- Attach runs `ssh -tt` with the user's normal OpenSSH configuration,
  authentication, and host-key verification. The bounded fixed remote script
  checks or creates the exact ARS tmux runtime, reapplies `Ctrl+Q`, and attaches
  with previous-client detachment. ARS does not weaken host-key checking or
  write SSH configuration.

Hosts are collected with at most four workers. One failed host produces a
bounded visible error while healthy local and peer sessions remain searchable
and attachable. A provider or runtime warning is shown beside otherwise healthy
sessions. If every selected host fails, the TUI still permits refresh and quit;
JSON mode writes the structured result and exits non-zero. A healthy empty host
is success. Provider-level corrupt records can yield a warning without
discarding valid sessions.

## Bounds and cleanup

Collection is bounded and fail-closed:

- 64 KiB startup noise and 64 KiB per ARS/2 protocol line
- 16 MiB total collector stdout and 64 KiB diagnostic stderr
- 10,000 sessions total; provider traversal also caps discovered sessions at
  10,000
- 1 MiB maximum provider JSONL line and 64 directory levels for Codex
- 36 bytes for a native UUID, 4,096 bytes for CWD, and 1,024 bytes for title

The uploaded collector is written with `umask 077` to one nonce-specific
`${TMPDIR:-/tmp}/ars-<128-bit-nonce>` directory. EXIT/HUP/INT/TERM traps remove
only that exact collector file and directory. On an interrupted local command,
ars makes one separate, five-second exact cleanup attempt. A remote power loss
or `SIGKILL` can prevent both paths and leave that one nonce-specific private
directory; version 1 has no janitor and operators may inspect and remove only
that exact leftover.

## Privacy

The private ARS/2 collector returns validated metadata only: provider, native
UUID, modification time, saved CWD, native title, runtime state, attached-client
count, and runtime start time. Prompts, responses, tool input or output,
credentials, raw transcript lines, provider source paths, filenames, pane
content, and attached terminal output never cross the collection boundary.
Host diagnostics are bounded and sanitized before display.

Public JSON schema v1 deliberately omits all runtime fields and remains the
provider-history view. Saved CWD and native title are still potentially
sensitive metadata, so protect JSON output and terminal history accordingly.

## Verification

The complete automated release check is:

```sh
go run ./cmd/ars-build --assets-only
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/ars-build
go test ./internal/runtime -run TestDisposableTmux -count=1 -v
go test ./internal/tui -run TestPTYAttachDetachRestoresTUI -count=1 -v
npm test
git diff --check
```

The ephemeral loopback sshd integration is disposable and opt-in:

```sh
ARS_RUN_SSHD_INTEGRATION=1 go test ./internal/ssh -run TestEphemeralSSHDCollectsAndAttaches -count=1 -v
```

It generates temporary host/client keys, `authorized_keys`, `known_hosts`, and
configs, logs, provider fixture, and isolated tmux socket. It verifies strict
host-key checking, remote create/detach with the same provider PID, and
second-client handoff without modifying system sshd, persistent SSH state, or
the default tmux server.

### Real two-host acceptance

Automated evidence is separate from real-host acceptance. Before release, use
two genuinely ready hosts with the same ARS build and reciprocal peer
inventories: host A's `hosts` file contains only host B's configured SSH target,
and host B's contains only host A's. Neither file lists its own computer.

Identity is intentionally observer-relative: a computer is `localhost` to
itself and its configured SSH target to its peer. Verify:

1. Compare the two computers' visible session sets by `(provider, native ID)`,
   not by host label or canonical session key.
2. On host A, start a local Claude session, press `Ctrl+Q`, and confirm its PID
   remains alive after A returns to its TUI.
3. From A's TUI, attach to that local Claude runtime so A is actually attached.
   While A is attached, select the same provider and native ID on B under A's
   configured SSH target. Confirm A is forced back to its TUI and B attaches to
   the same runtime and PID.
4. While B remains attached, select that same local runtime on A again. Confirm
   B is forced back to its TUI and A attaches to the same PID.
5. Repeat steps 2–4 for a B-hosted Claude runtime, with A and B reversed.
6. Repeat steps 2–5 for Codex.
7. Network loss returns to the TUI without killing the provider, and an
   unreachable peer remains visible beside healthy sessions.
8. The user's default tmux server, configuration, keys, and sessions are
   unchanged.

If inventory, SSH, DNS, tmux, or provider readiness is missing, record this
manual gate as incomplete. Disposable or simulated results are never a real
two-host pass.

## Release

Releases run after CI succeeds on `main`. Conventional `feat`, `fix`, `perf`,
and `BREAKING CHANGE` commits determine the next version; documentation,
chore, and test-only changes are no-ops. Rapid pushes may coalesce into one
pending release run; that run includes every commit since the last release tag.

The one-time npm setup is:

1. publish `@baleen37/ars@0.0.0` with the `bootstrap` dist-tag
2. configure npm Trusted Publishing for GitHub repository
   `baleen37/agent-remote-sessions` and workflow `ci.yml`
3. allow `npm publish`, then verify the first `main` release as `v1.0.0`

If publication is partial, inspect the Git tag, npm version, and GitHub Release
before changing state. Preserve any public npm version. Reconstruct a missing
GitHub Release from the same tag, or publish a missing npm package rebuilt from
that exact tag. For a missing npm version `X.Y.Z`, run:

```sh
git switch --detach vX.Y.Z
go run ./cmd/ars-build --release X.Y.Z
npm login
npm publish ./dist/npm --access public
```

Delete a failed tag only when neither registry published it.
