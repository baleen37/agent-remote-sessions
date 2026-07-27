# Explicit `ars update` Command Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a non-interactive `ars update` command that immediately updates an npm or standalone installation by reusing the existing verified updater.

**Architecture:** `internal/update` gains an explicit coordinator with a typed result while the existing automatic `Maybe` path keeps its prompt and re-exec behavior. `internal/app` routes the new top-level command before topology loading, and `cmd/ars` supplies production dependencies and renders the result.

**Tech Stack:** Go 1.24, standard library HTTP/process/filesystem packages, existing `internal/update` package, existing dependency-injected CLI tests.

## Global Constraints

- The only new public command is `ars update`; do not add an `ars upgrade` alias.
- Running `ars update` is consent to update immediately; do not show a confirmation prompt.
- Support both global npm installs and standalone GitHub Release binaries through the existing channel detection and apply functions.
- Do not add `--check`, version selection, downgrade, or prerelease support.
- Keep the existing interactive startup update check and choice menu unchanged.
- Use the existing 1.5-second release-check timeout.
- A development build with an empty embedded version must fail with `updates are unavailable for development builds`.
- Do not re-exec ARS after an explicit update.
- Print exactly `Updated ars from v<current> to v<latest>` after an update.
- Print exactly `ars v<current> is already up to date` when no update is needed.
- Preserve exit codes 0 for success, 1 for update failures, and 2 for invalid usage.
- Add no new third-party dependency.

## File Map

- `internal/update/update.go`: explicit update orchestration and the shared apply helper.
- `internal/update/update_test.go`: explicit-mode behavior and automatic-mode regression coverage.
- `internal/app/app.go`: top-level command routing, help, usage, and exit-code mapping.
- `internal/app/app_test.go`: routing isolation, output/error, and invalid-shape tests.
- `cmd/ars/main.go`: production updater dependencies and explicit result rendering.
- `cmd/ars/main_test.go`: command-level result copy tests with a fake release server.
- `README.md`: document the new command and its no-confirmation behavior.

---

### Task 1: Add an explicit updater coordinator

**Files:**
- Modify: `internal/update/update.go`
- Test: `internal/update/update_test.go`

**Interfaces:**
- Consumes: existing `Dependencies`, `FetchLatest`, `IsNewer`, `ApplyNPM`, and `ApplyBinary`.
- Produces:

```go
type Result struct {
	CurrentVersion string
	LatestVersion  string
	Updated        bool
}

func Explicit(ctx context.Context, deps Dependencies) (Result, error)
```

- Keeps `func Maybe(ctx context.Context, deps Dependencies) error` behavior-compatible.

- [ ] **Step 1: Write failing explicit-mode tests**

Append focused tests to `internal/update/update_test.go` using the existing
`newMaybeHarness`:

```go
func TestExplicitRejectsDevBuild(t *testing.T) {
	t.Parallel()

	_, err := Explicit(context.Background(), Dependencies{})
	if err == nil || err.Error() != "updates are unavailable for development builds" {
		t.Fatalf("Explicit() error = %v", err)
	}
}

func TestExplicitReportsUpToDateWithoutApplyingOrExecing(t *testing.T) {
	t.Parallel()

	harness := newMaybeHarness(t, "v1.2.0", "1.2.0", true, npmExecutable)
	result, err := Explicit(context.Background(), harness.deps)
	if err != nil {
		t.Fatal(err)
	}
	want := Result{CurrentVersion: "1.2.0", LatestVersion: "1.2.0"}
	if result != want {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
	if len(harness.choices) != 0 || len(harness.commands) != 0 || len(harness.execs) != 0 {
		t.Fatalf("unexpected side effects: choices=%v commands=%v execs=%v", harness.choices, harness.commands, harness.execs)
	}
}

func TestExplicitAppliesWithoutPromptOrReExec(t *testing.T) {
	t.Parallel()

	harness := newMaybeHarness(t, "v1.3.0", "1.2.0", true, npmExecutable)
	result, err := Explicit(context.Background(), harness.deps)
	if err != nil {
		t.Fatal(err)
	}
	want := Result{CurrentVersion: "1.2.0", LatestVersion: "1.3.0", Updated: true}
	if result != want {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
	wantCommands := [][]string{{"npm", "install", "-g", "@baleen37/ars@1.3.0"}}
	if !reflect.DeepEqual(harness.commands, wantCommands) {
		t.Fatalf("commands = %#v, want %#v", harness.commands, wantCommands)
	}
	if len(harness.choices) != 0 || len(harness.execs) != 0 {
		t.Fatalf("explicit update prompted or re-execed: choices=%v execs=%v", harness.choices, harness.execs)
	}
}

func TestExplicitReturnsCheckAndApplyFailures(t *testing.T) {
	t.Parallel()

	t.Run("check", func(t *testing.T) {
		harness := newMaybeHarness(t, "v1.3.0", "1.2.0", true, npmExecutable)
		harness.deps.ReleaseAPI = "http://127.0.0.1:0/unreachable"
		if _, err := Explicit(context.Background(), harness.deps); err == nil {
			t.Fatal("Explicit() = nil error")
		}
	})

	t.Run("invalid latest version", func(t *testing.T) {
		harness := newMaybeHarness(t, "vbanana", "1.2.0", true, npmExecutable)
		if _, err := Explicit(context.Background(), harness.deps); err == nil {
			t.Fatal("Explicit() = nil error")
		}
	})

	t.Run("apply", func(t *testing.T) {
		harness := newMaybeHarness(t, "v1.3.0", "1.2.0", true, npmExecutable)
		harness.deps.RunCommand = func(context.Context, string, ...string) error {
			return errors.New("exit status 1")
		}
		if _, err := Explicit(context.Background(), harness.deps); err == nil {
			t.Fatal("Explicit() = nil error")
		}
		if len(harness.execs) != 0 {
			t.Fatalf("execs = %v, want none", harness.execs)
		}
	})
}
```

