# external-tmux-reuse: reuse an existing provider from the real TUI

**What this covers**: a freshly built `ars` discovers a saved Claude session,
selects it through the real TUI, attaches to the matching provider already
running in an external tmux server, and returns to the same TUI after the
external server's normal `prefix d`.

## Pre-state

- `tmux`, Go, and the repository checkout under test are available.
- Create one `mktemp -d` root with isolated `HOME`, `XDG_CONFIG_HOME`,
  `XDG_CACHE_HOME`, `TMPDIR`, and `TMUX_TMPDIR`. All fixture paths must stay
  below that root.
- Run `go build -o "$root/ars" ./cmd/ars`. Record its SHA-256 digest so a stale
  installed binary cannot satisfy the scenario.
- Build a fake executable named `claude`. It must record its PID, print the
  unique marker `EXTERNAL_TMUX_E2E_PROVIDER`, and remain alive. Start it with
  exactly these three argv values:

  ```text
  /path/to/claude
  --resume
  77777777-7777-7777-7777-777777777777
  ```

- Write one current, regular Claude history file below
  `$HOME/.claude/projects/external-tmux/` with that session ID, the CWD
  `/tmp/ars-external-tui-project`, and the user title
  `External tmux E2E fixture`.
- Start the fake provider in a disposable `tmux -L external -f /dev/null`
  server below the isolated `TMUX_TMPDIR`. Record its provider PID and hashes
  of `list-sessions`, `list-windows -a`, `list-panes -a`, `list-keys -T root`,
  and `show-options -g`.
- If the real `/tmp/tmux-$(id -u)` contains unrelated sockets, put a
  fixture-local `tmux` shim first in `PATH`. It must return success only for
  resolver inspection of those fallback `-S /tmp/tmux-$(id -u)/*` paths and
  delegate every other argv unchanged to the real tmux binary. This preserves
  fail-closed production behavior while keeping the scenario isolated from
  user-owned servers.
- Start the fresh `"$root/ars"` in a separate fixed-size
  `tmux -L driver -f /dev/null` session with the isolated environment. Record
  the ARS pane PID. Poll `capture-pane`; do not use fixed readiness sleeps.

## Steps

1. Wait for the real TUI to show `ars-external-tui-project (1)`.
2. Send `Enter` to expand the group, then `j` to select
   `External tmux E2E …`.
3. Send `Enter` and poll the driver pane for
   `EXTERNAL_TMUX_E2E_PROVIDER`. At the same time, poll the external session
   until `#{session_attached}` is `1`.
4. Send the external server's normal detach binding to the driver pane:

   ```sh
   tmux -L driver send-keys -t ars-driver C-b d
   ```

5. Poll until the driver pane again contains
   `ars-external-tui-project (1)` and `attach finished`, and the external
   session reports zero attached clients.
6. Compare the ARS pane PID and provider PID with their pre-attach values.
   Recompute the five external tmux snapshot hashes.

## Expected

- The selected row is the history-backed fixture. Seeing another row selected
  fails the scenario.
- The attached screen contains `EXTERNAL_TMUX_E2E_PROVIDER`, the external
  client count becomes `1`, and no ARS-owned runtime is created for the fixture
  ID. Missing the marker or seeing a different pane fails the scenario.
- After `C-b d`, the same ARS pane PID renders the same project TUI with
  `attach finished`; the provider PID is unchanged and the external client
  count returns to `0`. A restarted provider, exited TUI, or different ARS PID
  fails the scenario.
- The before and after hashes for sessions, windows, panes, root keys, and
  global options are identical. Any mismatch fails the scenario.

## Cleanup

Quit the driver TUI with `q`, kill only the `driver` and `external` servers
inside the fixture's isolated `TMUX_TMPDIR`, verify the recorded provider PID
has exited and both fixture sockets are gone, then remove only the `mktemp`
root. Leave every pre-existing process and socket untouched.

## Sharp edges

- The row title is intentionally truncated at 120 columns, so poll for
  `External tmux E2E`, not the full fixture title.
- External resolution on macOS performs exact argv inspection and can be slow
  on a heavily loaded machine. Poll the observable state until the scenario
  deadline.
- The fallback-socket shim is a test-isolation boundary only. A production
  resolver error from any standard socket must remain fail closed.
