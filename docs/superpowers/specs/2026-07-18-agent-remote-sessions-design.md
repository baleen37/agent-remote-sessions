# Agent Remote Sessions Design

- Date: 2026-07-18
- Updated: 2026-07-19
- Status: approved for implementation
- Command: ars

## Goal and scope

ars shows Claude Code and Codex sessions from managed SSH hosts in one
searchable list. Selecting a session resumes it with the native provider CLI
on its original host.

V1 is one local ars binary with a plain SSH inventory. It discovers Claude
and Codex root/user sessions concurrently, exposes fzf and JSON views, resumes
through interactive SSH, and preserves healthy results when some hosts fail.

V1 excludes remote installation, daemons, caches, indexes, dynamic plugins,
tmux/mosh attach, task orchestration, provider conversion, and session
mutation. It never transfers prompts, responses, tool output, credentials, raw
transcript lines, or provider transcript paths.

Resume starts a new provider process from saved history. It does not attach to
a running terminal.

## Architecture

The project is one Go module and one release:

~~~text
cmd/
  ars/                 local CLI
  ars-collector/       one-shot remote helper

internal/
  app/                 inventory, aggregation, and orchestration
  session/             small validated metadata core
  provider/            Claude and Codex adapters
  protocol/            bounded helper wire format
  ssh/                 separate collect and resume paths
  output/              fzf and public JSON
~~~

Four boundaries keep changes local: provider adapters own unstable formats,
SSH owns remote execution, protocol carries only normalized metadata, and
output depends only on validated sessions and host results.

Collection and resume are separate because collection is bounded and
non-interactive, while resume needs a PTY and the user's normal SSH
environment. Providers are registered at compile time. Interfaces are added
only when a second implementation exists.

## CLI and data contract

The inventory is $XDG_CONFIG_HOME/ars/hosts, falling back to
~/.config/ars/hosts:

~~~text
devbox
user@agent-mac
~~~

ars rejects duplicate targets and targets that begin with -, contain
whitespace, or contain control characters. Each target is passed to system SSH
as one argv value.

~~~text
ars                 collect, pick, and resume
ars devbox          limit collection to one configured host
ars list --json     print structured results without fzf
~~~

The normalized model is:

~~~go
type Session struct {
    Host      string
    Provider  string
    NativeID  string
    UpdatedAt time.Time
    CWD       string
    Title     string
}
~~~

Project is derived from the basename of CWD. Session identity is
(host, provider, native_id).

The public JSON schema is independent from the helper protocol:

~~~json
{
  "schema_version": 1,
  "hosts": [{"target":"devbox","status":"ok"}],
  "sessions": [{
    "host":"devbox", "provider":"claude", "native_id":"...",
    "updated_at":"2026-07-18T09:00:00Z",
    "cwd":"/work/app", "title":"Fix login"
  }],
  "errors": [{"host":"agent-mac","code":"ssh_timeout","message":"..."}]
}
~~~

hosts distinguishes a healthy empty host from a failed host. Removing or
changing a public field requires a schema version increase.

fzf receives sanitized rows prefixed by opaque indexes. Selection maps the
index back to Session; display text is never parsed into a command. fzf is
required only for interactive use.

## Collection

ars probes uname -s and uname -m, selects an embedded collector, uploads it to
a private nonce-named directory, executes it once, validates the response, and
removes the exact helper and directory.

Supported remote targets:

| uname | Target |
| --- | --- |
| Darwin arm64 | darwin/arm64 |
| Linux x86_64 or amd64 | linux/amd64 |
| Linux aarch64 or arm64 | linux/arm64 |

Runtime limits:

| Limit | Value |
| --- | --- |
| Concurrent hosts | 4 |
| SSH connect timeout | 5 seconds |
| Total time per host | 60 seconds |
| Collector stdout | 16 MiB |
| Sessions per host | 10,000 |

Collection uses system SSH without a PTY and forces batch mode, no forwarding,
one connection attempt, and the connect timeout. Host-key verification stays
enabled; an unknown key fails with instructions to connect normally once.

