# Implicit localhost Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the current computer an implicit `localhost` target while keeping it unlabeled in the TUI and treating the host inventory as remote-only configuration.

**Architecture:** `internal/app` owns one reserved `localhost` target and prepends it to the loaded remote inventory. Existing `Host.Local` routing continues to select direct provider/tmux execution, while canonical session and JSON identity stays `localhost`. `internal/tui` receives that identity through its existing `LocalTarget` dependency and suppresses it only in interactive presentation.

**Tech Stack:** Go 1.24, Bubble Tea v2, Lip Gloss v2, existing Go tests and Node release-contract tests.

## Global Constraints

- Do not infer local identity from hostnames, SSH aliases, DNS, VPNs, users, or interfaces.
- Keep `localhost` as the non-empty identity for session keys, routing, and JSON schema version 1.
- Keep local versus SSH execution selected by `Host.Local`, not by parsing rendered text.
- Do not delete an existing `local-host` file; stop reading it.
- Do not add dependencies, daemons, background state, discovery, or compatibility abstractions.
- Preserve the current worktree's unrelated staged `AGENTS.md` and `CLAUDE.md` by working only in `.worktrees/260722-feat-implicit-localhost`.

## File map

- `internal/app/inventory.go`: reserved localhost identity, optional remote inventory, topology construction, remote-target validation.
- `internal/app/inventory_test.go`: topology, missing inventory, reserved-target, and ordering contracts.
- `internal/app/app.go`: remove the local configuration command and load the single inventory path.
- `internal/app/app_test.go`: CLI help, command rejection, selection, and dependency contracts.
- `cmd/ars/main.go`: wire implicit topology and fixed TUI local identity into existing direct/SSH collection and attach routes.
- `internal/tui/filter.go`: return a blank rendered location for localhost.
- `internal/tui/view.go`: omit localhost from the host count and local diagnostic prefixes.
- `internal/tui/filter_test.go`, `internal/tui/view_test.go`, `internal/tui/model_test.go`, `internal/tui/pty_integration_test.go`: presentation and search regressions.
- `README.md`: remote-only inventory, implicit localhost, removed command, and migration behavior.

---

### Task 1: Build an implicit-localhost topology

**Files:**
- Modify: `internal/app/inventory.go:17-220`
- Test: `internal/app/inventory_test.go:15-180`

**Interfaces:**
- Produces: `const LocalhostTarget = "localhost"`
- Produces temporarily: `func LoadTopology(hostsPath, localPath string) ([]Host, error)` with `localPath` ignored until Task 2 removes the compatibility parameter
- Preserves: `type Host struct { Target string; Local bool }`
- Preserves: `func Load(path string) ([]Host, error)` and `func Add(path, target string) error`

- [ ] **Step 1: Replace explicit-local tests with failing implicit-topology tests**

Keep the `LocalConfigPath`, `SetLocal`, and explicit `local-host` tests until
Task 2 removes their application call sites. Replace only the old
`LoadTopology` tests and add:

```go
func TestLoadTopologyPrependsImplicitLocalhost(t *testing.T) {
	path := writeInventory(t, "devbox\nserver\n")
	got, err := LoadTopology(path, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	want := []Host{
		{Target: LocalhostTarget, Local: true},
		{Target: "devbox"},
		{Target: "server"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadTopology() = %#v, want %#v", got, want)
	}
}

func TestLoadTopologyAllowsMissingRemoteInventory(t *testing.T) {
	got, err := LoadTopology(filepath.Join(t.TempDir(), "missing", "hosts"), "ignored")
	if err != nil {
		t.Fatal(err)
	}
	want := []Host{{Target: LocalhostTarget, Local: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadTopology() = %#v, want %#v", got, want)
	}
}

func TestRemoteInventoryRejectsReservedLocalhost(t *testing.T) {
	path := writeInventory(t, "devbox\nlocalhost\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "localhost is reserved") {
		t.Fatalf("Load() error = %v", err)
	}
	if err := Add(filepath.Join(t.TempDir(), "hosts"), LocalhostTarget); err == nil ||
		!strings.Contains(err.Error(), "localhost is reserved") {
		t.Fatalf("Add() error = %v", err)
	}
}
```

