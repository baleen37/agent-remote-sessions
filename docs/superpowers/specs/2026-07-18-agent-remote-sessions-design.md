# Agent Remote Sessions Design

- Date: 2026-07-18
- Status: revised design pending written review
- Repository: baleen37/agent-remote-sessions
- Command: ars

## Purpose

ars gives the user one searchable list of existing Claude Code and Codex
root/user sessions across managed SSH hosts. Selecting a row resumes that
native session on its original host.

Only the local machine installs ars. A remote host needs an SSH server, a
POSIX shell, an executable temporary directory, and the provider CLI that
created the session. It does not need Python, a Go toolchain, arsd, or an
installed ars helper.

The design optimizes for maintenance through a modular monolith: one
repository, one release, one local binary, and compile-time provider adapters.
It does not introduce a daemon, plugin loader, local index, or provider-defined
commands.

## V1 success criteria

- ars queries every configured host concurrently and opens one searchable TUI
  containing all sessions that pass a known root/user compatibility adapter.
- ars <host> performs the same operation for one configured host.
- Claude Code and Codex sessions appear together, newest first.
- Internal Claude and Codex subagent transcripts do not appear.
- Each row shows host, provider, updated time, project or working directory,
  native title when available, and an abbreviated session ID.
- Enter resumes the selected session with its native provider command over an
  interactive SSH connection.
- Failure of one host or provider does not hide valid results from healthy
  hosts and providers.
- An unsupported provider file shape is reported as incompatible rather than
  silently presented as an empty session list.
- Normal collection and failure paths remove the temporary remote helper and
  retain no remote ars service, configuration, cache, or index.
- ars list --json exposes a versioned public schema that distinguishes valid
  sessions, healthy empty hosts, and host-local errors.

## Scope

Included:

- a plain managed-host inventory
- a bundled, one-shot Go metadata collector for three remote targets
- compile-time Claude Code and Codex compatibility adapters
- concurrent multi-host aggregation
- an fzf-based local TUI
- native remote resume
- ars list --json for scriptable inspection
- sanitized provider fixtures and compatibility manifests

Excluded:

- arsd or any persistent remote installation
- Python or a Go toolchain as a remote prerequisite
- tmux, mosh, Tailscale, PTY attach, or relay implementations other than SSH
- starting, stopping, deleting, forking, or sending input to sessions
- worktrees, tasks, queues, conductor agents, and provider conversion
- prompt, response, tool-output, credential, environment-body, raw transcript,
  or provider transcript-file-path transfer
- persistent local indexing, caching, and background refresh
- dynamic plugins, shared libraries, provider command configuration, or
  arbitrary remote commands
- runtime fallback after a selected provider backend fails

V1 calls the action resume, not attach: it starts the provider's native resume
process for saved history and does not claim to reconnect to an already
running PTY.

## Architecture and ownership

The stable core is separated from provider-owned compatibility code:

~~~text
cmd/
  ars/                    local CLI wiring
  ars-collector/          one-shot remote helper
  ars-build/              canonical release builder

internal/
  app/                    collection orchestration and exit behavior
  session/                provider-neutral model and validation
  inventory/              managed SSH host loading
  provider/
    registry.go           compile-time provider registration
    claude/               Claude discovery and resume rules
    codex/                Codex discovery and resume rules
  protocol/v1/            private helper framing and limits
  ssh/
    collect.go            batch SSH and helper lifecycle
    resume.go             interactive SSH resume
  output/
    json.go               public JSON schema
    fzf.go                display and opaque selection mapping

testdata/providers/
  claude/jsonl-v1/
  codex/rollout-v1/
~~~

Ownership rules:

- SSH code does not know Claude or Codex file formats.
- Provider adapters do not know SSH, aggregation, fzf, or public JSON.
- The private protocol never contains raw provider JSON or provider source
  paths.
- Output code receives only normalized, locally validated sessions and
  diagnostics.
- Collection and resume use separate concrete SSH packages because their PTY,
  authentication, timeout, forwarding, and terminal requirements differ.
- V1 does not create a generic transport framework. A transport interface is
  extracted only when a second real transport exists.

