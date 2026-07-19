# ars

`ars` lists Claude Code and Codex sessions from configured SSH hosts, combines
them in one searchable picker, and resumes the selected native session. It is a
single local Go binary: remote hosts do not need an `ars` install, daemon,
cache, or background process.

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
- `fzf` locally for interactive selection; `ars list --json` does not use it
- a POSIX `/bin/sh` plus `uname`, `mkdir`, `cat`, `chmod`, `rm`, and `rmdir` on
  each remote host
- `claude` and/or `codex` on the remote `PATH`, with their native session
  metadata under the remote user's home directory

Build the three collector assets and the local executable:

```sh
go run ./cmd/ars-build
install -m 0755 ./ars ~/.local/bin/ars
```

`ars-build --assets-only` creates exactly these embedded collectors before any
local `ars` build: `darwin/arm64`, `linux/amd64`, and `linux/arm64`. Generated
collector blobs and the root `ars` build artifact are local build outputs and
must not be committed.

## Inventory

The default inventory is `${XDG_CONFIG_HOME}/ars/hosts` when
`XDG_CONFIG_HOME` is set, otherwise `~/.config/ars/hosts`. Put one OpenSSH
target on each line, in the order it should be reported:

```text
# ~/.config/ars/hosts
devbox
deploy@example.internal
agent-mac
```

Blank lines and comments are ignored. Targets may use names and aliases from
the user's SSH config. Duplicates, whitespace, control characters, a leading
dash, and targets over 255 bytes make the entire inventory invalid. `ars` does
not infer `localhost` or discover hosts.

`ars remote add <host>` creates the inventory and its parent directory when
missing, preserves existing comments, entries, and order, and rejects invalid
or duplicate targets. A target beginning with `#` is rejected because inventory
loading would interpret it as a comment. The command does not edit `~/.ssh/config`.

## Commands

The supported command forms are:

```sh
ars                    # search sessions from every configured host
ars devbox             # search one configured host
ars list --json        # return all hosts, sessions, and errors as JSON
ars remote add devbox  # add an SSH target to the ARS inventory
ars --help             # show all command forms
ars remote --help      # show remote command help
```

Interactive rows contain a private numeric index followed by display-only
metadata. `fzf` returns only that opaque index; titles, paths, and host names
are never parsed back into identity. Enter resumes the selected session.
Canceling `fzf` exits zero without starting the resume SSH connection. A
healthy result with no sessions prints `No sessions found.` and does not open
`fzf`.

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

## JSON schema version 1

Public JSON uses dedicated DTOs and always ends with a newline:

```json
{
  "schema_version": 1,
  "hosts": [
    {"target": "devbox", "status": "ok"},
    {"target": "down", "status": "error"}
  ],
  "sessions": [
    {
      "host": "devbox",
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
`hosts` records every selected inventory target exactly once. `status=ok` with
an empty `sessions` array distinguishes a healthy empty host from an
unreachable host. Stable error codes are `ssh_timeout`, `ssh_failed`,
`unsupported_target`, `protocol_error`, and `resource_limit`.

## SSH and failure behavior

Collection and resume deliberately use separate SSH modes:

- Collection is non-interactive (`BatchMode=yes`, no agent/X11/port
  forwarding), uses separate probe and upload invocations that each make one
  connection attempt, enforces existing host keys with
  `StrictHostKeyChecking=yes`, uses a 5-second connect timeout, and gives the
  full probe/upload/protocol flow 60 seconds per host. Unknown hosts fail; ars
  never accepts or rewrites `known_hosts` entries.
- Resume runs `ssh -tt` with the user's normal SSH configuration,
  authentication, and forwarding behavior. It executes only
  `cd <saved-cwd> && exec claude --resume <uuid>` or
  `cd <saved-cwd> && exec codex resume <uuid>`. Resume preserves the SSH exit
  code when available.

Hosts are collected with at most four workers. One failed host produces a
structured error while healthy peer sessions remain usable. If every selected
host fails, `ars` exits non-zero and never opens `fzf`. A healthy empty host is
success. Provider-level corrupt records can yield a partial provider result
without discarding its valid sessions.

## Bounds and cleanup

Collection is bounded and fail-closed:

- 64 KiB startup noise and 64 KiB per ARS/1 protocol line
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

The collector returns validated metadata only: provider, native UUID,
modification time, saved CWD, and native title. Prompts, responses, tool input
or output, credentials, raw transcript lines, provider source paths, and
filenames never cross the ARS/1 boundary. Host diagnostics are bounded and
sanitized before public output. The saved CWD and native title are still
potentially sensitive metadata, so treat JSON output and terminal history
accordingly.

## Verification

The complete automated release check is:

```sh
go run ./cmd/ars-build --assets-only
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/ars-build
```

The ephemeral loopback sshd integration is disposable and opt-in:

```sh
ARS_RUN_SSHD_INTEGRATION=1 go test ./internal/ssh -run TestEphemeralSSHDCollectsAndResumes -v
```

It generates temporary host/client keys, `authorized_keys`, `known_hosts`, and
configs, and does not modify a system sshd or persistent SSH configuration.
Before release, also use two explicitly configured real hosts to verify one
Claude and one Codex resume, an unreachable peer beside a healthy host, a
healthy empty host, fzf cancellation, and nonce-specific cleanup after an
interrupt.

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
