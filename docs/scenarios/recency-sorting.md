# recency-sorting: project groups follow latest activity

**What this covers**: The recency-first project ordering rendered by the ARS
TUI from a freshly built binary.

## Pre-state

Build `./cmd/ars` into a new `mktemp -d` directory. Point `XDG_CONFIG_HOME` and
`XDG_CACHE_HOME` inside that directory so the scenario uses localhost only and
does not change the user's ARS configuration or cache. Use a dedicated tmux
server label derived from the directory. Capture `ars list --json` immediately
before the TUI pane; the JSON inventory is the authoritative record.

## Steps

1. From sessions updated within the TUI's seven-day window, group by the
   basename of `cwd` and calculate each project's maximum `updated_at`. Compare
   the full RFC3339 nanosecond value rather than rounding to seconds.
2. Start the fresh binary in a 200x60 tmux session and wait until collection
   completes. Turn the preview off so all project headers fit without
   truncation.
3. Capture the pane and read the project headers from top to bottom. If active
   session files changed between the JSON and TUI collections, refresh the JSON
   and TUI once before comparing.
4. Open and close one project with `l` and `h`, then capture the pane again.

## Expected

- Unpinned project headers appear in descending maximum `updated_at` order.
  The scenario fails if an older project appears above a newer project.
- The second capture has the same project order as the first. The scenario
  fails if folding changes the order.
- The pane contains no panic and the stderr log is empty. Any panic or stderr
  output fails the scenario.

If the live inventory has fewer than two project groups, report the scenario as
blocked rather than passed.

## Cleanup

Quit the TUI, stop only the dedicated tmux server, and remove only the temporary
build directory created by this run.

## Sharp edges

Automatic folding can hide saved session rows. Project ranking still uses the
latest activity across every session in the JSON inventory.
