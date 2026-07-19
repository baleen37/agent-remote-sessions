# ARS Remote Add Design

- Date: 2026-07-19
- Status: approved
- Command: `ars remote add <host>`

## Goal and scope

`ars remote add <host>` adds one SSH target to the existing ARS host inventory.
It does not edit `~/.ssh/config`, test the SSH connection, or add `remote list`
or `remote remove` commands.

`ars --help` and `ars remote --help` print command usage to standard output and
exit successfully without reading the inventory or collecting sessions.

## Behavior

The command resolves the same `$XDG_CONFIG_HOME/ars/hosts` path used by the
existing inventory loader, falling back to `~/.config/ars/hosts`. It validates
the target with the existing inventory rules, creates the parent directory and
file when absent, and appends the target as one line while preserving existing
comments, entries, and order.

If the existing file does not end in a newline, the command inserts one before
the new target. A target already present as an active inventory entry fails
with an `already configured` error and leaves the file unchanged. Invalid
targets and unreadable or unwritable inventory files also fail without starting
collection.

The implementation extends the current explicit argument parser and inventory
module rather than adding a CLI framework or a new configuration abstraction.

## Help

Top-level help documents the existing interactive and JSON commands plus the
new command:

```text
ars [host]
ars list --json
ars remote add <host>
ars remote --help
```

Remote help describes `ars remote add <host>`. Unsupported command shapes keep
returning usage errors.

## Verification

Tests cover:

- creating a missing inventory and parent directory
- appending to files with and without a trailing newline
- preserving existing comments and entries
- rejecting duplicate and invalid targets without changing the file
- top-level and remote help succeeding without application dependencies
- routing `ars remote add <host>` without collection
- preserving all existing command behavior

The final verification runs the focused tests, the full Go test suite with the
generated collector assets, the race-enabled suite, `go vet`, and direct help
output checks.
