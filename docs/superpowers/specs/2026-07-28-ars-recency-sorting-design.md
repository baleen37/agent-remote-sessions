# ARS Recency Sorting Design

- Date: 2026-07-28
- Status: Approved

## Goal

Make the project tree order easy to predict from the activity timestamps shown
in the TUI. Both project groups and sessions use the same rule:

1. pinned items first;
2. newest activity first.

Runtime state remains visible but does not affect ordering.

## Scope

Change only the default ordering in `internal/tui/tree.go`.

Keep the existing project grouping, automatic folding, search, filters, pin
behavior, selection restoration, row rendering, and keyboard controls. Do not
add a sort menu, configuration, persisted preference, or new status text.

## Ordering

### Sessions within a project

Sort sessions by:

1. whether the session is pinned, descending;
2. `UpdatedAt`, descending.

The sort remains stable. Sessions with equal pin state and equal activity time
keep their input order.

### Project groups

Sort groups by:

1. whether any session in the group is pinned, descending;
2. the newest `UpdatedAt` across every session in the group, descending.

Pinned groups remain above unpinned groups. Within each tier, the group with the
newer activity appears first. Opening or closing a group does not change this
ranking because group mode is applied after sorting.

## Data Flow

`model.refreshVisible` continues to filter the inventory and passes the result
to `buildRows`. `groupSessions` groups the sessions and applies the two stable
sorts above. `buildRows` then applies the existing automatic open, partial, or
closed group mode without further reordering.

No session data, runtime state, or persistent state changes.

## Error Handling

There is no new input or failure path. A zero `UpdatedAt` remains a valid oldest
timestamp under the existing `time.Time` comparison.

## Verification

Automated tests must prove:

- a newer saved session appears before an older running session in the same
  project;
- a project with newer activity appears before an older active project;
- pinned sessions and pinned groups remain above newer unpinned items;
- pinned tiers retain newest-first ordering;
- equal timestamps retain stable input order;
- opening or closing a group does not change project order.

Run the focused TUI tests, the full Go test suite, `go vet ./...`, and a
production build. Finally, run the built TUI in tmux with representative
fixtures and capture the pane to confirm the rendered order matches
`pinned → recent`.