- [ ] **Step 2: Run the focused tests and confirm RED**

Run:

```bash
go test ./internal/update -run 'TestExplicit' -count=1
```

Expected: compilation fails because `Explicit` and `Result` do not exist.

- [ ] **Step 3: Implement the typed explicit flow and share application**

Add `Result` and `Explicit` to `internal/update/update.go`. Validate both current
and latest stable versions so malformed release data is an explicit error:

```go
type Result struct {
	CurrentVersion string
	LatestVersion  string
	Updated        bool
}

func Explicit(ctx context.Context, deps Dependencies) (Result, error) {
	current := deps.CurrentVersion
	if current == "" {
		return Result{}, fmt.Errorf("updates are unavailable for development builds")
	}
	if _, ok := parseVersion(current); !ok {
		return Result{}, fmt.Errorf("invalid current version %q", current)
	}

	checkCtx, cancel := context.WithTimeout(ctx, deps.CheckTimeout)
	defer cancel()
	latest, err := FetchLatest(checkCtx, deps.Client, deps.ReleaseAPI)
	if err != nil {
		return Result{}, err
	}
	if _, ok := parseVersion(latest); !ok {
		return Result{}, fmt.Errorf("invalid latest version %q", latest)
	}

	result := Result{CurrentVersion: current, LatestVersion: latest}
	if !IsNewer(latest, current) {
		return result, nil
	}
	if _, err := apply(ctx, deps, latest); err != nil {
		return Result{}, err
	}
	result.Updated = true
	return result, nil
}
```

Extract the existing executable lookup and channel selection into this private
helper:

```go
func apply(ctx context.Context, deps Dependencies, latest string) (string, error) {
	executable, err := deps.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	if IsNPMInstall(executable) {
		err = ApplyNPM(ctx, deps.RunCommand, latest)
	} else {
		err = ApplyBinary(ctx, deps.Client, deps.DownloadBase, latest, deps.GOOS, deps.GOARCH, executable)
	}
	if err != nil {
		return "", err
	}
	return executable, nil
}
```

Replace the matching block in `Maybe` with:

```go
	executable, err := apply(ctx, deps, latest)
	if err != nil {
		return err
	}
	if err := deps.Exec(executable, deps.Args, deps.Environ); err != nil {
		return fmt.Errorf("start updated ars: %w", err)
	}
```

- [ ] **Step 4: Run updater tests and confirm GREEN**

Run:

```bash
go test ./internal/update -count=1
```

Expected: all updater tests pass, including the existing prompt, npm,
standalone checksum, replacement, and re-exec tests.

- [ ] **Step 5: Commit the updater core**

```bash
git add internal/update/update.go internal/update/update_test.go
git commit -m "feat(update): add explicit update flow"
```

---

### Task 2: Route and document `ars update`

**Files:**
- Modify: `internal/app/app.go`
- Test: `internal/app/app_test.go`
- Modify: `cmd/ars/main.go`
- Test: `cmd/ars/main_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `update.Explicit(ctx, deps) (update.Result, error)` from Task 1.
- Adds to `app.Dependencies`:

```go
RunUpdate func(context.Context) error
```

- Adds command helpers:

```go
func updateDependencies(stdin io.Reader, stdout, stderr io.Writer) update.Dependencies
func runExplicitUpdate(ctx context.Context, deps update.Dependencies, stdout io.Writer) error
```

- [ ] **Step 1: Write failing app-routing tests**

Add these tests to `internal/app/app_test.go`:

```go
func TestRunUpdatesWithoutLoadingTopology(t *testing.T) {
	calls := 0
	deps, stdout, stderr := appDependencies()
	deps.RunUpdate = func(context.Context) error {
		calls++
		return nil
	}
	deps.LoadTopology = func(string) ([]Host, error) {
		t.Fatal("LoadTopology called for update")
		return nil, nil
	}
	deps.Collect = func(context.Context, []Host) Result {
		t.Fatal("Collect called for update")
		return Result{}
	}
	deps.RunInteractive = func(context.Context, []Host) error {
		t.Fatal("TUI called for update")
		return nil
	}

	if code := Run(context.Background(), []string{"update"}, deps); code != 0 {
		t.Fatalf("Run() = %d, want 0; stderr = %q", code, stderr.String())
	}
	if calls != 1 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("calls/stdout/stderr = %d/%q/%q", calls, stdout.String(), stderr.String())
	}
}

