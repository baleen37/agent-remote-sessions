# Agent Remote Sessions Design

- Date: 2026-07-18
- Status: approved design pending written review
- Repository: `baleen37/agent-remote-sessions`
- Commands: `ars`, `arsd`

## Purpose

`ars` is a provider-independent control surface for sessions that already exist
on managed hosts. It gives the user one searchable list of native Claude Code
and Codex sessions across every configured host, then resumes the selected
session on its original host.

The product discovers and opens sessions; it does not create, schedule, or own
them. This separates it from agent orchestrators that launch agents, create
worktrees, manage tasks, or supervise workers.

## V1 success criteria

- `ars` with no arguments queries every configured host in parallel.
- `ars <host>` restricts the same query to one configured host.
- Claude Code and Codex sessions from every responding host appear in one
  searchable picker, sorted by most recently updated.
- Each row shows host, provider, updated time, project or working directory,
  title, and native session ID.
- Pressing Enter opens an interactive connection to the selected host and runs
  the provider's native resume operation.
- An unreachable or incompatible host does not hide results from healthy
  hosts. The command fails only when no host can return a valid result.
- The remote agent runs continuously as the user and incrementally indexes
  native session metadata.
- V1 uses SSH as its only relay, while session and provider code remain
  independent of SSH.

## Product boundary

V1 is a federated native session index with resume. It includes no tmux
integration and does not attach to an existing interactive PTY.

Included:

- managed-host inventory
- remote user daemon installation and updates
- Claude Code and Codex native session collectors
- incremental metadata indexing
- multi-host aggregation
- searchable local picker
- native session resume
- SSH relay
- health and compatibility diagnostics

Excluded:

- tmux discovery or attach
- arbitrary PTY attach
- starting, stopping, restarting, or deleting sessions
- sending prompts or keystrokes
- worktrees, tasks, queues, and conductor agents
- cross-provider session conversion
- prompt and response body indexing
- relay implementations other than SSH
- a daemon TCP listener or central hosted control plane
- background self-update

`attach` remains a possible future session capability. It will be implemented
only when a provider exposes a native attach endpoint or `arsd` gains a PTY
broker. V1 does not simulate attach by starting a second process.

## Architecture

```text
local machine
┌──────────────────────────────────────────────────────────────┐
│ ars                                                          │
│  ├─ host inventory                                           │
│  ├─ bounded parallel aggregation                             │
│  ├─ unified fzf picker                                       │
│  └─ Relay                                                    │
│      └─ SSHRelay                                             │
└──────────────────────────┬───────────────────────────────────┘
                           │ versioned RPC / interactive exec
                           │
remote managed host        ▼
┌──────────────────────────────────────────────────────────────┐
│ arsd bridge ── user-only Unix socket ── arsd serve           │
│                                           ├─ Claude collector│
│                                           ├─ Codex collector │
│                                           └─ session index   │
└──────────────────────────────────────────────────────────────┘
```

The repository produces two binaries from one Go module:

- `ars`: local inventory, aggregation, picker, relay, installation, updates,
  and diagnostics
- `arsd`: remote collector daemon, RPC bridge, index query, and safe native
  resume entrypoint

Separate binaries make the trust boundary explicit. Local UI and host
credentials do not belong in the remote daemon, and provider parsers and
session data do not depend on a particular relay.

## Common session model

Remote collectors normalize provider records into the following model:

```go
type Session struct {
    Key        string
    Provider   string
    NativeID   string
    UpdatedAt  time.Time
    CWD        string
    Project    string
    Title      string
    Capabilities []Capability
}
```

- `Key` is stable within one host and derived from provider plus native ID.
- `Provider` is an adapter name such as `claude` or `codex`.
- `Capabilities` contains `resume` in v1 when the native record is resumable.
- Host name and relay configuration are not part of the remote session model.
  The local aggregator adds them when it builds a `RemoteSession`.
