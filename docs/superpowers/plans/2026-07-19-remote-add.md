# ARS Remote Add Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `ars remote add <host>` and discoverable top-level and remote help without changing existing SSH collection behavior.

**Architecture:** Keep the plain host inventory as the only ARS-owned configuration. Add one inventory append function beside `Load`, then route the new command and help forms from the existing explicit application parser through an injected dependency.

**Tech Stack:** Go 1.26, standard library, existing table-driven tests

## Global Constraints

- Add targets only to `$XDG_CONFIG_HOME/ars/hosts`, falling back to `~/.config/ars/hosts`.
- Never edit `~/.ssh/config` or test the SSH connection.
- Preserve existing inventory comments, entries, and order.
- Reject invalid and duplicate targets without changing the inventory.
- Do not add `remote list`, `remote remove`, a CLI framework, or a new configuration abstraction.
- Help must succeed without reading the inventory or collecting sessions.

---

### Task 1: Append one validated target to the inventory

**Files:**
- Modify: `internal/app/inventory.go`
- Test: `internal/app/inventory_test.go`

**Interfaces:**
- Consumes: existing `validateTarget(string) error` and `Load(string) ([]Host, error)`
- Produces: `Add(path string, target string) error`

- [ ] **Step 1: Write the failing inventory tests**

Add these tests before changing production code:

```go
func TestAddCreatesInventoryAndParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "ars", "hosts")
	if err := Add(path, "devbox"); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := string(contents), "devbox\n"; got != want {
		t.Fatalf("inventory = %q, want %q", got, want)
	}
}

func TestAddAppendsAndPreservesExistingInventory(t *testing.T) {
	tests := []struct {
		name, existing, want string
	}{
		{"trailing newline", "# managed\ndevbox\n", "# managed\ndevbox\nagent-mac\n"},
		{"missing trailing newline", "# managed\ndevbox", "# managed\ndevbox\nagent-mac\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeInventory(t, test.existing)
			if err := Add(path, "agent-mac"); err != nil {
				t.Fatalf("Add() error = %v", err)
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if got := string(contents); got != test.want {
				t.Fatalf("inventory = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAddRejectsDuplicateAndInvalidTargetsWithoutChangingInventory(t *testing.T) {
	tests := []struct {
		name, target, wantError string
	}{
		{"duplicate", "devbox", "already configured"},
		{"invalid", "bad host", "whitespace"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const original = "# managed\ndevbox\n"
			path := writeInventory(t, original)
			err := Add(path, test.target)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Add() error = %v, want %q", err, test.wantError)
			}
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("ReadFile() error = %v", readErr)
			}
			if got := string(contents); got != original {
				t.Fatalf("inventory changed to %q", got)
			}
		})
	}
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```sh
go test ./internal/app -run TestAdd -count=1
```

Expected: build failure because `Add` is undefined.

- [ ] **Step 3: Implement the minimum inventory operation**

Add `errors` and `io` imports, then add:

```go
func Add(path string, target string) error {
	if err := validateTarget(target); err != nil {
		return err
	}

	contents, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read host inventory: %w", err)
	}
	if err == nil {
		hosts, loadErr := Load(path)
		if loadErr != nil {
			return loadErr
		}
		for _, host := range hosts {
			if host.Target == target {
				return fmt.Errorf("host target is already configured")
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create host inventory directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open host inventory for append: %w", err)
	}

	line := target + "\n"
	if len(contents) > 0 && contents[len(contents)-1] != '\n' {
		line = "\n" + line
	}
	if _, err := io.WriteString(file, line); err != nil {
		_ = file.Close()
		return fmt.Errorf("append host inventory: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close host inventory: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Format and verify GREEN**

Run:

```sh
gofmt -w internal/app/inventory.go internal/app/inventory_test.go
go test ./internal/app -run TestAdd -count=1
go test ./internal/app -count=1
git diff --check
```

Expected: all `internal/app` tests pass and `git diff --check` has no output.

- [ ] **Step 5: Commit the inventory change**

```sh
git add internal/app/inventory.go internal/app/inventory_test.go
git commit -m "feat: add remote host inventory entry"
```

---

### Task 2: Route remote add and expose help

**Files:**
- Modify: `internal/app/app.go`
- Test: `internal/app/app_test.go`
- Modify: `cmd/ars/main.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `app.Add(path string, target string) error` from Task 1
- Produces: `Dependencies.AddHost func(path string, target string) error`; `ars --help`; `ars remote --help`; `ars remote add <host>`

- [ ] **Step 1: Write failing help and routing tests**

Add `path/filepath` to test imports and add:

```go
func TestRunPrintsHelpWithoutApplicationDependencies(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"top level", []string{"--help"}, "ars remote add <host>"},
		{"remote", []string{"remote", "--help"}, "Usage:\n  ars remote add <host>"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), test.args, Dependencies{
				Stdout: &stdout,
				Stderr: &stderr,
			})
			if code != 0 {
				t.Fatalf("Run() = %d, want 0; stderr = %q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), test.want) {
				t.Fatalf("stdout = %q, want %q", stdout.String(), test.want)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestRunAddsRemoteWithoutLoadingOrCollecting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/ars-test-config")
	var gotPath, gotTarget string
	deps, stdout, stderr := appDependencies()
	deps.AddHost = func(path string, target string) error {
		gotPath, gotTarget = path, target
		return nil
	}
	deps.LoadHosts = func(string) ([]Host, error) {
		t.Fatal("LoadHosts called for remote add")
		return nil, nil
	}
	deps.Collect = func(context.Context, []Host) Result {
		t.Fatal("Collect called for remote add")
		return Result{}
	}

	if code := Run(context.Background(), []string{"remote", "add", "devbox"}, deps); code != 0 {
		t.Fatalf("Run() = %d, want 0; stderr = %q", code, stderr.String())
	}
	wantPath := filepath.Join("/tmp/ars-test-config", "ars", "hosts")
	if gotPath != wantPath || gotTarget != "devbox" {
		t.Fatalf("AddHost(%q, %q), want (%q, %q)", gotPath, gotTarget, wantPath, "devbox")
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q; want empty", stdout.String(), stderr.String())
	}
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```sh
go test ./internal/app -run 'TestRun(PrintsHelp|AddsRemote)' -count=1
```

Expected: build failure because `Dependencies.AddHost` is undefined.

- [ ] **Step 3: Add minimal help and remote-add routing**

Move stdout/stderr initialization to the start of `Run`. Add exact early returns for `--help` and `remote --help`, then recognize exactly `remote add <host>` before checking collection dependencies.

Add the dependency:

```go
AddHost func(string, string) error
```

Use these help texts:

```go
const topLevelHelp = `Usage:
  ars [host]
  ars list --json
  ars remote add <host>

Run "ars remote --help" for remote command help.
`

const remoteHelp = `Usage:
  ars remote add <host>

Add one SSH target to the ARS host inventory.
`
```

The remote-add branch must:

1. require `dependencies.AddHost`
2. call `ConfigPath()`
3. call `dependencies.AddHost(configPath, args[2])`
4. print failures as `ars: <error>` to stderr and return exit code 1
5. return exit code 0 without loading hosts or collecting sessions

Wire production in `cmd/ars/main.go`:

```go
AddHost: app.Add,
```

- [ ] **Step 4: Format and verify GREEN**

Run:

```sh
gofmt -w internal/app/app.go internal/app/app_test.go cmd/ars/main.go
go test ./internal/app -run 'TestRun(PrintsHelp|AddsRemote)' -count=1
```

Expected: focused tests pass.

- [ ] **Step 5: Lock down invalid usage and documentation**

Add these cases to `TestRunRejectsInvalidUsageBeforeLoadingInventory`:

```go
{"remote"},
{"remote", "add"},
{"remote", "add", "devbox", "extra"},
```

Update the README usage block with:

```sh
ars remote add devbox  # add an SSH target to the ARS inventory
ars --help             # show all command forms
ars remote --help      # show remote command help
```

Document that add creates the inventory when missing, preserves comments and entries, rejects duplicates, and does not edit `~/.ssh/config`.

- [ ] **Step 6: Run command tests and commit**

Run:

```sh
go test ./internal/app -count=1
git diff --check
```

Expected: all `internal/app` tests pass and `git diff --check` has no output.

Commit:

```sh
git add internal/app/app.go internal/app/app_test.go cmd/ars/main.go README.md docs/superpowers/plans/2026-07-19-remote-add.md
git commit -m "feat: add remote host command and help"
```

---

### Task 3: Verify the complete CLI change

**Files:**
- Verify only; no planned source changes

**Interfaces:**
- Consumes: completed inventory and command behavior from Tasks 1 and 2
- Produces: fresh test, race, vet, build, and CLI-output evidence

- [ ] **Step 1: Generate assets and run the full suite**

```sh
go run ./cmd/ars-build --assets-only
go test ./... -count=1
```

Expected: both commands exit 0 with all packages passing.

- [ ] **Step 2: Run race detection and vet**

```sh
go test -race ./... -count=1
go vet ./...
```

Expected: both commands exit 0 with no race reports or vet findings.

- [ ] **Step 3: Build and inspect real help**

```sh
go build -o /tmp/ars-remote-add ./cmd/ars
/tmp/ars-remote-add --help
/tmp/ars-remote-add remote --help
```

Expected: build exits 0; both help commands exit 0, write usage to stdout, and include `ars remote add <host>`.

- [ ] **Step 4: Inspect final scope**

```sh
git status --short
git diff origin/main...HEAD --check
git diff --stat origin/main...HEAD
```

Expected: no uncommitted source changes, no whitespace errors, and only the approved design, plan, inventory, app, command wiring, tests, and README are changed.