func TestRunReportsUpdateFailure(t *testing.T) {
	deps, _, stderr := appDependencies()
	deps.RunUpdate = func(context.Context) error {
		return errors.New("network unavailable")
	}

	if code := Run(context.Background(), []string{"update"}, deps); code != 1 {
		t.Fatalf("Run() = %d, want 1", code)
	}
	if got := stderr.String(); got != "ars: update: network unavailable\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestRunHelpIncludesUpdateWithoutUpgradeAlias(t *testing.T) {
	deps, stdout, stderr := appDependencies()
	if code := Run(context.Background(), []string{"--help"}, deps); code != 0 {
		t.Fatalf("Run() = %d; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "ars update") || strings.Contains(stdout.String(), "ars upgrade") {
		t.Fatalf("help = %q", stdout.String())
	}
}
```

Add `{"update", "extra"}` to `TestRunRejectsInvalidUsageBeforeLoadingTopology`.
Add this separate test to prove `upgrade` is not an alias:

```go
func TestRunDoesNotProvideUpgradeAlias(t *testing.T) {
	deps, _, stderr := appDependencies()
	deps.LoadTopology = func(string) ([]Host, error) {
		t.Fatal("LoadTopology called for rejected upgrade alias")
		return nil, nil
	}
	if code := Run(context.Background(), []string{"upgrade"}, deps); code != 2 {
		t.Fatalf("Run() = %d, want 2; stderr = %q", code, stderr.String())
	}
}
```

- [ ] **Step 2: Run app tests and confirm RED**

Run:

```bash
go test ./internal/app -run 'TestRun(Updates|ReportsUpdate|HelpIncludes|RejectsInvalid)' -count=1
```

Expected: compilation fails because `RunUpdate` does not exist, or `ars update`
is routed as a host instead of a command.

- [ ] **Step 3: Add the minimal app command route**

Set the top-level help to:

```go
const topLevelHelp = `Usage:
  ars [host]
  ars list --json
  ars remote add <host>
  ars update

Run "ars remote --help" for remote command help.
`
```

Set the invalid-usage output to:

```go
fmt.Fprintln(stderr, "usage: ars [host] | ars list --json | ars remote add <host> | ars update")
```

Add `RunUpdate` to `Dependencies`. Before remote configuration and
`parseArguments`, route exactly one `update` argument:

```go
	if len(args) == 1 && args[0] == "update" {
		if dependencies.RunUpdate == nil {
			fmt.Fprintln(stderr, "ars: invalid application dependencies")
			return exitFailure
		}
		if err := dependencies.RunUpdate(ctx); err != nil {
			fmt.Fprintln(stderr, "ars: update:", err)
			return exitFailure
		}
		return exitSuccess
	}
```

Immediately after that branch, reject the unimplemented alias before topology
loading:

```go
	if len(args) == 1 && args[0] == "upgrade" {
		fmt.Fprintln(stderr, "usage: ars [host] | ars list --json | ars remote add <host> | ars update")
		return exitUsage
	}
```

Do not change host selection or reserve any other single-word host name.

- [ ] **Step 4: Write failing command-level output tests**

Add `bytes`, `net/http`, `net/http/httptest`, and `internal/update` imports to
`cmd/ars/main_test.go`, then add:

```go
func TestRunExplicitUpdatePrintsResult(t *testing.T) {
	tests := []struct {
		name       string
		tag        string
		wantOutput string
		wantInstall bool
	}{
		{
			name:       "already current",
			tag:        "v1.2.0",
			wantOutput: "ars v1.2.0 is already up to date\n",
		},
		{
			name:        "updated",
			tag:         "v1.3.0",
			wantOutput:  "Updated ars from v1.2.0 to v1.3.0\n",
			wantInstall: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, `{"tag_name":%q}`, test.tag)
			}))
			defer server.Close()

			installed := false
			deps := update.Dependencies{
				CurrentVersion: "1.2.0",
				Client:         server.Client(),
				ReleaseAPI:     server.URL,
				Executable: func() (string, error) {
					return "/usr/local/lib/node_modules/@baleen37/ars/vendor/ars-darwin-arm64", nil
				},
				RunCommand: func(context.Context, string, ...string) error {
					installed = true
					return nil
				},
				CheckTimeout: time.Second,
			}
			var stdout bytes.Buffer
			if err := runExplicitUpdate(context.Background(), deps, &stdout); err != nil {
				t.Fatal(err)
			}
			if stdout.String() != test.wantOutput || installed != test.wantInstall {
				t.Fatalf("stdout/installed = %q/%v, want %q/%v", stdout.String(), installed, test.wantOutput, test.wantInstall)
			}
		})
	}
}
```

- [ ] **Step 5: Run the command-level test and confirm RED**

Run:

```bash
go test ./cmd/ars -run TestRunExplicitUpdatePrintsResult -count=1
```

Expected: compilation fails because `runExplicitUpdate` does not exist.

- [ ] **Step 6: Wire shared production dependencies and exact output**

In `cmd/ars/main.go`, add `io` to imports. Extract the dependencies currently
constructed inside `maybeUpdate` into:

```go
func updateDependencies(stdin io.Reader, stdout, stderr io.Writer) update.Dependencies {
	return update.Dependencies{
		CurrentVersion: version,
		Client:         http.DefaultClient,
		ReleaseAPI:     update.DefaultReleaseAPI,
		DownloadBase:   update.DefaultDownloadBase,
		GOOS:           goruntime.GOOS,
		GOARCH:         goruntime.GOARCH,
		Executable:     os.Executable,
		RunCommand: func(ctx context.Context, name string, args ...string) error {
			command := exec.CommandContext(ctx, name, args...)
			command.Stdin = stdin
			command.Stdout = stdout
			command.Stderr = stderr
			return command.Run()
		},
		Exec:         syscall.Exec,
		Args:         os.Args,
		Environ:      os.Environ(),
		CheckTimeout: 1500 * time.Millisecond,
	}
}
```

Keep `maybeUpdate` behavior by adding only its TUI choice callback:

```go
func maybeUpdate(ctx context.Context, stdin, stdout *os.File) error {
	deps := updateDependencies(stdin, stdout, os.Stderr)
	deps.Choose = func(current, latest string) bool {
		return tui.ChooseUpdate(ctx, stdin, stdout, current, latest)
	}
	return update.Maybe(ctx, deps)
}
```

Add explicit rendering:

```go
func runExplicitUpdate(ctx context.Context, deps update.Dependencies, stdout io.Writer) error {
	result, err := update.Explicit(ctx, deps)
	if err != nil {
		return err
	}
	if result.Updated {
		_, err = fmt.Fprintf(stdout, "Updated ars from v%s to v%s\n", result.CurrentVersion, result.LatestVersion)
	} else {
		_, err = fmt.Fprintf(stdout, "ars v%s is already up to date\n", result.CurrentVersion)
	}
	return err
}
```

Wire `RunUpdate` into the existing `app.Dependencies` literal in `main`:

```go
		RunUpdate: func(ctx context.Context) error {
			return runExplicitUpdate(ctx, updateDependencies(os.Stdin, os.Stdout, os.Stderr), os.Stdout)
		},