- The model does not claim that a session is running, waiting, or idle unless
  a provider supplies authoritative evidence. Stored history alone is
  reported as resumable history, not a guessed live state.

The provider boundary is intentionally small:

```go
type Provider interface {
    Name() string
    Roots() []string
    Parse(path string) ([]Session, error)
    ResolveResume(key string) (ExecSpec, error)
}
```

V1 registers Claude and Codex statically. A dynamic plugin SDK is not needed.

## Remote indexing

`arsd serve` scans these native sources:

- Claude Code: `~/.claude/projects/**/*.jsonl`
- Codex: `~/.codex/sessions/**/*.jsonl`

The daemon performs an initial scan and then polls every five seconds. It
tracks `(path, mtime, size)` and reparses only new or changed files. Removed
files remove their corresponding index entries.

The daemon stores only resumable metadata:

- provider and native session ID
- title or provider-generated name
- working directory and project
- last valid timestamp, with file modification time as fallback
- source fingerprint

It never stores prompt text, response text, tool output, credentials, or
environment variables. Titles are accepted only from native title or name
records; prompt bodies are not used as fallback titles.

The live index is held in memory and checkpointed as an atomic JSON snapshot.
The snapshot is disposable: an incompatible schema or corrupt file is deleted
and rebuilt from native sources. V1 does not require SQLite, bbolt, or fsnotify.

State paths:

- Linux: `$XDG_STATE_HOME/ars` or `~/.local/state/ars`
- macOS: `~/Library/Application Support/ars`

State directories use mode `0700`; the Unix socket and snapshot use user-only
permissions.

## Relay boundary

The local client depends on a transport-neutral interface:

```go
type Relay interface {
    OpenControl(ctx context.Context, host Host) (io.ReadWriteCloser, error)
    Resume(ctx context.Context, host Host, sessionKey string) error
}
```

V1 provides only `SSHRelay`:

```text
control: ssh -T <target> ~/.local/bin/arsd bridge
resume:  ssh -tt <target> ~/.local/bin/arsd resume --key <validated-key>
```

The implementation invokes the system `ssh` executable so existing SSH
aliases, host keys, ProxyJump settings, and authentication agents remain the
source of truth. `arsd` does not know that the byte stream arrived over SSH.

A future mosh, EternalTerminal, WebSocket, or custom relay must implement the
same control-stream and interactive-resume semantics. V1 contains no plugin
loader or speculative relay configuration beyond a `relay = "ssh"` field.

## Daemon protocol

`arsd serve` listens only on a user-owned Unix socket. `arsd bridge` connects
stdin and stdout to that socket so a relay can carry the protocol without
opening a network port.

V1 uses newline-delimited, versioned JSON requests and responses. JSON string
escaping makes embedded newlines safe while keeping the protocol inspectable.

Methods:

- `system.hello`: protocol version, daemon version, host ID, provider list,
  and capabilities
- `system.health`: index freshness, last scan result, and provider errors
- `sessions.list`: paginated session summaries ordered by update time
- `index.refresh`: request an immediate incremental scan

Protocol version and binary version are distinct. An incompatible protocol
produces an explicit update instruction; neither side guesses compatibility.

Resume does not accept a command, working directory, or executable from the
client. The client sends only a validated session key to `arsd resume`. The
remote binary resolves the current indexed record and executes a fixed
provider adapter:

```text
Claude: chdir(session.cwd); exec claude --resume <native-id>
Codex:  chdir(session.cwd); exec codex resume <native-id>
```

If the recorded directory no longer exists, the action fails with a clear
diagnostic. V1 does not silently resume from a different directory.

## Host inventory and CLI

The local configuration lives at `~/.config/ars/config.toml` or the platform
configuration directory equivalent:

```toml
[[hosts]]
name = "devbox"
relay = "ssh"
target = "devbox"

[[hosts]]
name = "agent-mac"
relay = "ssh"
target = "user@agent-mac"
```

