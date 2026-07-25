# ars Update Choice Menu Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the one-byte startup update prompt with a numbered two-row menu that supports Up/Down movement and an explicit update-or-continue choice.

**Architecture:** A focused Bubble Tea model in `internal/tui` owns rendering and key handling. `internal/update.Maybe` receives a bool-returning choice callback, so release checking and applying remain independent from the TUI package; `cmd/ars` is the only wiring point.

**Tech Stack:** Go 1.26, Bubble Tea v2 and Lip Gloss v2 already present in the repository, Go tests, tmux for live CLI/TUI verification.

## Global Constraints

- Default selection is update, preserving the existing `Enter = update` behavior.
- Up/Down move between exactly two numbered rows; Enter confirms; `1`/`2` confirm directly.
- `q`, Escape, Ctrl+C, menu errors, and cancellations continue with the current version.
- Update checks remain interactive-only, dev builds still skip them, and release/apply behavior is unchanged.
- Charm imports remain confined to `internal/tui`.
- No new dependencies or generalized menu abstraction.
- Run `go run ./cmd/ars-build --assets-only` before builds when generated collector assets are absent.

---

### Task 1: Add the focused Bubble Tea update-choice model

**Files:**
- Create: `internal/tui/update_choice.go`
- Create: `internal/tui/update_choice_test.go`

**Interfaces:**
- Produces: `func ChooseUpdate(ctx context.Context, input io.Reader, output io.Writer, current, latest string) bool`
- Produces an unexported `updateChoiceModel` whose final `update` field is true only when the user confirmed row 1.
- Reuses: `viewStyles`, `newViewStyles`, Bubble Tea `tea.KeyPressMsg`, and inline `tea.View`.

- [ ] **Step 1: Write failing model tests**

Add literal assertions that the initial view includes:

```text
ars v1.3.0 available (current v1.2.0)
> 1. Update to v1.3.0
  2. Continue with v1.2.0
↑/↓ move · 1/2 choose · enter confirm
```

Drive the real model with `tea.KeyPressMsg` and assert:

```go
// Default Enter confirms update.
// Down then Enter confirms continue.
// Down then Up then Enter confirms update.
// '1' and '2' directly confirm the matching row.
// q, Escape, and Ctrl+C confirm continue.
// An unrelated key neither quits nor changes the cursor.
```

The production regression caught by these tests is removal or reversal of a
user-visible choice, not a private implementation detail.

- [ ] **Step 2: Run the tests and verify RED**

Run:

```bash
go test ./internal/tui/ -run 'TestUpdateChoice' -count=1 -v
```

Expected: build failure because `updateChoiceModel` and `ChooseUpdate` do not
exist.

- [ ] **Step 3: Implement the minimal model and runner**

Create a two-row model with:

```go
type updateChoiceModel struct {
	current   string
	latest    string
	selected int
	confirmed bool
	update    bool
	styles    viewStyles
}
```

`Update` handles only background color and the required keys. Confirming sets
`confirmed`, derives `update = selected == 0`, and returns `tea.Quit`.
`View` returns an inline `tea.View` with the version header, two numbered rows,
cursor marker, and key hint. `ChooseUpdate` runs the model with
`tea.WithContext`, `tea.WithInput`, and `tea.WithOutput`; it returns false on a
program error or an unexpected final model.

- [ ] **Step 4: Run the focused tests and verify GREEN**

Run:

```bash
go test ./internal/tui/ -run 'TestUpdateChoice' -count=1 -v
```

Expected: all update-choice tests pass.

---

### Task 2: Inject the menu choice into the update orchestrator

**Files:**
- Modify: `internal/update/update.go`
- Modify: `internal/update/update_test.go`
- Delete: `internal/update/prompt.go`
- Delete: `internal/update/prompt_test.go`
- Modify: `cmd/ars/main.go`
- Modify: `cmd/ars/main_test.go` only if its observable wiring assertions require it

**Interfaces:**
- Consumes: `tui.ChooseUpdate(ctx, stdin, stdout, current, latest) bool`
- Changes `update.Dependencies` to include:

```go
Choose func(current, latest string) bool
```

- Removes `MakeRaw`, `Input`, and `Output` from `update.Dependencies`.

- [ ] **Step 1: Change orchestrator tests first**

Replace byte-input harness setup with a deterministic choice function:

```go
Choose: func(_, _ string) bool { return accept },
```

Record the received current/latest pair and assert it is exactly
`1.2.0`/`1.3.0`. Keep separate tests proving declined choice performs no
command/re-exec and accepted choice performs the existing npm update and
re-exec.

- [ ] **Step 2: Run the orchestrator tests and verify RED**

Run:

```bash
go test ./internal/update/ -run 'TestMaybe' -count=1 -v
```

Expected: build failure because `Dependencies.Choose` is not defined.

- [ ] **Step 3: Replace the prompt dependency and wire the TUI**

In `Maybe`, call `deps.Choose(deps.CurrentVersion, latest)` after the newer
release check. In `cmd/ars.maybeUpdate`, inject:

```go
Choose: func(current, latest string) bool {
	return tui.ChooseUpdate(ctx, stdin, stdout, current, latest)
},
```

Remove the manual `term.MakeRaw` callback and its now-unused import. Delete the
old one-byte prompt and tests. Do not alter update application or re-exec code.

- [ ] **Step 4: Run focused package tests and verify GREEN**

Run:

```bash
go test ./internal/update/ ./internal/tui/ ./cmd/ars/ -count=1
```

Expected: all three packages pass.

---

### Task 3: Document and prove the assembled startup flow

**Files:**
- Modify: `README.md`
- Create: `docs/scenarios/update-choice-menu.md`

**Interfaces:**
- Documents the exact rendered labels and keys exposed by the implementation.
- Scenario drives a freshly built real `ars` binary in isolated tmux sessions.

- [ ] **Step 1: Update the README behavior**

Replace the old “Enter to update, any other key to skip” wording with the
numbered menu behavior: Up/Down, Enter, `1`/`2`, and explicit current-version
continuation.

- [ ] **Step 2: Write the falsifiable scenario card**

The card must include:

- a fresh fake-old-version build under a temporary directory;
- fixed-size, uniquely named tmux sessions;
- a captured initial pane proving rows `1.` and `2.` and the cursor;
- Down + Enter proving the main ARS TUI appears without replacing the fake-old
  binary;
- a separate temporary copy where Enter selects update, the current release is
  downloaded and re-execed, and the update menu no longer reappears;
- stderr capture, checksum/version evidence, idempotent tmux/temp cleanup, and
  explicit failure observations.

- [ ] **Step 3: Run repository verification**

Run:

```bash
go run ./cmd/ars-build --assets-only
go build ./...
go vet ./...
go test ./...
gofmt -l cmd internal
npm test
```

Expected: every command exits 0; `gofmt -l` prints nothing.

- [ ] **Step 4: Execute the scenario card**

Build from the current worktree, run both tmux paths, capture the panes and
authoritative temporary binary evidence, then clean up only the named sessions
and temporary directory. Every assertion in the card must be reported
pass/fail with its concrete observation.

- [ ] **Step 5: Commit implementation and evidence**

Stage only the files named by this plan and commit with:

```bash
git commit -m "feat: add startup update choice menu"
```
