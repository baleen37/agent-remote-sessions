# startup-update-choice: choose update or current version before the session TUI

**What this covers**: The numbered startup menu, real Up/Down input, explicit
confirmation, standalone update replacement, and re-exec from
`feat/improve-entry-tui`.

## Pre-state

- Run from the repository worktree under test on a supported release target.
- `go`, `tmux`, `trash`, and network access to GitHub Releases are available.
- The tmux sessions `ars-e2e-update-continue` and `ars-e2e-update-apply` do not
  exist. If either exists, stop rather than touching it.
- Generate embedded assets and build two independent fake-old binaries:

```bash
go run ./cmd/ars-build --assets-only
ARS_E2E_TMP=$(mktemp -d "${TMPDIR:-/tmp}/ars-update-choice.XXXXXX")
mkdir -p "$ARS_E2E_TMP/config"
go build -ldflags "-X main.version=0.0.1" -o "$ARS_E2E_TMP/ars-continue" ./cmd/ars
cp "$ARS_E2E_TMP/ars-continue" "$ARS_E2E_TMP/ars-update"
shasum -a 256 "$ARS_E2E_TMP/ars-continue" "$ARS_E2E_TMP/ars-update" >"$ARS_E2E_TMP/before.sha"
```

## Steps

1. Start the continue-path binary in a fixed-size isolated tmux session:

   ```bash
   tmux new-session -d -s ars-e2e-update-continue -x 100 -y 24 \
     "env NO_COLOR=1 XDG_CONFIG_HOME='$ARS_E2E_TMP/config' '$ARS_E2E_TMP/ars-continue' 2>'$ARS_E2E_TMP/continue.stderr'"
   ```

2. Poll `tmux capture-pane -t ars-e2e-update-continue -p` until the pane shows
   both `1. Update to v` and `2. Continue with v`. Capture the pane as
   `continue-menu.txt`.

3. Send a real Down key and Enter, then poll until the pane shows the session
   TUI header containing `ars` and `active`:

   ```bash
   tmux send-keys -t ars-e2e-update-continue Down Enter
   ```

   Capture the settled pane as `continue-result.txt`, calculate the binary
   checksum again, and quit this ARS process with `q`.

4. Start the second fake-old binary in another fixed-size session and capture
   the same initial menu as `update-menu.txt`:

   ```bash
   tmux new-session -d -s ars-e2e-update-apply -x 100 -y 24 \
     "env NO_COLOR=1 XDG_CONFIG_HOME='$ARS_E2E_TMP/config' '$ARS_E2E_TMP/ars-update' 2>'$ARS_E2E_TMP/update.stderr'"
   ```

5. Press Enter on the default row. Poll through download/replacement/re-exec
   until the pane shows the session TUI header containing `ars` and `active`,
   without either numbered update row:

   ```bash
   tmux send-keys -t ars-e2e-update-apply Enter
   ```

   Capture the settled pane as `update-result.txt`, calculate the replaced
   binary checksum, and quit with `q`.

6. Inspect `continue.stderr`, `update.stderr`, both before/after checksums, and
   all four pane captures.

## Expected

- The initial pane has a cursor on `1. Update`, two numbered rows, and the
  `↑/↓ move · 1/2 choose · enter confirm` hint. Missing numbering, a cursor on
  row 2, or absence of the key hint fails the scenario.
- Down + Enter reaches the main session TUI and `ars-continue` keeps its exact
  pre-run SHA-256. A changed checksum, an update command, or a remaining menu
  fails the continue path.
- Default Enter reaches the main session TUI after re-exec and `ars-update` has
  a different SHA-256. An unchanged binary, a repeated update menu, or a dead
  process fails the update path.
- Both stderr files are empty. Any panic, terminal error, checksum failure, or
  update error fails the scenario.

## Cleanup

Quit the ARS processes with `q`. Kill only these named sessions if they remain,
then move only the directory recorded in `ARS_E2E_TMP` to the recoverable
system Trash:

```bash
tmux has-session -t ars-e2e-update-continue 2>/dev/null && tmux kill-session -t ars-e2e-update-continue
tmux has-session -t ars-e2e-update-apply 2>/dev/null && tmux kill-session -t ars-e2e-update-apply
trash "$ARS_E2E_TMP"
```

## Sharp edges

- The release check has a 1.5 second budget. Poll for visible states instead of
  sleeping for a fixed duration.
- The latest release may advance during the run. Read the version rendered in
  the menu instead of hard-coding the tag in polling assertions.
- Never press Enter in the continue-path session before moving to row 2.
- The update path intentionally replaces only its temporary binary. Never run
  this scenario against an installed `ars`.
- If the GitHub API or release asset is unavailable, record the external
  failure and do not treat the update-path assertion as passed.
