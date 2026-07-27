# Remote Codex PATH Independence Design

- Date: 2026-07-27
- Status: approved
- Command: `ars`

## Problem

ARS discovers and resumes Codex sessions by the native Codex CLI and its
`~/.codex/sessions` history. On an SSH peer, the collector and ARS-owned tmux
server inherit a non-interactive `PATH`, which can differ from the user's login
shell.

The verified `baleen` peer has:

- 26 Codex session files under `~/.codex/sessions`
- Codex CLI 0.144.6 at `/Users/baleen/.npm-global/bin/codex`
- no `.npm-global/bin` entry in either the non-interactive SSH `PATH` or the
  ARS tmux server `PATH`

The Codex provider currently calls `exec.LookPath("codex")` before reading
history, so the peer reports Codex as absent. Remote attach also passes the bare
`codex` executable directly to tmux, so it would fail under the same `PATH`.
Local Codex discovery works and must remain unchanged from the user's
perspective.

## Goal

When Codex is available from an SSH peer's login shell, ARS must:

- discover that peer's valid native Codex history without depending on the
  collector's non-interactive `PATH`
- resume a selected Codex session through the peer's login-shell environment
- keep provider command validation and shell quoting strict
- avoid host-specific executable paths or configuration

## Non-Goals

- Changing Claude discovery or attach behavior
- Adding provider executable paths to the public or private protocol
- Sourcing a specific shell startup file
- Adding host-specific PATH configuration
- Discovering Codex sessions outside `~/.codex/sessions`
- Changing session parsing, filtering, limits, ordering, cache, or TUI behavior

## Design

### 1. Treat native history as Codex discovery truth

The Codex adapter will stop requiring `exec.LookPath("codex")` before scanning
`~/.codex/sessions`.

The existing directory and record validation remains authoritative:

- a missing history directory still reports Codex as absent
- an invalid or inaccessible history directory keeps its current diagnostic
- only validated root user sessions are returned
- corrupt, incompatible, and resource-limit outcomes remain unchanged

This keeps collection read-only and avoids executing login-shell startup code
during every inventory refresh.

### 2. Use the peer's login shell only when attaching Codex

Remote Codex attach will create its tmux pane with one fixed shell command:

1. select `${SHELL:-/bin/sh}` from the peer environment
2. invoke it as a login shell with `-l -c`
3. `exec codex resume <validated-session-id>` inside that shell

Only the already validated Codex `ResumeSpec` is interpolated. Every executable
and argument remains POSIX single-quoted before it enters the login-shell
command. The tmux session name, working directory, and attach target keep their
existing validation and exact-target syntax.

Claude retains its existing macOS keychain guard. Local attach retains direct
execution because local ARS already inherits the interactive launch
environment.

No absolute provider path is sent over the protocol or persisted in cache.

## Failure Behavior

- If history exists but Codex is genuinely unavailable from the login shell,
  sessions remain discoverable but attach fails through the existing attach
  error path.
- A missing or empty `SHELL` falls back to `/bin/sh`; if that shell cannot find
  Codex, attach fails normally.
- Shell startup output is confined to the attached tmux pane.
- No fallback searches a list of guessed installation directories.

## Verification

Automated tests must prove:

- valid Codex history is discovered when `PATH` contains no Codex executable
- missing Codex history still reports the provider as absent
- remote Codex attach uses `${SHELL:-/bin/sh}` with login-shell flags
- the fixed `codex resume <id>` command remains fully quoted
- remote Codex attach does not gain the Claude keychain guard
- Claude and local attach commands remain unchanged
- focused provider and SSH tests pass
- the full Go test, race, vet, build, and npm test suites pass

Live verification against `baleen@baleens-macbook.ojos-in.ts.net` must prove:

- remote Codex sessions appear in `ars list --json`
- selecting one starts or attaches to the ARS-owned remote tmux session
- `Ctrl+Q` detaches back to ARS
