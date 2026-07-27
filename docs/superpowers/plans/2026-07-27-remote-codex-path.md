# Remote Codex PATH Independence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make remote Codex sessions discoverable and attachable when Codex is available only through the peer's login-shell PATH.

**Architecture:** Codex inventory will use validated native history as its source of truth instead of gating on the collector process PATH. Remote Codex attach will execute the existing validated resume command through `${SHELL:-/bin/sh}` as a login shell; Claude and local attach paths remain unchanged.

**Tech Stack:** Go 1.24, OpenSSH, tmux, Go `testing`, Node.js test runner

## Global Constraints

- Do not hardcode a user name, home directory, or Codex installation path.
- Do not change Claude discovery or attach behavior.
- Do not change public/private protocol, cache, parsing, filtering, limits, ordering, or TUI behavior.
- Keep `provider.ValidResumeSpec` as the command validation boundary.
- Apply every production change through a failing regression test first.

---

### Task 1: Discover Codex History Without Collector PATH

**Files:**
- Modify: `internal/provider/codex_test.go:284-298`
- Modify: `internal/provider/codex.go:3-10,90-100`

**Interfaces:**
- Consumes: `codexAdapter.Discover(context.Context, home string) provider.Result`
- Produces: unchanged Codex `Result`; valid metadata is discoverable even when `exec.LookPath("codex")` would fail

- [ ] **Step 1: Replace the executable-gated test with the desired metadata behavior**

```go
func TestCodexDiscoverUsesMetadataWithoutExecutable(t *testing.T) {
	home := t.TempDir()
	id := "99999999-9999-9999-9999-999999999999"
	writeFile(t, filepath.Join(home, ".codex", "sessions", "valid.jsonl"),
		codexMeta(id, "/synthetic/codex", "cli", "user"))
	t.Setenv("PATH", t.TempDir())

	result := (codexAdapter{}).Discover(context.Background(), home)
	if result.Status != OK || result.ErrorCode != "" || result.Seen != 1 ||
		result.Skipped != 0 || len(result.Sessions) != 1 ||
		result.Sessions[0].NativeID != id {
		t.Fatalf("Discover() = %#v, want one Codex session from native metadata", result)
	}
}

func TestCodexDiscoverIsAbsentWithoutMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	assertAbsentResult(t, (codexAdapter{}).Discover(context.Background(), home), session.Codex)
}
```

- [ ] **Step 2: Run the regression test and verify RED**

Run:

```bash
go test ./internal/provider -run 'TestCodexDiscover(UsesMetadataWithoutExecutable|IsAbsentWithoutMetadata)$' -count=1
```

Expected: `TestCodexDiscoverUsesMetadataWithoutExecutable` fails because the current `exec.LookPath("codex")` gate returns an absent result; the missing-metadata test passes.

- [ ] **Step 3: Remove only the executable gate**

Delete the unused `os/exec` import and this block from `codexAdapter.historyFiles`:

```go
if _, err := exec.LookPath("codex"); err != nil {
	return nil, "", nil
}
```

Do not change directory traversal or metadata parsing.

- [ ] **Step 4: Run focused provider tests and verify GREEN**

Run:

```bash
go test ./internal/provider -run 'TestCodex' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the provider fix**

```bash
git add internal/provider/codex.go internal/provider/codex_test.go
git commit -m "fix(provider): discover Codex history without PATH"
```

### Task 2: Resume Remote Codex Through the Login Shell

**Files:**
- Modify: `internal/ssh/attach_test.go:152-168`
- Modify: `internal/ssh/attach.go:65-113`

**Interfaces:**
- Consumes: validated `provider.ResumeSpec{Executable string, Args []string}` for `session.Codex`
- Produces: `loginPaneCommand(spec provider.ResumeSpec) string`, a single tmux pane shell-command that runs the fixed resume spec through `${SHELL:-/bin/sh} -l -i -c`

- [ ] **Step 1: Change the Codex attach test to require login-shell execution**

Replace `TestRemoteAttachCodexOmitsKeychainGuard` with:

```go
func TestRemoteAttachCodexUsesLoginShellWithoutKeychainGuard(t *testing.T) {
	item := remoteAttachedSession()
	item.Provider = session.Codex
	spec := provider.ResumeSpec{Executable: "codex", Args: []string{"resume", item.NativeID}}
	command, err := NewAttachCommand(context.Background(), item.Host, item, spec)
	if err != nil {
		t.Fatal(err)
	}

	script := command.command.Args[3]
	if strings.Contains(script, "unlock-keychain") ||
		strings.Contains(script, "Claude Code-credentials") {
		t.Fatalf("codex script must not carry the claude keychain guard:\n%s", script)
	}
	want := `exec "${SHELL:-/bin/sh}" -l -i -c 'exec '\''codex'\'' '\''resume'\'' '\''` +
		item.NativeID + `'\'''`
	if !strings.Contains(script, want) {
		t.Fatalf("codex script missing login-shell launcher %q:\n%s", want, script)
	}
}
```

The production mutation this catches is returning to bare `codex`, using a
non-login shell, or losing argument quoting.

- [ ] **Step 2: Run the regression test and verify RED**

Run:

```bash
go test ./internal/ssh -run TestRemoteAttachCodexUsesLoginShellWithoutKeychainGuard -count=1
```

Expected: FAIL because the current script contains only the plain
`'codex' 'resume' '<id>'` launcher.

- [ ] **Step 3: Add the minimal login-shell pane command**

Change the Codex branch in `remoteAttachScript`:

```go
if prov == session.Claude {
	createArgs = append(createArgs, quotePOSIX(guardedPaneCommand(spec)))
} else {
	createArgs = append(createArgs, quotePOSIX(loginPaneCommand(spec)))
}
```

Add beside `guardedPaneCommand`:

```go
func loginPaneCommand(spec provider.ResumeSpec) string {
	words := []string{quotePOSIX(spec.Executable)}
	for _, arg := range spec.Args {
		words = append(words, quotePOSIX(arg))
	}
	return `exec "${SHELL:-/bin/sh}" -l -i -c ` +
		quotePOSIX("exec " + strings.Join(words, " "))
}
```

Do not change `provider.Resume`, `provider.ValidResumeSpec`, Claude's guarded
command, or local `runtime.AttachCommand`.

- [ ] **Step 4: Run focused SSH and attach tests and verify GREEN**

Run:

```bash
go test ./internal/ssh -run 'TestRemoteAttach|TestAttachCommand' -count=1
go test ./internal/runtime -run 'TestAttachCommand|TestNewAttachCommand' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the remote attach fix**

```bash
git add internal/ssh/attach.go internal/ssh/attach_test.go
git commit -m "fix(ssh): resume Codex through login shell"
```

> Execution note: live verification later discovered and confirmed that the
> peer's provider PATH is initialized by interactive startup files. Commit
> `2833684` added `-i` to this launcher; this documents the completed work and
> does not change the checkbox history above.
### Task 3: Full and Live Verification

**Files:**
- No source changes expected
- Generated local build outputs under `internal/ssh/generated/` and root `ars` remain untracked/ignored

**Interfaces:**
- Consumes: completed Tasks 1 and 2
- Produces: automated and real-peer acceptance evidence

- [ ] **Step 1: Generate embedded collectors**

Run:

```bash
go run ./cmd/ars-build --assets-only
```

Expected: exit 0 and three non-empty collector assets for `darwin/arm64`,
`linux/amd64`, and `linux/arm64`.

- [ ] **Step 2: Run the complete automated verification suite**

Run:

```bash
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/ars-build
go test ./internal/runtime -run TestDisposableTmux -count=1 -v
go test ./internal/tui -run TestPTYAttachDetachRestoresTUI -count=1 -v
npm test
```

Expected: every command exits 0 with no failed tests or vet diagnostics.

- [ ] **Step 3: Verify remote inventory with the freshly built binary**

Run:

```bash
./ars list --json
```

Inspect the JSON structurally and assert that
`baleen@baleens-macbook.ojos-in.ts.net` has at least one session whose provider
is `codex`. Do not infer success from the host status alone.

- [ ] **Step 4: Verify real remote attach and detach**

Use the freshly built `./ars` in a disposable tmux client, select one remote
Codex session, wait for the Codex UI to render, send `Ctrl+Q`, and verify the
ARS TUI returns. Confirm on the peer that the corresponding `ars-v1` tmux
session exists and its pane command resolved to the peer's Codex installation.

- [ ] **Step 5: Check the final worktree**

Run:

```bash
git status --short --branch
git log --oneline -3
```

Expected: only intended commits and no uncommitted source changes.
