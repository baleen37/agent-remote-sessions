# ARS TUI Visual Balance Design

- Date: 2026-07-22
- Status: approved
- Command: `ars`

## Goal

Polish the existing full-screen TUI so session rows are easier to scan without
reducing useful information or turning the interface into a boxed dashboard.
The change is limited to spacing, alignment, and restrained color treatment.

The existing flat Active/Recent list, keyboard behavior, responsive column
removal, session data, and attach flow remain unchanged. In particular, this
work does not add tabs, panels, borders, mouse support, themes, provider colors,
or new configuration.

## Balanced spacing

Use one column of horizontal inset on terminals wide enough to preserve it.
Very narrow terminals drop the inset before dropping required row content.

Keep vertical rhythm predictable:

- one blank line after the header;
- no blank line between a group heading and its first row;
- one blank line between Active and Recent;
- one blank line between the session list and the selected-session details;
- one blank line before the help line.

Each row stays on one terminal line. Visible columns use a consistent two-space
gutter and align across the current list. The title remains the primary flexible
column. Location and project may also shrink, while runtime and activity stay
right-aligned enough to scan consistently. Existing narrow-terminal behavior
continues to remove project, provider, and attached-client count in that order.

The selected row receives one space of internal padding on each side when the
terminal permits it. Its background extends across the usable content width so
selection does not look like an uneven highlight around differently sized text.

## Color hierarchy

Color communicates interaction and runtime state, not provider identity:

- selection: subtle adaptive cyan background with a stronger cyan cursor;
- attached: green state marker and runtime label;
- running: yellow state marker and runtime label;
- saved/recent: neutral foreground with secondary metadata dimmed;
- errors: red;
- header statistics, details, diagnostics without error severity, and help:
  muted neutral text.

Titles, provider names, locations, and project names use the normal foreground.
This preserves the earlier decision not to distinguish Claude, Codex, local,
and remote sessions by color.

Styles must remain legible on light and dark terminals. Reduced-color terminals
may approximate the palette. `NO_COLOR` must emit no ANSI styling; selection,
state symbols, labels, spacing, and alignment must still convey the same
structure without color.

## Rendering boundary

Keep the change inside `internal/tui`. Define the small set of styles once and
apply them from the existing renderer. Do not introduce a widget library,
general theme system, or configuration surface.

Column sizing must use rendered terminal width rather than byte length. Insets,
padding, ANSI sequences, Unicode titles, and truncation must never make a line
wider than the terminal.

## Verification

Focused tests must prove:

- wide rows share aligned column gutters;
- the selected background covers the usable row width;
- required information remains present at narrow widths;
- every rendered line stays within the reported terminal width;
- light/dark-capable styles do not color provider or location fields;
- `NO_COLOR` emits no ANSI while preserving selection and state cues;
- small terminal heights still keep the selected row, details, and help visible;
- existing navigation, search, refresh, and attach tests remain unchanged and
  passing.

Run the repository's Go test suite and race detector after the focused TUI
tests. Interactive terminal inspection is useful acceptance evidence, but it
must be reported separately from automated verification.
