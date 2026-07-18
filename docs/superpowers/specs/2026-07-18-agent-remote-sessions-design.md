# Agent Remote Sessions Design

- Date: 2026-07-18
- Status: approved design pending written review
- Repository: `baleen37/agent-remote-sessions`
- Command: `ars`

## Purpose

`ars` gives the user one searchable list of existing Claude Code and Codex
root sessions across managed SSH hosts. Selecting a row resumes that native
session on its original host.

Only the local machine installs `ars`. A remote host needs an SSH server, a
POSIX shell, an executable temporary directory, and the provider CLI that
created the session. It does not need Python, a Go toolchain, `arsd`, or an
installed `ars` helper.

## V1 success criteria

- `ars` queries every configured host concurrently and opens one searchable
  TUI containing all natively resumable root/user sessions.
- `ars <host>` performs the same operation for one configured host.
- Claude Code and Codex sessions appear together, newest first.
- Internal Claude and Codex subagent transcripts do not appear.
- Each row shows host, provider, updated time, project or working directory,
  native title when available, and an abbreviated session ID.
- Enter resumes the selected session with its native provider command over an
  interactive SSH connection.
- Failure of one host does not hide results from healthy hosts. The command
  fails when no host returned sessions.
- Normal collection and failure paths remove the temporary remote helper and
  retain no remote `ars` service, configuration, cache, or index.

## Scope

Included:

- a plain managed-host inventory
- a bundled, one-shot Go metadata collector for three remote targets
- Claude Code and Codex root-session discovery
- concurrent multi-host aggregation
- an `fzf`-based local TUI
- native remote resume
- `ars list --json` for scriptable inspection

Excluded:

- `arsd` or any persistent remote installation
- Python or a Go toolchain as a remote prerequisite
- tmux, mosh, Tailscale, PTY attach, or relay implementations other than SSH
- starting, stopping, deleting, forking, or sending input to sessions
- worktrees, tasks, queues, conductor agents, and provider conversion
- prompt, response, tool-output, credential, environment-body, or source-path
  collection
- persistent local indexing, caching, and background refresh

V1 calls the action `resume`, not `attach`: it starts the provider's native
resume process for saved history and does not claim to reconnect to an already
running PTY.

## User interface

The host inventory is `~/.config/ars/hosts`. Each non-empty line is one SSH
target or alias; leading and trailing whitespace is ignored and lines beginning
with `#` are comments.

```text
# ~/.config/ars/hosts
devbox
user@agent-mac
```

Commands:

```text
ars                 query all hosts, pick a session, resume it
ars devbox          query one configured host, pick, and resume
ars list --json     print all normalized sessions without opening the TUI
```

`ars` invokes the system `ssh` executable so `~/.ssh/config`, ProxyJump, host
keys, and the user's authentication agent remain authoritative. It invokes the
system `fzf` executable for the TUI. These are local runtime prerequisites; the
repository supplies one fat `ars` binary.

An inventory entry is data, not an SSH option. `ars` rejects a target that
starts with `-` or contains whitespace or control characters before invoking
SSH.

The picker receives an opaque numeric row index followed by display columns.
After selection, `ars` maps the index back to the structured session object;
it never reparses display text. Escape cancels without resuming, and Enter
resumes exactly one row.

## Data model and boundaries

```go
type Session struct {
    Host       string
    Provider   string
    NativeID   string
    UpdatedAt  time.Time
    CWD        string
    Project    string
    Title      string
}
```

Provider handling is a fixed allowlist for `claude` and `codex`. It validates
collector records and constructs native resume commands; there is no plugin
loader or user-supplied executable name.

The relay boundary keeps session discovery and resume independent from SSH so
a later version can add another transport without changing the session model.
V1 has exactly one relay implementation, `SSHRelay`.

## Bundled Go collector

The collector source lives in this repository and uses only the Go standard
library. The release build cross-compiles it for exactly these V1 targets:

| Remote `uname` result | Embedded target |
| --- | --- |
| `Darwin arm64` | `darwin/arm64` |
| `Linux x86_64` or `Linux amd64` | `linux/amd64` |
| `Linux aarch64` or `Linux arm64` | `linux/arm64` |

The three binaries are compressed and embedded into the final local `ars`
binary. Generated helper artifacts are build inputs, not committed files. The
release build creates them in a temporary build area before compiling `ars` and
ships only the resulting `ars` binary.

The helper reads provider session files locally on the remote host and writes
only the bounded protocol described below. It never accepts a command or path
from the session data and never modifies provider files.

### Root-session discovery

Claude collection examines only direct files matching
`~/.claude/projects/<project>/*.jsonl`. It does not recurse into nested agent
directories. It reads `sessionId`, `cwd`, and the latest explicit `aiTitle` or
`agentName`; it does not derive a title from prompt text.

Codex collection examines saved rollout JSONL files under
`~/.codex/sessions`, reads their `session_meta` record, and excludes records
whose `thread_source` is `subagent`. A missing explicit title remains empty;
the helper does not copy or summarize a prompt into `title`.

Within one host, the helper deduplicates by `(provider, native_id)` and keeps
the newest record. File modification time supplies `updated_at` when native
metadata has no usable timestamp.

## Collection protocol

For each host, `SSHRelay` first runs a fixed `uname` probe over system SSH and
maps its result to one embedded target. Unsupported OS or architecture is a
host-local error.

