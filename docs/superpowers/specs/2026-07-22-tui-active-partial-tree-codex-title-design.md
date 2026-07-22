# TUI: active-only partial tree + codex titles

Date: 2026-07-22
Status: approved

## Problem

1. The project tree expands every group by default. With large projects (e.g.
   `search (78)`, `infra (19)`) the list floods the screen and the sessions the
   user actually cares about — running/attached ones — drown among saved ones.
2. Codex sessions always render as an 8-char ID because the codex adapter
   hard-codes `Title: ""`, while Claude sessions show meaningful titles.

## Decisions

- Layout: keep the project-tree structure. Groups with active sessions start
  expanded showing only active sessions plus a `… N more` tail row; groups
  without active sessions start collapsed. (Chosen over a flat ACTIVE/RECENT
  section split and over per-group top-N previews.)
- Codex titles: use the first `user_message` event text, matching codex's own
  resume picker. Fall back to the ID prefix when absent.

## Design

### 1. Codex session title

`internal/provider/codex.go` `readHistory`:

- While scanning lines, also capture the first envelope with
  `type == "event_msg"` whose payload has `type == "user_message"`; take its
  `message` string.
- Normalize: trim whitespace, use the first non-empty line of multi-line prompts,
  truncate to `session.MaxTitleBytes` on a UTF-8 boundary.
- If the resulting title fails candidate validation, drop the title (keep the
  session) so the TUI falls back to the ID prefix as today.
- Keep scanning to EOF regardless — the multiple-`session_meta` detection must
  still see the whole file.

### 2. Tree: default collapse + active-only partial expansion

Per-group display mode, replacing `model.collapsed map[string]bool`:

```go
type groupMode int // groupModeAuto (zero value / absent), groupModeOpen, groupModeClosed
model.groupMode map[string]groupMode
```

Effective rendering per group:

- **auto** (no user toggle recorded):
  - group has active (running/attached) sessions → expanded, emit only active
    sessions, then a `└─ … N more` row (N = saved session count)
  - no active sessions → collapsed header (`▸ project (count)`)
- **open** → all sessions (current expanded behavior)
- **closed** → collapsed header

Transitions:

- enter/space on the `… N more` row → group becomes **open**
- enter/space on a header → if the group is currently rendered expanded
  (auto-partial or open) it becomes **closed**; if collapsed it becomes
  **open**
- active search (`query != ""`) forces every group fully expanded, as today
- refresh/streaming updates never touch user-set open/closed; auto groups
  re-evaluate from the latest runtime states

Implementation touch points:

- `internal/tui/tree.go`: `buildRows` takes the mode map; add `rowMore` to
  `rowKind`; more-rows carry the project and hidden count; extend `rowRef` and
  `refOf` so selection restore works for more-rows.
- `internal/tui/model.go`: replace `collapsed` with `groupMode`; `toggle`
  implements the header transition; enter/space handling gains the more-row
  case.
- `internal/tui/view.go`: render `rowMore` as a muted `… N more` line using the
  existing tree guide (`└─`).

Unchanged: group ordering (active first, then latest activity), in-group
ordering (active first, then newest), filtering, attach flow, header stats.

## Testing

- codex provider: fixture with a `user_message` event → title populated;
  fixture without → empty title; oversized/multi-line message → trimmed and
  truncated; invalid UTF-8 in a message never reaches the title (JSON decoding replaces it with U+FFFD); a defensive fallback drops the title (keeping the session) if title validation ever fails.
- tree/model: auto group emits active sessions + more-row with correct count;
  inactive groups start collapsed; enter on more-row opens the group; header
  toggle transitions (auto-partial→closed, closed→open, open→closed); search
  forces full expansion; selection restore across rebuilds including more-rows.
