# Agent Remote Sessions Design

- Date: 2026-07-18
- Status: approved design pending written review
- Repository: `baleen37/agent-remote-sessions`
- Command: `ars`

## Purpose

`ars` gives the user one searchable list of existing Claude Code and Codex
sessions across managed SSH hosts. Selecting a row resumes that native session
on its original host.

Only the local machine installs `ars`. Remote hosts need SSH, a POSIX shell,
standard file utilities, and the provider CLI that created the session. There
is no `arsd`, remote package, service, cache, socket, or persistent script.

## V1 success criteria

- `ars` queries every configured host concurrently and opens a searchable TUI.
- `ars <host>` performs the same operation for one configured host.
- Claude Code and Codex sessions appear together, newest first.
- Each row shows host, provider, updated time, project or working directory,
  native title when available, and an abbreviated session ID.
- Typing filters the rows; Enter resumes the selected session with its native
  provider command over an interactive SSH connection.
- Failure of one host does not hide results from healthy hosts. The command
  fails when no host returned sessions.
- Querying and resuming leave no `ars` state on remote hosts.

## Scope

Included:

- a plain managed-host inventory
- one-shot SSH metadata collection
- Claude Code and Codex session discovery
- concurrent multi-host aggregation
- an `fzf`-based local TUI
- native remote resume
- `ars list --json` for scriptable inspection

Excluded:

- `arsd` or any remote installation
- tmux, mosh, Tailscale, PTY attach, or relay implementations other than SSH
- starting, stopping, deleting, forking, or sending input to sessions
- worktrees, tasks, queues, conductor agents, and provider conversion
- prompt, response, tool-output, credential, or environment-body collection
- persistent local indexing, caching, and background refresh

V1 calls the action `resume`, not `attach`: it starts the provider's native
resume process for saved history and does not claim to reconnect to an already
running PTY.

## User interface

The host inventory is `~/.config/ars/hosts`. Each non-empty line is an SSH
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
system `fzf` executable for the TUI. These are local runtime prerequisites;
only the single `ars` binary is supplied by this repository.

The picker receives an opaque numeric row index followed by display columns.
After selection, `ars` maps the index back to the structured session object;
it never reparses display text. Escape and Enter keep their normal `fzf`
semantics: Escape cancels without resuming, and Enter resumes one row.

## Data model

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

`Provider` is a small static adapter used to parse records and construct a
fixed resume command. V1 registers only `claude` and `codex`; there is no plugin
loader.

```go
type Provider interface {
    Name() string
    Parse(ProbeRecord) (Session, error)
    ResumeCommand(Session) (name string, args []string, err error)
}
```

The relay boundary keeps provider parsing independent from SSH without adding
configuration for relays that do not exist yet.

```go
type Relay interface {
    Collect(context.Context, Host) ([]ProbeRecord, error)
    Resume(context.Context, Host, Session) error
}
```

V1 has one implementation, `SSHRelay`.

## Collection flow

For each selected host, `SSHRelay.Collect` runs:

```text
ssh -T -o BatchMode=yes -o ConnectTimeout=5 <target> sh -s
```

The embedded probe is written to stdin. It scans:

- Claude Code: `~/.claude/projects/**/*.jsonl`
- Codex: `~/.codex/sessions/**/*.jsonl`

The probe uses `find`, `grep`, `sed`, `tail`, `stat`, and `printf`. It extracts
and emits only NUL-delimited scalar fields: provider, source path, mtime,
native ID, CWD, and explicit title/name. It never emits a complete JSONL line;
this is important because a Codex `session_meta` record can also contain base
instructions. GNU and BSD `stat` forms are both supported.

Local Go code parses JSON and normalizes records. A malformed session file is
skipped and reported in the host diagnostic count; it does not abort the host.
Sessions are sorted by `UpdatedAt` descending, using source mtime only when the
provider metadata has no valid timestamp.

Host collection uses a small fixed concurrency limit. Each host has an
independent timeout and captured stderr. Healthy results remain usable when
other hosts time out, reject authentication, lack a provider directory, or
contain malformed files. Diagnostics are written to stderr before the picker;
they are not inserted as fake session rows.

No local cache is included in V1. Real latency will be measured before adding
state or a daemon.

## Resume flow

After the user selects a session, `SSHRelay.Resume` gives the terminal to:

```text
ssh -tt <target> <fixed provider resume command>
```

The remote command changes to the recorded CWD and then executes exactly one
of these provider operations:

```text
Claude Code: claude --resume <native-id>
Codex:       codex resume <native-id>
```

Host, CWD, and session ID are shell-quoted as data. Provider names never become
arbitrary executable names, and the client cannot supply an arbitrary command.
If the CWD no longer exists or the provider binary is unavailable, SSH returns
the native failure to the user. `ars` does not silently choose another CWD or
session.

## Error handling

- Missing or empty host inventory: fail with the expected path and an example.
- Unknown `ars <host>` value: fail before opening SSH.
- Missing `ssh` or `fzf`: fail with the exact missing executable.
- Some hosts fail: show concise host diagnostics and continue with valid rows.
- All hosts fail or return no valid sessions: fail without opening `fzf`.
- Picker cancellation: exit successfully without starting SSH resume.
- Resume failure: preserve the SSH exit status when possible.

## Verification

Implementation follows test-first development. Required automated coverage:

- host-file parsing, comments, duplicates, empty inventory, and host selection
- Claude and Codex metadata fixtures without prompt-body fallback
- six-field NUL-framed probe parsing and malformed-record isolation
- BSD/GNU stat fallback in the embedded probe
- SSH collection argv and probe stdin using a fake `ssh` executable
- bounded aggregation, partial host failure, sorting, and race testing
- `fzf` row-index mapping and cancellation using a fake `fzf` executable
- shell-safe, provider-fixed resume argv for hostile CWD and ID values
- CLI flow from host inventory through selection to fake SSH resume
- `go test ./...`, `go test -race ./...`, `go vet ./...`, and cross-builds for
  macOS and Linux

Manual acceptance uses at least two SSH aliases with real Claude/Codex history:

1. Run `ars list --json` and confirm both hosts/providers are normalized.
2. Run `ars`, search by host/project/title, and select one Claude session.
3. Confirm the remote Claude process resumes the exact native ID and CWD.
4. Repeat for Codex.
5. Make one host unreachable and confirm healthy sessions remain selectable.
6. Confirm no `ars` binary, service, cache, socket, or script remains remotely.