The helper protocol begins with this version line:

```text
ARS-PROBE/1
```

Each following line is one JSON object with exactly the public metadata fields:

```json
{"provider":"claude","native_id":"id-1","cwd":"/work/app","title":"Fix login","updated_at":"2026-07-18T09:00:00Z"}
```

The protocol never contains a provider source path or a complete transcript
line. Local parsing ignores at most 64 KiB of remote startup noise while
looking for the version header, then accepts at most 16 MiB and 10,000 records
per host. An absent or unknown header, an oversized response, or malformed
framing fails that host. A malformed provider session file is skipped by the
helper and counted in a path-free stderr diagnostic.

Local code validates every record again, including the provider allowlist,
non-empty native ID and CWD, timestamp, field sizes, and total limits. It does
not execute or render raw record values without the relevant shell or display
escaping.

## Temporary helper lifecycle

Collection uses a fixed remote shell launcher. The launcher creates a private
directory with `umask 077` and `mktemp`, reports its exact path in one bounded
`ARS-TEMP/1` stderr control record, writes the selected helper from SSH stdin,
marks it executable, runs it once, and installs `EXIT`, `HUP`, `INT`, and
`TERM` cleanup traps before execution.

The local side applies a five-second SSH connect timeout and a 60-second total
collection timeout per host. On any failure after allocation, it also attempts
a second cleanup command for only the exact quoted path received in that
control record. It removes the helper with `rm -f` and the private directory
with `rmdir`; cleanup uses neither a glob nor recursive deletion.

The expected steady state is no remote `ars` file. A remote power loss or
`SIGKILL` between temporary-file creation and cleanup can still leave the
private temporary file behind; a disk-backed executable cannot make that case
impossible. V1 documents this crash-only limitation rather than claiming an
absolute guarantee.

An executable temporary directory is therefore a V1 prerequisite. A `noexec`
temporary mount produces a concise host-local error. V1 does not add a second
shell parser, interpreter dependency, or persistent fallback to bypass it.

Host collection uses a fixed small concurrency limit. Healthy results remain
usable when another host times out, rejects authentication, lacks a provider
directory, has an unsupported target, or returns invalid data. Diagnostics go
to stderr before the picker and never appear as fake session rows. There is no
local cache.

## Resume flow

After selection, `SSHRelay.Resume` gives the terminal to:

```text
ssh -tt <target> <fixed provider resume command>
```

The fixed remote command changes to the recorded CWD and then executes exactly
one provider operation:

```text
Claude Code: claude --resume <native-id>
Codex:       codex resume <native-id>
```

The CWD and native ID are single-quote escaped as data. Provider names never
become arbitrary executable names, and the client cannot supply an arbitrary
remote command. If the CWD no longer exists or the provider binary is
unavailable, SSH returns the native failure. `ars` does not choose another CWD
or session.

## Error handling

- Missing or empty host inventory: fail with the expected path and an example.
- Unknown `ars <host>` value: fail before opening SSH.
- Missing `ssh` or `fzf`: fail with the exact missing executable.
- Unsupported remote target or non-executable temp directory: fail that host.
- Some hosts fail: show concise host diagnostics and keep valid rows.
- All hosts fail or return no valid sessions: fail without opening `fzf`.
- Picker cancellation: exit successfully without starting SSH resume.
- Resume failure: preserve the SSH exit status when possible.

## Build and verification

Implementation follows strict RED-GREEN-REFACTOR. The build keeps collector
source separate from generated target artifacts and verifies that every
embedded target was produced from the current source. CI creates artifacts in
a temporary build area and checks the final fat binary; helper binaries are not
committed.

Required automated coverage:

- host parsing, duplicate rejection, target selection, and SSH option-injection
  rejection
- Claude direct/root fixtures and nested subagent exclusion
- Codex root/user fixtures and `thread_source=subagent` exclusion
- per-host deduplication and newest-record selection
- proof that source paths and sensitive transcript markers never reach stdout
- protocol header/noise recovery, NDJSON validation, and output/record limits
- target mapping for the three supported OS/architecture combinations
- helper upload, timeout, trap launcher, exact-path cleanup, and `noexec` errors
  using a fake `ssh` executable
- partial host failure, bounded aggregation, deterministic sorting, and race
  testing
- `fzf` opaque-index mapping, display sanitization, and cancellation
- shell-safe, provider-fixed resume commands for hostile CWD and ID values
- end-to-end CLI flow from inventory through selection to fake SSH resume
- cross-compiled helper execution against fixture homes where the runner can
  execute that target

Final commands include:

```bash
go test ./...
go test -race ./...
go vet ./...
```

The release build additionally produces and embeds `darwin/arm64`,
`linux/amd64`, and `linux/arm64` helpers, cross-builds the local `ars` targets,
and verifies that the fat binary contains exactly those helper entries.

Manual acceptance uses at least two SSH aliases with real Claude/Codex history:

1. Run `ars list --json` and confirm both hosts/providers are normalized.
2. Confirm internal subagent transcripts are absent.
3. Run `ars`, search by host/project/title, and resume one Claude session.
4. Confirm the remote Claude process resumes the exact native ID and CWD.
5. Repeat for Codex.
6. Make one host unreachable and confirm healthy sessions remain selectable.
7. Confirm the helper path is removed after success and forced failure.
8. Confirm no remote `ars` service, cache, socket, configuration, or installed
   binary exists.
