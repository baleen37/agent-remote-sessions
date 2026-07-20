# ARS TUI and Persistent Attach Design

- Date: 2026-07-20
- Status: approved
- Command: `ars`

## Goal

`ars` is a full-screen Claude Code and Codex session navigator for the current
computer and configured SSH hosts. Every managed computer shows the same
canonical sessions. The current computer is rendered as `local`; peers use
their configured host target.

Selecting a session starts or attaches to a provider process in an ARS-owned
tmux server. `Ctrl+Q` detaches the terminal client, keeps the provider running
on its original host, and returns to the same ARS TUI.

The design keeps one local ARS binary, one-shot remote collection, system
OpenSSH, provider adapters, bounded metadata, and visible partial failure. It
does not add an ARS daemon, database, cache, host discovery, or polling.

## TUI

`ars` and `ars <host>` open one alternate-screen TUI. `ars list --json` remains
a headless one-shot command.

~~~text
ars                              3 active · 9 recent · 4 hosts

Active
> ✻ test-connection-check            claude  local          ars          attached(1)  1d
  ✻ Full 배치 문제 해결              codex   search-server  search-data  running      2d

Recent
  ∙ Tmux 베스트프렉티스 조사         claude  server         dotfiles     saved        2d
  ∙ korean language support          codex   macbook        ars          saved       51d

~/dev/wooto/agent-remote-sessions · 123e4567-e89b-42d3-a456-426614174000

↑↓/jk move   / search   enter attach   r refresh   q quit
~~~

Each session is one row:

~~~text
state | title | provider | local/host | project | runtime | activity age
~~~

- Active contains an ARS tmux runtime; attached sorts before running.
- Recent contains provider history without an ARS runtime; newest sorts first.
- The selected row footer shows full CWD, native ID, and exact update time.
- A missing native title falls back to `project · <short-native-id>`; ARS never
  derives a title from prompt or response content.
- Narrow terminals remove project, provider, and client count in that order.
  Title, location, runtime, and activity remain visible.

Keys are deliberately small:

- `Up`, `Down`, `j`, `k`: move
- `/`: edit search
- `Enter`: start or attach
- `r`: refresh
- `q`, `Ctrl+C`: exit ARS
- `Ctrl+Q`: detach only inside an attached ARS tmux client

Search is a Unicode-aware, case-insensitive substring match over title,
provider, rendered location, project, CWD, and native ID. It does not rank
fuzzy matches. Refresh preserves the selected canonical session when present.

Color is limited to selection and state: a subtle cyan selected row, green
Active/attached, yellow running, dim neutral Recent/saved/time, and red errors.
Provider and local/remote text use the normal foreground. Icons and labels keep
the UI readable without color; light, dark, reduced-color, and `NO_COLOR`
rendering remain supported.

Bubble Tea v2 owns input, resize, alternate-screen, and terminal restoration.
Lip Gloss v2 is used only for these small adaptive styles. The design adds no
widget library, tree, table, mouse UI, modal, preview, theme, or animation.

## Identity and topology

Every computer uses the same roster:

~~~text
# ~/.config/ars/hosts
macbook
search-server
user@agent-host
~~~

Each computer explicitly identifies its own roster entry:

~~~text
# ~/.config/ars/local-host
macbook
~~~

`ars local set <configured-host>` writes the machine-local value and accepts
only an exact entry from `hosts`. ARS does not guess from OS hostnames, SSH
aliases, DNS, VPNs, user names, or interfaces.

~~~text
NodeKey    = exact configured host target
SessionKey = (NodeKey, provider, native ID)
RuntimeKey = hash(provider, native ID) within that node's ARS tmux server
~~~

`local` is presentation only. It is never stored as identity or parsed back
into routing. Public JSON continues to use the canonical host target.

## Boundaries and flow

~~~text
internal/app       CLI modes and collect/attach use cases
internal/tui       Bubble Tea state, filter, viewport, rendering
internal/runtime   ARS tmux inspect, create, and attach
internal/provider  Claude and Codex discovery and launch specs
internal/ssh       remote collection and interactive attach transport
internal/session   canonical identity and metadata
internal/protocol  bounded version-matched collector protocol
internal/output    public JSON only
~~~

