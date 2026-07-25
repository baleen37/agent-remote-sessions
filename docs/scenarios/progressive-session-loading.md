# progressive-session-loading: useful rows arrive before exhaustive refresh

**What this covers**: The version-matched ARS/3 streaming collector, immediate
reuse of the last successful host result, recent-first live updates,
interaction-safe final application, unchanged exhaustive results, actual tmux
navigation and attach-return, and the configured SSH peer lifecycle.

## Pre-state

- Run from the repository worktree under test with the current user's Claude
  and Codex histories available read-only.
- `go`, `ssh`, and `tmux` are available. The normal ARS inventory contains an
  accessible configured peer. Record only peer counts and redacted outcomes.
- The dedicated QA tmux session does not exist. Stop rather than touching it if
  it does.
- Create a task-owned evidence directory, generate fresh embedded assets, and
  build the root binary:

  ```bash
  mkdir -p .qa/reports/evidence
  go run ./cmd/ars-build --assets-only
  go run ./cmd/ars-build
  ```

- Warm the normal per-host result by completing one real collection. Never
  copy titles, CWDs, transcript content, raw JSON, raw protocol frames, host
  secrets, or pane contents into evidence.

## Steps

1. In a disposable checkout of baseline commit `49fb94e`, run its final
   collector against the same home as the feature collector. For both results,
   normalize and sort only:

   ```text
   provider<TAB>native_id<TAB>updated_at<TAB>runtime_state
   provider-summary<TAB>provider<TAB>status<TAB>seen<TAB>skipped<TAB>error_code
   ```

   Compare the normalized files and retain only counts, SHA-256 hashes, and the
   diff exit status.

2. Warm up the feature collector once on localhost. Run it three more times,
   measuring the first validated recent callback and the authoritative final
   callback. Record each duration and both medians without recording callback
   payloads.

3. Start `./ars localhost` in a dedicated, fixed-size tmux session with stderr
   redirected to a task-owned file. Poll through an in-memory pane capture and
   record only elapsed time, generic status-token assertions, privacy-safe row
   counts, and canonical-identity hashes. Do not persist the pane capture.

4. Confirm warm rows become usable within one second and the header shows only
   `refreshing`. Fail if `cached`, a recent/complete phase label,
   `loading localhost`, or any host-specific loading label appears.

5. While refresh is active, exercise movement, search entry and cancellation,
   project fold, filter clearing, runtime pinning, and a `… N more` reveal when
   those row types exist. Hash the selected `(host, provider, native ID)`
   before and during input; it must remain stable until the idle interval
   applies the newest pending snapshot.

6. Stop input and wait past the interaction idle interval. Confirm the final
   update appears automatically, collection finishes, and the selected
   canonical identity is restored when it remains present. No apply key may be
   required.

7. Select a safe existing session and attach through Enter. Do not send a
   provider command. Detach with `Ctrl+Q`, then confirm the same ARS process
   returns to a generic refresh and remains navigable.

8. Run the real collection path for the configured peer. From runner and
   process-lifecycle evidence, confirm the recent callback precedes the final
   callback, exactly one probe and one collector invocation occur, and the
   final result succeeds. If it fails, retain only its stable actionable error
   code and bounded sanitized category.

9. After the peer command exits, perform a scoped read-only check for the
   task's nonce-specific collector path. Confirm no task-owned collector file
   or directory remains.

## Expected

- Baseline and feature normalized counts and SHA-256 hashes are equal and the
  sorted diff exits zero. Any identity, timestamp, runtime-state, or provider
  diagnostic difference fails the scenario.
- Across the three measured localhost runs, the median recent callback is at
  most 1.5 seconds and the median final callback is at most 3.71 seconds.
  Missing callbacks or a slower median fails the scenario.
- Warm usable rows render within one second. The only collection status is
  generic `refreshing`; any cache, phase, or host loading label fails.
- Navigation, search, folding, filtering, pinning, and show-all remain
  responsive during refresh. The selected canonical identity does not change
  during active input; a spontaneous selection change fails.
- The newest exhaustive result applies automatically after idle and collection
  completes without an apply key. A permanently staged update fails.
- Enter attaches to the selected runtime, `Ctrl+Q` returns to the same TUI, and
  the return starts a generic refresh. Sending a command to the provider,
  losing the TUI, or failing to refresh fails.
- The configured peer emits recent before final using one probe and one
  collector lifecycle. Final success and no residual task-owned collector are
  required. An inaccessible peer, extra invocation, non-actionable failure, or
  residual collector makes this external risk surface incomplete or failed.

## Cleanup

Quit ARS with `q`. Idempotently kill only the dedicated QA tmux session if it
still exists. Remove only the explicitly recorded disposable baseline checkout
and task-owned temporary paths; leave user histories, inventory, host result,
provider runtimes, and all unrelated tmux sessions unchanged.

## Sharp edges

- Build both collectors from their stated commits immediately before testing;
  stale assets invalidate timing and protocol evidence.
- Warm-cache row timing and recent live callback timing are separate
  assertions.
- Bubble Tea pane captures contain private metadata and differential ANSI
  output. Inspect them only in memory and persist derived booleans,
  counts, timings, and hashes.
- A row required for fold, pin, or show-all can be absent from the current
  dataset. Record the operation as not applicable instead of fabricating it,
  and rely on the focused automated coverage for that unavailable row type.
- Automated, localhost, actual TUI, and configured-peer results are separate
  gates. Never promote a skipped or inaccessible live gate to PASS.
