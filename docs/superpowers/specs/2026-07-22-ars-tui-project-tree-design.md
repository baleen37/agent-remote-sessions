# ARS TUI Project Tree Design

- Date: 2026-07-22
- Status: approved
- Command: `ars`
- Supersedes: the flat Active/Recent list structure decided in
  `2026-07-22-ars-tui-visual-balance-design.md`. That document's spacing,
  alignment, color-hierarchy, `NO_COLOR`, and rendering-boundary rules remain
  in force.

## Goal

Replace the flat Active/Recent session list with a project-grouped tree.
Each project is a collapsible group whose header shows the project name and
session count. Runtime state moves entirely to per-row markers and colors, so
the Active/Recent subheadings disappear.

## Grouping and ordering

Sessions group by `session.Project(CWD)`. Groups derive from sessions, so an
empty group never appears; when no sessions exist at all the list keeps the
existing single `none` line.

Groups containing at least one non-saved session sort first; within that,
groups order by their most recent session activity, descending. Inside a
group, non-saved sessions sort before saved ones, then by most recent
activity, descending.

## Layout

```
ars  2 active · 3 recent · 1 hosts

▾ wooto (3)
  ├─ ✻ work-44  claude  local        attached(1)  now
  ├─ ✻ work-07  claude  local        running      5m
  └─ ∙ hi       codex   dev@remote   saved        2h
▸ blog (2) ✻
```

- A group header renders as `▾ name (count)` when expanded and `▸ name
  (count)` when collapsed. A collapsed group holding at least one non-saved
  session appends a trailing `✻` marker styled with that group's strongest
  runtime state color (attached over running).
- Session rows keep the existing columns except the project column, which the
  header now provides. Tree guides `├─` (non-last) and `└─` (last) prefix each
  session row and count toward rendered width.
- The fallback title for untitled sessions becomes `NativeID[:8]` alone, since
  the project name is no longer needed for context.
- The header statistics line, selected-session details, diagnostics, search
  line, and help line keep their current content and vertical rhythm.

## Interaction

Headers and session rows share one cursor. `j`/`k`/arrow keys move across both
kinds of rows.

- On a header, `enter` or `space` toggles collapse; `enter` never attaches
  from a header.
- On a session row, `enter` attaches exactly as today.
- Selection keeps the existing treatment: `> ` prefix plus the adaptive cyan
  background, prefix only under `NO_COLOR`.
- All groups start expanded. Collapse state is keyed by project name and lives
  only in memory: it survives manual and streaming refreshes but resets on
  restart. If a collapsed project disappears from results and later returns
  within the same run, stale keys may be dropped.
- When a toggle or refresh removes the row under the cursor, selection moves
  to the nearest remaining row, preferring the collapsed group's header.

## Search

While a `/` query is active, collapse state is ignored: every matching session
is visible beneath its group header, and groups with no matching sessions are
hidden. Clearing the query restores the prior collapse state and selection
rules.

## Rendering boundary

The change stays inside `internal/tui` and reuses the unified row approach:
the model flattens groups into one row slice (header rows and session rows)
that navigation, scrolling, and rendering consume through a single selected
index. No widget library, theme system, or configuration surface is
introduced. Responsive column removal, width fitting, and truncation continue
to apply to session rows, now accounting for the tree-guide prefix.

## Verification

Focused tests must prove:

- sessions group under the correct project headers with the specified group
  and in-group ordering;
- toggling a header hides and reveals exactly that group's sessions;
- a collapsed group with a non-saved session shows the trailing `✻` marker;
- collapse state survives refresh results and resets never leak between
  projects;
- `enter` on a header toggles without attaching; `enter` on a session still
  attaches;
- search shows matches inside collapsed groups, hides non-matching groups, and
  restores collapse state when cleared;
- every rendered line, including tree guides and headers, stays within the
  reported terminal width;
- `NO_COLOR` output keeps headers, guides, markers, and selection cues without
  ANSI sequences;
- existing navigation, refresh, and details tests updated for the new row
  model remain passing.

Run the focused TUI tests, then the repository's Go test suite and race
detector. Interactive terminal inspection remains separate acceptance
evidence.
