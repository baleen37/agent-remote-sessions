# Implicit localhost Design

- Date: 2026-07-22
- Status: approved
- Command: `ars`

## Goal

The current computer is always available without configuration. Users configure
only SSH peers; they do not add the current computer to the remote inventory or
run a separate local-identity command.

ARS uses `localhost` as the canonical machine-readable identity for the current
computer. The TUI does not render that identity because the current computer is
the default context.

## Configuration and commands

`~/.config/ars/hosts` (or its XDG equivalent) contains only SSH targets. A
missing inventory means there are no configured peers and is valid.

The supported command forms are:

```text
ars
ars <host>
ars list --json
ars remote add <host>
```

- `ars` and `ars list --json` collect `localhost` plus every configured peer.
- `ars localhost` collects only the current computer.
- `ars <configured-peer>` collects only that peer.
- `ars remote add localhost` and a literal `localhost` inventory entry are
  rejected because the identity is reserved for the implicit current computer.
- `ars local set <host>`, `local-host`, and their help text are removed.
- An existing `local-host` file is ignored. ARS does not delete user files.

ARS does not infer identity from an OS hostname, SSH alias, DNS, VPN, user name,
or interface.

## Identity and routing

The topology loader prepends one host value with `Target: "localhost"` and
`Local: true`, followed by configured peers with `Local: false`.

`localhost` remains the session host identity used for deduplication, selection,
refresh preservation, direct collection, direct tmux attach, and JSON output.
The `Local` flag, not the target string, selects direct execution. Remote peers
continue to use one-shot collection and interactive attach through OpenSSH.

This deliberately replaces the previous cross-machine canonical mapping. On
each computer, its own directly collected sessions use `localhost`; configured
peers retain their SSH targets.

## TUI and JSON

For `localhost` sessions, the TUI location column is blank. Hidden `localhost`
text is not part of filtering. Local diagnostics omit a location prefix. The
header counts configured peers only, so a local-only screen reports zero hosts.
Remote session locations and diagnostics are unchanged.

Public JSON remains schema version 1 and represents the current computer
explicitly as `localhost`:

```json
{
  "hosts": [{"target":"localhost","status":"ok"}],
  "sessions": [{"host":"localhost"}]
}
```

This preserves a non-empty host identity for machine-readable consumers while
keeping the interactive surface free of a redundant local label.

## Failure behavior

Local collection failures remain visible and use `localhost` in JSON. A local
failure does not discard healthy peer sessions, and a peer failure does not
discard healthy local sessions. Missing or invalid peer inventory entries fail
closed as before, except that an entirely missing inventory is now a valid
local-only configuration.

## Verification

Focused tests must prove:

- missing and empty inventories produce exactly one implicit local host;
- configured peers follow `localhost` in inventory order;
- literal `localhost` peers are rejected;
- the removed local command is invalid and absent from help;
- default, local-only, and peer-only selection route correctly;
- local collection and attach execute directly while peers use SSH;
- local TUI rows have a blank location, cannot be found by `localhost`, and do
  not increase the displayed host count;
- JSON retains `localhost` identities;
- the existing Go, race, vet, build, PTY, SSH, and npm checks remain green.