```

- [ ] **Step 7: Update README command documentation**

Add this line to the README command block:

```sh
ars update             # update the current installation to the latest release
```

Add this paragraph after the command block:

```markdown
`ars update` applies the latest release immediately without a second
confirmation. It reports success without reinstalling when the current
version is already latest. The command supports npm and standalone installs;
development builds do not update themselves.
```

- [ ] **Step 8: Format and run focused tests**

Run:

```bash
gofmt -w internal/update/update.go internal/update/update_test.go internal/app/app.go internal/app/app_test.go cmd/ars/main.go cmd/ars/main_test.go
go test ./internal/update ./internal/app ./cmd/ars -count=1
```

Expected: all focused tests pass.

- [ ] **Step 9: Build generated assets and run full verification**

Run:

```bash
go run ./cmd/ars-build --assets-only
go test ./... -count=1
go vet ./...
npm test
git diff --check
```

Expected: every command exits 0. Generated collector binaries and any root
`ars` build artifact remain untracked local build outputs and are not staged.

- [ ] **Step 10: Inspect the final scope**

Run:

```bash
git status --short
git diff --stat
git diff -- internal/update/update.go internal/update/update_test.go internal/app/app.go internal/app/app_test.go cmd/ars/main.go cmd/ars/main_test.go README.md
```

Expected: every changed production or test line traces to `ars update`; no
unrelated formatting, refactor, generated collector, or root binary is staged.

- [ ] **Step 11: Commit the CLI feature**

```bash
git add internal/app/app.go internal/app/app_test.go cmd/ars/main.go cmd/ars/main_test.go README.md
git commit -m "feat(cli): add ars update command"
```