- [ ] **Step 2: Run the focused tests and confirm RED**

Run: `go test ./internal/app -run 'TestLoadTopology|TestRemoteInventoryRejectsReservedLocalhost'`

Expected: assertion failure because the old topology requires a `local-host`
file and missing inventory still fails.

- [ ] **Step 3: Implement the smallest topology change**

Add the reserved identity and replace the local-file topology:

```go
const LocalhostTarget = "localhost"

func LoadTopology(hostsPath, _ string) ([]Host, error) {
	hosts, err := Load(hostsPath)
	if err != nil {
		return nil, err
	}
	return append([]Host{{Target: LocalhostTarget, Local: true}}, hosts...), nil
}

func validateRemoteTarget(target string) error {
	if err := validateTarget(target); err != nil {
		return err
	}
	if target == LocalhostTarget {
		return fmt.Errorf("localhost is reserved for the current computer")
	}
	return nil
}
```

In `Load`, treat only `os.ErrNotExist` as an empty remote inventory and call
`validateRemoteTarget(target)` for entries:

```go
file, err := os.Open(path)
if errors.Is(err, os.ErrNotExist) {
	return []Host{}, nil
}
if err != nil {
	return nil, fmt.Errorf("open host inventory: %w", err)
}
```

In `Add`, replace its first validation call with
`validateRemoteTarget(target)`. Retain `LocalConfigPath`, `SetLocal`, and
`setLocal` temporarily so the pre-Task-2 application still builds; retain the
existing atomic append behavior for remote targets.

- [ ] **Step 4: Run app package tests and confirm GREEN**

Run: `go test ./internal/app`

Expected: PASS.

- [ ] **Step 5: Commit the topology contract**

```bash
git add internal/app/inventory.go internal/app/inventory_test.go
git commit -m "feat: make localhost implicit in topology"
```

---

### Task 2: Remove explicit local configuration from the CLI

**Files:**
- Modify: `internal/app/inventory.go:33-181`
- Test: `internal/app/inventory_test.go:35-160`
- Modify: `internal/app/app.go:18-152`
- Modify: `cmd/ars/main.go:51-95`
- Test: `internal/app/app_test.go:12-360`

**Interfaces:**
- Consumes: `app.LoadTopology(string) ([]app.Host, error)`
- Consumes: `app.LocalhostTarget`
- Produces: `Dependencies.LoadTopology func(string) ([]Host, error)`
- Removes: `Dependencies.SetLocal` and the `ars local set <host>` command
- Produces: `func LoadTopology(hostsPath string) ([]Host, error)` after removing the temporary compatibility parameter

- [ ] **Step 1: Write failing CLI tests for the reduced command surface**

Update fixtures to use
`[]Host{{Target: LocalhostTarget, Local: true}, {Target: "server"}}` and make
`LoadTopology` accept one path. Replace the local-set test with:

```go
func TestRunDoesNotExposeLocalConfigurationCommand(t *testing.T) {
	deps, stdout, stderr := appDependencies()
	if code := Run(context.Background(), []string{"--help"}, deps); code != 0 {
		t.Fatalf("help code = %d", code)
	}
	if strings.Contains(stdout.String(), "local set") {
		t.Fatalf("help still exposes local command: %q", stdout.String())
	}
	if code := Run(context.Background(), []string{"local", "set", "devbox"}, deps); code != 2 {
		t.Fatalf("removed command code = %d, want 2; stderr = %q", code, stderr.String())
	}
}
```

Extend the command-shape table with:

```go
{name: "local only", args: []string{LocalhostTarget}, wantSelected: []Host{{Target: LocalhostTarget, Local: true}}},
```

Assert that `LoadTopology` receives only
`$XDG_CONFIG_HOME/ars/hosts`, and remove all `SetLocal` test branches.

- [ ] **Step 2: Run CLI tests and confirm RED**

Run: `go test ./internal/app -run 'TestRun'`

Expected: build or assertion failure from the old two-path dependency and visible local command.