The provider registry is static and compiled into ars and ars-collector. Adding
a provider requires an explicit adapter, fixed resume rule, sanitized fixtures,
and a registry entry. There is no runtime plugin loader.

## User interface

The host inventory is:

~~~text
${XDG_CONFIG_HOME}/ars/hosts
~~~

If XDG_CONFIG_HOME is unset or empty, ars uses ~/.config/ars/hosts. A relative
XDG_CONFIG_HOME is ignored and the default is used. Each non-empty line is one
SSH target or alias; leading and trailing whitespace is ignored and lines
beginning with # are comments.

~~~text
# ~/.config/ars/hosts
devbox
user@agent-mac
~~~

Commands:

~~~text
ars                         query all hosts, pick a session, resume it
ars devbox                  query one configured host, pick, and resume
ars list --json             list all hosts without opening the TUI
ars list devbox --json      list one configured host
~~~

The word list is a reserved subcommand and cannot be used through the
ars <host> shorthand. A host alias named list remains addressable through
ars list list --json; V1 does not add a separate --host flag.

ars invokes the system ssh executable so ~/.ssh/config, ProxyJump, host keys,
and the user's authentication agent remain authoritative where the collection
and resume security contracts do not explicitly override them. It invokes the
system fzf executable only for interactive selection. ars list --json does not
require fzf.

An inventory entry is data, not an SSH option. ars rejects a target that starts
with -, contains whitespace or control characters, exceeds 512 bytes, or
duplicates an earlier trimmed entry. Duplicate detection is textual; distinct
aliases that resolve to the same remote account remain distinct inventory
entries. V1 accepts at most 256 hosts.

The picker receives an opaque numeric row index followed by sanitized display
columns. After selection, ars maps the index back to the structured session; it
never reparses display text. Escape cancels without resuming, and Enter resumes
exactly one row.

The public JSON output is independent from the private helper protocol:

~~~json
{
  "schema_version": 1,
  "sessions": [],
  "host_errors": []
}
~~~

Additive fields may be added within schema version 1. Removing a field,
changing its type, or changing its meaning requires a public schema version
increase.

## Session model and validation

The stable internal model contains only provider-neutral values:

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

Project is a display derivation, not stored provider data. Local code computes
it with path.Base(path.Clean(CWD)) because every V1 remote target uses Unix
paths.

Local code validates every collected session:

- Host must match the inventory entry that produced the record.
- Provider must be a compile-time registered value.
- NativeID must satisfy the provider's canonical UUID contract. A saved session
  name is never accepted as a NativeID.
- CWD must be an absolute, valid UTF-8 Unix path with no NUL or control
  characters and at most 4 KiB.
- Title is optional, valid UTF-8, at most 1 KiB, and is never executed.
- UpdatedAt must be a valid UTC timestamp.
- Identity and deduplication use (host, provider, native_id).

Display code replaces non-printing and terminal-format control characters
without mutating the raw validated CWD or title used by JSON output. JSON
encoding remains responsible for escaping printable data.

## Provider adapters

Provider discovery is deliberately isolated because Claude and Codex transcript
formats are not stable public contracts. Each adapter owns:

1. locating candidate files
2. determining root/user eligibility with positive rules
3. checking that the provider CLI is executable in non-interactive SSH PATH
4. streaming the minimum required metadata
5. producing normalized candidate sessions
6. returning content-free compatibility diagnostics

The adapter returns one state:

~~~go
type DiscoveryState string

const (
    DiscoveryAbsent      DiscoveryState = "absent"
    DiscoveryEmpty       DiscoveryState = "empty"
    DiscoveryUnavailable DiscoveryState = "unavailable"
    DiscoveryOK          DiscoveryState = "ok"
    DiscoveryPartial     DiscoveryState = "partial"
    DiscoveryUnsupported DiscoveryState = "unsupported"
    DiscoveryCorrupt     DiscoveryState = "corrupt"
)
~~~

Meaning:

- absent: the provider data root does not exist
- empty: the known format was found but contains no eligible session
- unavailable: resumable data exists but the provider CLI is not executable in
  the non-interactive SSH PATH
