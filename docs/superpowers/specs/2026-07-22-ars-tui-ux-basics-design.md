# ARS TUI UX Basics

Date: 2026-07-22

## Problem

Live use of the TUI against a real inventory (819 sessions, one SSH peer)
surfaced friction in the fundamentals:

- Esc left a committed search filter active with no visible way to clear
  it, so an unmatched query stranded the screen on a bare `none`.
- No match count existed while filtering, and no guidance appeared when
  zero rows matched.
- The only movement keys were `j`/`k`/arrows; reaching the end of a long
  list required hundreds of presses.
- Column widths were computed from the filtered rows, so every search
  keystroke reflowed the table.
- After a search removed the selected row, the cursor could land on a
  group header, where Enter toggles the group instead of attaching.
- The header read `1 hosts`, and the footer help never mentioned group
  toggling or filter clearing.
- Normal Claude title-only sidecar files (`ai-title`/`agent-name` records
  with a session ID but no transcript) raised a persistent
  `Claude discovery partial (incompatible)` warning.

## Decisions

Search follows the common fzf/k9s contract: `/` opens the input, typing
filters live, `Enter` keeps the filter, `Esc` inside the input cancels and
clears it, and `Esc` outside the input clears a kept filter. This replaces
the earlier rule where `Esc` retained the query. The search line appends a
muted `matched/total` count. Zero matches render
`no matches for "query" · esc to clear`; an empty inventory renders
`no sessions`.

Navigation adds `g`/`G` and `Home`/`End` for the list edges plus
`PgUp`/`PgDn` and `Ctrl+U`/`Ctrl+D` for one viewport page with clamping at
both edges. `j`/`k` keep their existing wrap-around behavior. Shifted-rune
keys are matched by their text (bubbletea v2 reports Shift+g as code `g`
with text `G`).

The column layout derives from all collected sessions rather than the
visible rows, so filtering never reflows the table. When a search removes
the selected row, the cursor falls back to the first matching session
rather than a group header, keeping Enter meaning attach. The header
counts SSH peers as `1 peer`/`N peers` and omits the segment when only the
current computer is selected. The footer help adapts: search mode shows
`enter apply · esc cancel`, a kept filter adds `esc clear`, and a selected
group header swaps `enter attach` for `enter toggle`.

The Claude adapter skips title-only sidecar files silently; they are
metadata, not a lost session, and warning about them taught users to
ignore diagnostics.

## Out of scope

Fuzzy matching, match highlighting, sticky group headers, a help overlay,
and mouse support stay out; the footer plus README remain the help
surface. Mixed-session-ID histories (created by provider resume forks)
remain excluded by the fail-closed inclusion rule; revisiting that rule is
a separate decision.

## Verification

Every behavior is pinned by `internal/tui` and `internal/provider` unit
tests written first, and each batch was verified against the real binary
in a disposable tmux client (120x30 and 80x24), including search, clear,
paging, group toggling, attach, and `Ctrl+Q` detach round trips.