- [ ] **Step 3: Implement the minimal CLI and wiring changes**

Change the dependency type and help text:

```go
type Dependencies struct {
	LoadTopology   func(string) ([]Host, error)
	AddHost        func(string, string) error
	Collect        func(context.Context, []Host) Result
	RunInteractive func(context.Context, []Host) error
	Stdout         io.Writer
	Stderr         io.Writer
}
```

Remove the `local set` branch and use only `ConfigPath()`:

```go
hostsPath, err := ConfigPath()
if err != nil {
	fmt.Fprintln(stderr, "ars:", err)
	return exitFailure
}
hosts, err := dependencies.LoadTopology(hostsPath)
```

The top-level and invalid-usage text must contain only:

```text
ars [host]
ars list --json
ars remote add <host>
```

In `internal/app/inventory.go`, change `LoadTopology(hostsPath, _ string)` to
`LoadTopology(hostsPath string)`, then remove `LocalConfigPath`, `SetLocal`,
and `setLocal`. Remove their obsolete tests from `inventory_test.go`.

In `cmd/ars/main.go`, remove `SetLocal: app.SetLocal`, remove dynamic
`localTarget` discovery, and pass:

```go
LocalTarget: app.LocalhostTarget,
```

Keep both direct collection and direct attach guarded by `host.Local`.

- [ ] **Step 4: Run command and application tests and confirm GREEN**

Run: `go test ./internal/app ./cmd/ars`

Expected: PASS.

- [ ] **Step 5: Commit the CLI change**

```bash
git add internal/app/inventory.go internal/app/inventory_test.go internal/app/app.go internal/app/app_test.go cmd/ars/main.go
git commit -m "feat: remove explicit local configuration"
```

---

### Task 3: Hide localhost in the interactive TUI

**Files:**
- Modify: `internal/tui/filter.go:14-80`
- Modify: `internal/tui/view.go:108-315`
- Test: `internal/tui/filter_test.go:10-49`
- Test: `internal/tui/view_test.go:78-230`
- Test: `internal/tui/model_test.go`
- Test: `internal/tui/pty_integration_test.go`

**Interfaces:**
- Consumes: `Dependencies.LocalTarget string`, supplied as `localhost`
- Produces: blank interactive location and local diagnostic prefix
- Preserves: canonical `session.Session.Host == "localhost"`

- [ ] **Step 1: Write failing presentation and search tests**

Change TUI fixtures from `macbook`/`local-node` to `localhost`. In
`filter_test.go`, remove `LOCAL` from the positive query table and add:

```go
func TestFilterSessionsDoesNotExposeLocalTarget(t *testing.T) {
	item := twoSessions()[0]
	item.Host = "localhost"
	if got := filterSessions([]session.Session{item}, "localhost", "localhost"); len(got) != 0 {
		t.Fatalf("hidden local target matched search: %#v", got)
	}
}
```

In `view_test.go`, assert the selected local row does not contain `localhost`,
the header for one local and one remote result says `1 hosts`, and local
diagnostics omit their host prefix:

```go
func TestViewHidesLocalhostPresentation(t *testing.T) {
	model := readyModel()
	model.width = 120
	model.height = 24
	model.result.Hosts = []output.HostResult{
		{Target: "localhost", Status: output.HostOK},
		{Target: "server", Status: output.HostOK},
	}
	model.result.Warnings = []output.HostError{{Host: "localhost", Code: "corrupt", Message: "Claude discovery partial"}}
	content := ansi.Strip(model.View().Content)
	if strings.Contains(selectedRow(content), "localhost") {
		t.Fatalf("local row exposes localhost: %q", selectedRow(content))
	}
	if !strings.Contains(content, "1 hosts") || strings.Contains(content, "localhost: Claude") {
		t.Fatalf("local presentation leaked: %q", content)
	}
	if !strings.Contains(content, "Claude discovery partial (corrupt)") {
		t.Fatalf("local diagnostic missing: %q", content)
	}
}
```

- [ ] **Step 2: Run TUI tests and confirm RED**

Run: `go test ./internal/tui -run 'TestFilterSessions|TestView'`

