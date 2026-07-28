# External tmux Reuse Design

- Date: 2026-07-28
- Status: approved
- Command: `ars`

## Problem

ARS currently reuses only provider processes that it started in the dedicated
`ars-v1` tmux server. A Claude Code or Codex session already running in a tmux
pane created by the user or another tool is ignored. Selecting the same native
session in ARS can therefore start a second provider process.

The creator of the tmux session is not relevant. Reuse is safe only when ARS
can prove that one live pane is running the selected `(provider, native ID)`.
CWD, title, pane name, and provider process name alone are not sufficient
identity.

## Goal

When a selected native session is already running through an explicit native
resume command in any standard per-user tmux server on that host, ARS will
attach to that pane instead of starting another provider process.

The first version must:

- preserve any clients already attached to the external tmux session
- avoid changing the external server's keys, options, status line, or sessions
- retain the current ARS-owned runtime behavior when there is no exact match
- fail closed instead of choosing among multiple exact matches
- behave the same for local and SSH hosts

## Non-Goals

- Inferring identity from CWD, title, modification time, pane content, or a
  provider process without a native ID in its command
- Detecting a fresh session started as bare `claude` or `codex`
- Discovering sockets created with an arbitrary `tmux -S <path>` outside the
  standard per-user tmux socket directory
- Showing external runtimes under `Active`
- Routing preview, send, or kill actions to external panes
- Installing Claude Code or Codex hooks
- Persisting an external tmux locator in the cache or private protocol
- Changing public JSON

## Design

### Exact identity

ARS will recognize only these live process commands:

```text
claude --resume <selected-native-id>
codex resume <selected-native-id>
```

The executable may be resolved through a path, but the provider command and
native ID must match exactly. Commands with an absent ID, a different ID, or
extra ambiguous shell text do not match.

ARS will enumerate tmux panes and the current user's host processes, then walk
parent process relationships from the matching provider process to a pane PID.
One matching pane is reusable. Zero matching panes means no external runtime.
Multiple matching panes are a conflict.

The scan reads metadata only. It never captures pane content or provider
transcripts.

### Bounded tmux discovery

The resolver will inspect Unix tmux sockets in the standard `tmux-<uid>`
directories under `${TMUX_TMPDIR:-/tmp}` and `/tmp`. Socket traversal and
command output use fixed limits. Non-socket entries, symlinks, and entries not
owned by the current user are skipped.

Every eligible socket must be inspected successfully before ARS may accept a
unique match or create a new runtime. A query failure or malformed tmux/process
row makes the resolver fail closed because the missing data could hide another
matching pane.

This includes the normal default server and servers created with `tmux -L`.
Arbitrary `tmux -S` paths remain out of scope because discovering them would
require an unbounded filesystem or process-environment search.

### Attach order

Selection keeps the current priority:

1. If the exact hashed ARS-owned runtime exists, attach to it through the
   existing `ars-v1` path.
2. Otherwise, resolve the selected native ID against external tmux panes.
3. If exactly one external pane matches, attach to its exact
   `session:window.pane` target.
4. If no external pane matches, create and attach the normal ARS-owned runtime.
5. If multiple external panes match, return a visible conflict error and do
   not create another provider process.

Local and remote attach share the same bounded POSIX resolver fragment so their
identity and conflict rules cannot drift. The local path evaluates it through
the existing command runner. The remote path embeds the same fragment in the
quoted `ssh -tt` script.

### External client behavior

External attach clears nested tmux environment variables and targets the exact
pane. It does not pass `-d`, so clients already attached to that tmux session
remain connected.

ARS does not add its `Ctrl+Q` binding or status text to an external server.
Users detach with that server's existing tmux key binding, normally
`prefix d`, and return to the same ARS TUI.

The external target is resolved immediately before attach and is not persisted.
If the pane disappears after a unique match, attach fails and returns to ARS.
ARS does not fall back to native resume after that race because doing so could
start a duplicate provider process.

## Failure Behavior

- A missing tmux socket directory means there is no external match.
- Missing `tmux` retains the existing runtime capability error.
- An unavailable or malformed process table produces a bounded attach error;
  ARS does not guess.
- A malformed or inaccessible eligible tmux server returns a bounded resolver
  error and prevents both external attach and ARS runtime creation.
- Multiple exact matches return `external tmux conflict` and create nothing.
- A vanished unique target returns the underlying attach failure and creates
  nothing.
- A provider running without an explicit native resume ID is not matched, so
  the existing ARS-owned creation path remains unchanged.

## Verification

Automated tests must prove:

- exact Claude and Codex resume commands map through process ancestry to their
  tmux panes
- CWD, titles, provider names without IDs, different IDs, and shell text do not
  match
- one unique external match is attached without creating an ARS runtime
- external attach omits `-d`, `Ctrl+Q`, status, and server-option mutations
- an existing external client remains attached
- zero matches use the existing ARS create-and-attach path
- multiple exact matches fail without creating or attaching
- an existing ARS-owned runtime keeps priority over an external match
- a vanished external target fails without native-resume fallback
- local and remote scripts use the same resolver fragment
- socket, pane, process, output, and traversal limits are enforced
- existing ARS runtime, PTY restoration, disposable tmux, and ephemeral SSHD
  tests remain green
- full Go test, race, vet, build, and npm test suites pass

Live verification must use a disposable external tmux server and a fake
provider process with a canonical native ID. It must prove that ARS attaches to
the existing provider PID, preserves an already attached client, returns to the
same TUI after normal tmux detach, and leaves the external server's keys,
options, and sessions unchanged.