Only `internal/tui` imports Bubble Tea and Lip Gloss. tmux is the only runtime,
so no generic terminal-backend interface is added. The fzf implementation and
prerequisite are removed.

The TUI data flow is:

~~~text
collect local directly + collect peers through SSH
→ merge canonical sessions and runtime state
→ filter and render
→ start-or-attach selected runtime
→ hand terminal to local tmux or ssh -tt
→ Ctrl+Q, provider exit, SSH loss, or error
→ restore TUI and collect again
~~~

Collection runs at startup, on `r`, and after attach returns. Concurrent
refreshes coalesce and stale generations cannot replace newer results. There
is no timer, watcher, or cache.

Local and remote collection use the same provider adapters. The remote
collector additionally returns normalized tmux state only for discovered
provider sessions:

~~~text
saved     no matching ARS runtime
running   runtime with zero attached clients
attached  runtime with one or more attached clients
~~~

Runtime metadata is limited to state, client count, and start time. The private
collector protocol gets a new major for its changed frames and remains
version-matched to the embedded helper. Public JSON v1 remains the compatible
provider-history view and does not add live TUI fields.

## Persistent attach

Every node must provide tmux. ARS uses a dedicated versioned, per-user tmux
server with a stable socket environment, separate from the user's normal tmux
server and configuration. Local and SSH access as the same OS user reach the
same runtime.

At selection time ARS:

1. checks for the exact hashed runtime key
2. creates it detached in the saved CWD with the adapter's fixed provider
   resume command when absent
3. handles concurrent creation by rechecking the exact runtime instead of
   starting a duplicate process
4. reapplies a prefix-free `Ctrl+Q -> detach-client` binding
5. attaches the current terminal and detaches any previous client

Creation and attach are separate; detached creation does not use
`new-session -A`, which may try to attach in a non-terminal path when the
session already exists.

Bubble Tea releases the terminal while local tmux or `ssh -tt` owns it, then
restores the same TUI. `Ctrl+Q` is reserved inside the ARS tmux server and is
not delivered to Claude or Codex. A peer attaching the same runtime returns the
previous client to its TUI without stopping the provider.

Provider exit removes the normal tmux session while native history remains in
Recent. ARS does not adopt provider processes started outside its tmux server.
Host reboot or tmux loss ends the live process but not native history.

## Failure, privacy, and migration

- Failed nodes and providers appear as bounded errors below the list; healthy
  local and remote sessions remain usable.
- Missing tmux is a runtime capability error, not a healthy empty result.
- Attach failure returns to the TUI. If all nodes fail, refresh and quit remain
  available.
- Non-TTY interactive invocation points to `ars list --json`.
- Collection stays bounded and non-interactive. Attach keeps normal OpenSSH
  authentication, configuration, and host-key verification.
- ARS transfers provider, native ID, update time, CWD, native title, and the
  small runtime fields only. It never transfers prompts, responses, tool data,
  credentials, raw transcripts, source paths, pane content, or terminal output.
- Existing histories become Recent. Existing foreground provider processes and
  user tmux sessions are not adopted.
- Users add the current node to the common roster and run `ars local set` once
  per machine. The first selection creates its ARS runtime.

## Verification and success

Automated checks cover TUI navigation, search, resize, narrow and colorless
rendering, selection preservation, refresh coalescing, local/remote routing,
runtime state, exact/racing creation, tmux isolation, terminal restoration,
partial failure, and unchanged JSON v1. PTY and SSH integration verifies that
`Ctrl+Q` preserves the provider PID and returns to the same TUI. Full tests,
race, vet, generated collectors, and ephemeral sshd remain release gates.

Real two-host acceptance is separate from automated proof. On different hosts:

~~~text
A: ars → start Claude → Ctrl+Q → same TUI, same provider PID
B: ars → same canonical session under host A → attach same PID
A: previous client returns to its TUI without stopping the provider
A or B: Ctrl+Q → refreshed list shows the runtime under Active
~~~

The live gate also covers Codex, relative `local` rendering, network loss,
partial host failure, and unchanged user tmux state. Simulated proof must never
be reported as real-host acceptance.