Expected: FAIL because `location` returns `local`, the filter matches it, the header counts both results, and diagnostics prefix the local identity.

- [ ] **Step 3: Implement presentation-only suppression**

Change `location` to:

```go
func location(item session.Session, localTarget string) string {
	if item.Host == localTarget {
		return ""
	}
	return item.Host
}
```

Count only non-local results in `header`:

```go
hosts := 0
for _, host := range value.result.Hosts {
	if host.Target != value.deps.LocalTarget {
		hosts++
	}
}
header := fmt.Sprintf("ars  %d active · %d recent · %d hosts", active, len(value.result.Sessions)-active, hosts)
```

Pass the local target to diagnostics and omit only its prefix:

```go
func diagnosticLine(value output.HostError, localTarget string) string {
	if value.Host == localTarget {
		return fmt.Sprintf("%s (%s)", value.Message, value.Code)
	}
	return fmt.Sprintf("%s: %s (%s)", value.Host, value.Message, value.Code)
}
```

Update both diagnostic call sites to
`diagnosticLine(diagnostic, value.deps.LocalTarget)`. Keep JSON untouched.

- [ ] **Step 4: Run all TUI tests and confirm GREEN**

Run: `go test ./internal/tui`

Expected: PASS.

- [ ] **Step 5: Commit the TUI behavior**

```bash
git add internal/tui/filter.go internal/tui/filter_test.go internal/tui/view.go internal/tui/view_test.go internal/tui/model_test.go internal/tui/pty_integration_test.go
git commit -m "feat: hide localhost in TUI"
```

---

### Task 4: Update user documentation and run full verification

**Files:**
- Modify: `README.md:1-110`
- Verify: all tracked source and tests

**Interfaces:**
- Documents: remote-only inventory, implicit `localhost`, local-only command, ignored legacy file
- Preserves: JSON schema version 1 and SSH-native boundaries

- [ ] **Step 1: Update README with the final user contract**

Replace “Common roster and local identity” with “Localhost and remote
inventory”. Document exactly:

```markdown
The current computer is always included as `localhost`; it requires no
configuration. The hosts file contains only OpenSSH peers. A missing hosts file
is a valid local-only configuration.

`ars localhost` opens only current-computer sessions. `localhost` is reserved
and cannot be added with `ars remote add`. Existing `local-host` files are
ignored and are not deleted.
```

Remove every instruction and help example for `ars local set`. In the TUI
section, describe the location column as blank for current-computer sessions
and the configured SSH target for peers. In the JSON section, show
`"target":"localhost"` and `"host":"localhost"` for local records.

- [ ] **Step 2: Check docs and formatting**

Run:

```bash
rg -n 'local set|local-host|current computer is rendered as `local`' README.md internal cmd
git diff --check
```

Expected: `rg` finds only the intentional migration sentence about ignored
`local-host`; `git diff --check` exits 0.

- [ ] **Step 3: Run focused and full Go verification**

Run:

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/ars ./cmd/ars-build ./cmd/ars-collector
```

Expected: every command exits 0.

- [ ] **Step 4: Run integration and package-contract verification**

Run:

```bash
go test ./internal/runtime -run TestDisposableTmuxPreservesProviderAfterDetach
go test ./internal/tui -run TestPTYAttachDetachRestoresTUI
ARS_RUN_SSHD_INTEGRATION=1 go test ./internal/ssh -run TestEphemeralSSHDCollectsAndAttaches
npm test
```

Expected: tmux and PTY checks pass; SSH integration passes when local OpenSSH
fixtures are available or reports its existing explicit prerequisite skip;
all npm tests pass.

- [ ] **Step 5: Commit documentation**

```bash
git add README.md
git commit -m "docs: document implicit localhost"
```

- [ ] **Step 6: Verify the final branch scope**

Run:

```bash
git status --short --branch
git diff --check main...HEAD
git diff --stat main...HEAD
git log --oneline --decorate main..HEAD
```

Expected: the worktree is clean; the branch contains only the approved design,
plan, topology/CLI/TUI changes, tests, and README documentation.