The remote launcher uses umask 077, atomically creates the nonce directory
under TMPDIR or /tmp, and installs cleanup traps immediately. Secondary
cleanup accepts only the exact nonce-bearing path and never uses globs or
recursive deletion.

A power loss or SIGKILL may leave the private file behind. V1 documents this
instead of adding a daemon or janitor. A noexec temporary directory is a
host-local error.

## Provider adapters

Each provider owns its file walk, streaming parser, CLI availability check,
native-ID validation, and fixed resume rule. It returns sessions,
seen/skipped counts, and one status:

- absent: no provider history
- ok: collection completed, including zero eligible sessions
- partial: valid sessions plus skipped invalid input
- error: unavailable CLI, incompatible format, corruption, or resource limit

This prevents a provider format change from silently appearing as empty
history. Diagnostics contain counts and error codes, never session content or
source paths.

Claude:

- inspect direct ~/.claude/projects/<project>/*.jsonl files only
- exclude explicitly internal or sidechain sessions
- read session ID, latest CWD, and an explicit/native title when available
- never derive Title from prompt or message content

Codex:

- inspect session_meta under ~/.codex/sessions
- include only thread_source=user and source=cli or vscode
- exclude exec, subagent, missing, and unknown sources
- leave Title empty rather than reading a prompt preview

Both adapters stream input, use file modification time as UpdatedAt,
deduplicate by native ID, and emit only validated metadata. Sanitized fixtures
cover normal, internal, malformed, unknown, and empty histories.

If an official provider API later replaces file parsing, only that provider
adapter changes. Session, SSH, protocol, resume, and output remain stable.

## Protocol, validation, and resume

The local binary and collector come from the same release, so the private
protocol supports only its current major:

~~~text
ARS/1 BEGIN <nonce>
{"type":"session", ...}
{"type":"summary", ...}
ARS/1 END <nonce> <session-count>
~~~

A host result is accepted only when the nonce and count match, every bounded
UTF-8 JSON frame is valid, and collector and SSH both exit successfully.
Startup noise is allowed only before the matching begin frame and is bounded.
Unknown versions or record types fail that host.

Local validation requires a registered provider, a provider-valid UUID, an
absolute Unix CWD without control characters, bounded UTF-8 fields, and a
valid timestamp. Terminal controls are removed before fzf rendering.

Resume runs one fixed command through ssh -tt:

~~~text
Claude: cd <cwd> && exec claude --resume <uuid>
Codex:  cd <cwd> && exec codex resume <uuid>
~~~

Provider names never become executable names. CWD and UUID are validated and
shell-quoted as data. Resume preserves the user's normal SSH authentication
and forwarding configuration. ars never guesses an alternative CWD,
executable, or session.

Failure behavior:

- one host fails: report it and keep healthy sessions
- every host fails: return an error without opening fzf
- healthy hosts are empty: JSON succeeds; interactive mode reports no sessions
- picker cancellation: succeed without starting resume
- resume failure: preserve the SSH exit status when possible

Public JSON error codes are stable even when human messages improve.

## Maintenance and verification

The implementation plan owns exhaustive cases. The design requires:

1. sanitized provider fixture and contract tests
2. protocol nonce, truncation, count, limit, and fuzz tests
3. fake-SSH upload, timeout, quoting, and exact-cleanup tests
4. one ephemeral-sshd integration test
5. aggregation race and deterministic-sort tests
6. fzf index and cancellation tests
7. synthetic end-to-end list and resume tests

The release build cross-compiles the three collectors, embeds exactly those
targets, and runs:

~~~text
go test ./...
go test -race ./...
go vet ./...
~~~

Provider parser bugs start with sanitized fixtures. A new provider adds one
adapter, one fixed resumer, fixtures, and registry entries. Private protocol
versions move with the binary while public JSON stays backward compatible.
Daemons, caches, plugins, generic backends, and transport abstractions require
a demonstrated need.

Manual acceptance uses two SSH hosts, resumes one Claude and one Codex
session, verifies partial-host failure, and confirms helper cleanup after
success and forced failure.