The SSH `target` is passed to the system client and may be an alias defined in
`~/.ssh/config`. Host names are stable display and cache identities.

User-facing commands:

```text
ars                              all configured hosts, picker, resume
ars <host>                       one configured host, picker, resume
ars list [<host>] --json         machine-readable list without picker
ars host add <name> --target <ssh-target>
ars host list
ars host install <name>
ars host update <name>
ars host update --all
ars doctor [<host>]
```

`ars` uses local `fzf` for v1. If `fzf` is unavailable, `ars list` continues to
work and the interactive command prints an installation diagnostic. A custom
TUI is outside v1.

## Installation and service management

`ars host install <name>` performs a user-level SSH bootstrap:

1. Query remote operating system and architecture.
2. Select the matching `arsd` release artifact.
3. Download locally and verify the published SHA-256 checksum.
4. Upload to a temporary path over SSH.
5. Atomically replace `~/.local/bin/arsd`.
6. Install or update the user service.
7. Start the service and verify `system.hello` and the initial provider scan.

Service implementations:

- macOS: user LaunchAgent with `RunAtLoad` and `KeepAlive`
- Linux: systemd user service with `Restart=on-failure`

The service never runs as root. Linux systems without a usable systemd user
manager are reported as unsupported by the v1 installer rather than receiving
a second daemonization mechanism.

Updates are explicit. `ars host update` performs the same verified atomic
replacement and restarts the service. `arsd` does not download or update
itself.

## Failure handling

- Host queries use bounded concurrency and per-host timeouts.
- Unreachable, unauthenticated, unhealthy, or incompatible hosts appear as
  warnings while healthy results remain searchable.
- Malformed JSONL records are skipped and counted in provider health; they do
  not invalidate the rest of a file or host.
- A failed provider collector does not hide records from another provider on
  the same host.
- Resume re-resolves the selected key against the current daemon index so a
  stale local selection cannot inject an arbitrary command.
- Cancellation from `fzf` exits cleanly without opening a connection.

## Security

- V1 opens no daemon TCP port and makes no daemon-originated outbound network
  connection.
- SSH remains responsible for host authentication, encryption, host keys, and
  access policy.
- RPC exposes a fixed method allowlist and no arbitrary command execution.
- Provider and session identifiers are validated before lookup and execution.
- Resume uses direct argv execution rather than constructing a shell command.
- Provider executables are resolved during daemon health checks and stored as
  validated absolute paths.
- Titles and paths are stripped of control characters before local display.
- Logs exclude prompts, responses, credentials, session bodies, and auth data.
- Installation verifies release checksums before atomic replacement.

## Testing strategy

- Anonymized Claude and Codex JSONL fixtures verify metadata extraction,
  malformed-line tolerance, timestamp fallback, and title rules.
- Index tests verify fingerprint reuse, changed-file parsing, deletion, atomic
  snapshot replacement, and corrupt-cache rebuild.
- Protocol tests run `arsd bridge` against a temporary Unix socket and verify
  version negotiation, pagination, health, and invalid methods.
- Fake relay tests verify parallel host aggregation, timeouts, partial failure,
  version mismatch, and cancellation.
- Resume tests use stub executables to verify provider argv, working directory,
  terminal wiring, and rejection of unknown keys.
- Installer tests verify platform mapping, checksum failure, atomic upload,
  LaunchAgent generation, and systemd user unit generation without touching a
  real service manager.
- An end-to-end harness starts two temporary daemons with fixture indexes,
  aggregates both through a test relay, selects a session, and observes the
  correct stub resume process.

## Future extension points

The approved v1 leaves three explicit extension boundaries without
implementing them prematurely:

- new providers implement the internal `Provider` interface
- new relays implement the local `Relay` interface
- native attach or a future PTY broker may add an `attach` capability

Adding tmux, orchestration, destructive session management, or a central
control plane requires a new design review because those features change the
product boundary rather than merely adding an adapter.