- ok: all relevant input was interpreted successfully
- partial: at least one session was emitted and some relevant input was
  malformed, oversized, or unknown
- unsupported: provider files exist but no known format can interpret them
- corrupt: a known format was detected but could not be read safely

Diagnostics contain only provider, adapter family, state, and bounded counts
such as files_seen, records_seen, sessions_emitted, records_rejected, and
unknown_shapes. They never contain a transcript line, prompt, CWD, title,
native ID, or provider source path.

Unknown fields may be ignored only after the adapter has positively identified
the required eligibility and identity fields. Unknown eligibility values fail
closed.

### Claude jsonl-v1

The Claude adapter examines only direct files matching
~/.claude/projects/<project>/*.jsonl. It does not recurse into nested agent
directories.

It streams each file and extracts:

- the session ID
- the latest valid CWD
- the latest explicit customTitle when present
- otherwise the latest aiTitle
- otherwise the latest agentName

It does not copy a first prompt, conversation summary, message, or tool output
into Title. A missing supported title remains empty. Direct-file placement is
necessary but not sufficient: records with an explicitly internal or sidechain
identity are excluded.

### Codex rollout-v1

The Codex adapter examines rollout JSONL files under ~/.codex/sessions and
reads only the session_meta record needed for identity and eligibility.

V1 includes a rollout only when:

- payload.thread_source is user
- payload.source is cli or vscode
- payload.id is a canonical UUID
- payload.cwd is a valid absolute Unix path

exec, subagent objects, missing source fields, and unknown source values are
excluded and counted. Title remains empty; the adapter does not read or
transfer a prompt preview.

For both providers, file modification time supplies UpdatedAt unless a future
adapter family has a documented native last-activity timestamp. Creation time
is not used as last activity. Within one host, the adapter keeps the newest
record for a duplicate (provider, native_id).

Provider input is streamed. V1 bounds candidate files at 100,000 per provider,
input lines at 1 MiB, emitted sessions at 10,000 per host, and total collector
runtime at 50 seconds. Crossing a bound produces partial, unsupported, or
corrupt according to whether valid sessions were already emitted.

## Bundled Go collector

The collector uses only the Go standard library and imports the same session,
provider, and protocol packages as the local binary where appropriate. The
release build cross-compiles it for exactly these V1 targets:

| Remote uname output | Embedded target |
| --- | --- |
| Darwin / arm64 | darwin/arm64 |
| Linux / x86_64 or amd64 | linux/amd64 |
| Linux / aarch64 or arm64 | linux/arm64 |

Target probing runs bounded uname -s and uname -m commands. Any extra fields or
unsupported values fail that host.

The three collector binaries are compressed and embedded into the final local
ars binary. Generated helper artifacts are temporary release inputs, not
committed files. go run ./cmd/ars-build owns cross-compilation, compression,
embedding, checksums, and final local builds. Ordinary provider,
protocol, and orchestration tests operate on source and sanitized fixtures
without requiring cross-built blobs.

The helper never accepts a provider path or command from session data, never
modifies provider files, and writes only the bounded private protocol.

## Private collection protocol

The helper protocol is private and lockstep: a local ars release always uploads
the collector built from the same commit. It does not retain old protocol
decoders indefinitely.

Each run begins with a locally generated 128-bit nonce:

~~~text
ARS-PROBE/1 <nonce>
{"type":"session","provider":"claude","native_id":"...","cwd":"/work/app","title":"Fix login","updated_at":"2026-07-18T09:00:00Z"}
{"type":"provider_summary","provider":"claude","adapter":"jsonl-v1","state":"ok","files_seen":2,"records_seen":10,"sessions_emitted":2,"records_rejected":0,"unknown_shapes":0}
ARS-END/1 <nonce> <session-count>
~~~

Local parsing:

- ignores at most 64 KiB of startup noise while looking for the exact
  nonce-bearing header
- accepts only UTF-8 NDJSON record types session and provider_summary between
  header and end
- requires the matching end nonce, declared session count, collector exit 0,
  and SSH exit 0 before accepting any host result
- rejects an unknown major version or record type
- accepts at most 64 KiB per protocol line, 16 MiB stdout, 1 MiB stderr, and
  10,000 sessions per host
- drains stdout and stderr concurrently; crossing a bound marks the host
  failed rather than blocking the child process

An optional JSON field may be added within protocol major 1. A required field,
record meaning, or framing change creates protocol/v2. Because client and
helper move in lockstep, protocol/v2 does not require permanent v1 fallback.

## SSH collection

Collection uses system ssh without a PTY and explicitly owns its batch
behavior:

~~~text
-T
-a
-x
-o BatchMode=yes
-o ClearAllForwardings=yes
-o ConnectionAttempts=1
-o ConnectTimeout=5
-o PermitLocalCommand=no
-o RemoteCommand=none
-o StdinNull=no
~~~

Unknown host keys therefore fail collection without prompting. The diagnostic
instructs the user to connect with normal ssh once to review and accept the
host key. ars never disables known-host verification.

The target is passed as one argv element after all fixed options. Inventory
validation prevents it from becoming an SSH option. The remote launcher and
resume command are fixed program text; data values use one audited POSIX
single-quote encoder.

Host collection uses min(4, configured hosts) workers. Each host has a
five-second SSH connect timeout and a 60-second total collection timeout.
Healthy provider results remain usable when another host or provider fails.
Host diagnostics are sorted in inventory order. Sessions sort by updated_at
descending, then host, provider, and native_id ascending.

## Temporary helper lifecycle

The local side creates a cryptographically random lowercase hexadecimal nonce.
The fixed remote launcher:

1. sets umask 077
2. requires TMPDIR, when set, to be an absolute path without control characters
3. atomically creates ${TMPDIR:-/tmp}/ars-<nonce> with mkdir
4. installs EXIT, HUP, INT, and TERM cleanup traps immediately after mkdir
5. reports ARS-TEMP/1 <nonce> <exact-directory> on bounded stderr
6. writes the selected helper from SSH stdin into that directory
7. marks it executable and runs it once

The local side accepts the temp control record only when the nonce matches and
the absolute directory has the exact ars-<nonce> final component. After
allocation, any local failure triggers a separately timed cleanup command for
only:

~~~text
<directory>/collector
<directory>
~~~

Cleanup uses rm -f for the exact helper followed by rmdir for the exact private
directory. It never uses a glob or recursive deletion. The helper also attempts
to unlink its own executable after startup; failure to self-unlink is
non-fatal because the shell trap remains authoritative.

A remote power loss or SIGKILL between allocation and cleanup can leave the
private directory or file behind. V1 documents this crash-only limitation
rather than adding a daemon or stale-file janitor. A noexec temporary mount is
a concise host-local error.

## Resume flow

After selection, resume gives the terminal to system SSH:

~~~text
ssh -tt -o RemoteCommand=none <target> <fixed-provider-command>
~~~

Resume intentionally preserves the user's normal authentication agent and
forwarding settings because the resumed coding agent may need the same Git and
network access as an ordinary interactive SSH session.

The fixed remote command changes to the validated absolute CWD and executes
exactly one registered provider operation:

~~~text
Claude Code: claude --resume <canonical-uuid>
Codex:       codex resume <canonical-uuid>
~~~

Provider names never become executable names. Native IDs and CWDs are encoded
as data with the audited POSIX single-quote function. Provider ID validation
also prevents option injection. If the CWD no longer exists or the provider
binary is unavailable in non-interactive SSH PATH, the native command fails.
ars does not choose another CWD, shell, provider binary, or session.

## Error and exit behavior

- Missing or empty inventory: configuration error with expected path and an
  example.
- Unknown ars <host>: configuration error before SSH.
- Missing ssh: error for every command.
- Missing fzf: error only for interactive selection.
- unavailable, unsupported, or corrupt provider with another provider healthy:
  report the provider diagnostic and keep healthy sessions.
- Partial host failure: return valid sessions and include host_errors in JSON.
- Every host failed: fail without opening fzf.
- At least one host collected successfully but all were empty: JSON succeeds
  with an empty sessions array; interactive mode exits without opening fzf and
  reports that no sessions were found.
- Picker cancellation: success without starting SSH resume.
- Resume failure: preserve the SSH or remote command exit status when possible.

Public error objects use stable codes such as ssh_timeout,
unsupported_platform, provider_unsupported, protocol_invalid, and
provider_unavailable. Human messages may improve without changing those codes.

## Compatibility and future evolution

Each adapter family has sanitized fixtures and a manifest containing:

- adapter family name
- provider CLI versions on which the shape was observed
- fixture update date
- positive root/user eligibility rules
- known limitations

A provider parser change must add a reproducing fixture, demonstrate unchanged
behavior against existing fixtures, or explicitly document dropped
compatibility. COMPATIBILITY.md records the adapter families and provider
versions verified by each ars release.

When a stable official provider API becomes suitable, the change remains
inside that provider directory:

1. implement the official acquisition path with the same normalized session
   and diagnostic contract
2. compare it with the file adapter on sanitized parity fixtures
3. verify root/user eligibility, last-activity semantics, and privacy
4. enable it explicitly for testing
5. make it the default only after contract parity is demonstrated
6. remove the file adapter after a documented compatibility period

Unsupported capability may fall back to the file adapter. A supported official
API that fails does not silently fall back, because that would hide regressions
and produce ambiguous deduplication.

V1 does not pre-create a generic backend interface. The provider package
extracts one when a second real acquisition path exists.

Adding a new provider requires:

1. a collector adapter
2. canonical ID validation and a fixed resumer
3. sanitized root, internal, malformed, and empty fixtures
4. provider contract and privacy tests
5. compile-time registry entries

## Build and verification

Implementation follows RED-GREEN-REFACTOR. Required automated coverage:

- host parsing, textual duplicate rejection, reserved list behavior, bounds,
  XDG fallback, and SSH option-injection rejection
- canonical Session validation and provider registry contract tests
- Claude root, nested/internal, title precedence, latest-CWD, malformed, and
  unsupported fixtures
- Codex user/cli, user/vscode, exec, subagent, malformed, and unsupported
  fixtures
- absent, empty, unavailable, ok, partial, unsupported, and corrupt provider
  states
- proof that raw transcript lines, provider source paths, and sensitive markers
  never reach protocol or public JSON
- protocol header, nonce, end count, unknown type, truncation, startup noise,
  stdout/stderr/line/record limits, golden tests, and decoder fuzzing
- deterministic deduplication, sorting, bounded aggregation, and race testing
- temp allocation, immediate trap installation, upload, timeout, exact cleanup,
  noexec behavior, and hostile temp-control records using fake SSH
- one Linux ephemeral-sshd integration covering upload, execute, disconnect,
  timeout, and cleanup
- fzf opaque-index mapping, display sanitization, lazy prerequisite, and
  cancellation
- provider-fixed resume commands for hostile values and canonical UUIDs
- end-to-end JSON flow against a synthetic remote home without provider
  authentication
- cross-compiled helper execution where CI has a native or emulated runner for
  that target

Final source checks:

~~~text
go test ./...
go test -race ./...
go vet ./...
~~~

The release build additionally:

1. cross-compiles and compresses darwin/arm64, linux/amd64, and linux/arm64
   collectors from the current source
2. embeds exactly those three assets
3. records and verifies their checksums
4. cross-builds supported local ars targets
5. scans sanitized fixtures and release artifacts for forbidden sensitive
   markers

Manual acceptance uses at least two SSH aliases with real Claude/Codex history:

1. Run ars list --json and confirm both hosts/providers and diagnostics.
2. Confirm internal subagent and exec transcripts are absent.
3. Resume one Claude session and verify exact native ID and CWD.
4. Resume one Codex session and verify exact native ID and CWD.
5. Make one host unreachable and confirm healthy sessions remain selectable.
6. Confirm unknown provider shapes are reported, not shown as empty.
7. Confirm helper paths are removed after success, provider failure, timeout,
   and forced disconnect.
8. Confirm no remote ars service, cache, socket, configuration, or installed
   binary exists.
