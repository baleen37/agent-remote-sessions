# ARS TUI and Persistent Attach Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the one-shot fzf/resume flow with a full-screen ARS TUI that lists canonical local and remote Claude/Codex sessions, starts or attaches persistent ARS-owned tmux runtimes, and returns to the same TUI after `Ctrl+Q`.

**Architecture:** Keep provider discovery, bounded one-shot SSH collection, and public JSON as stable edges. Add explicit local-node topology, provider-neutral runtime metadata, one dedicated tmux runtime, and a Bubble Tea TUI; interactive execution becomes collect → render → attach → refresh while JSON remains headless.

**Tech Stack:** Go 1.26, standard library, OpenSSH, tmux, Bubble Tea v2.0.8, Lip Gloss v2.0.5, test-only `github.com/creack/pty` v1.1.24

## Global Constraints

- Preserve one ARS binary, one-shot remote collectors, system OpenSSH configuration/authentication/host-key verification, and exactly the current Darwin arm64, Linux amd64, and Linux arm64 targets.
- Use the same canonical `hosts` roster on every managed computer and exactly one machine-local `local-host` entry; never infer local identity.
- Keep canonical identity `(configured host target, provider, native ID)`; `local` is display-only and must never become routing input.
- Use one runtime implementation: the versioned, per-user ARS tmux server. Do not add a daemon, database, cache, watcher, polling, plugin backend, or external-process adoption.
- Transfer only provider, native ID, update time, CWD, native title, runtime state, attached-client count, and runtime start time.
- Keep public JSON schema version 1 unchanged; runtime fields are private TUI data.
- Remove fzf and its installation/runtime path completely.
- Only `internal/tui` may import Bubble Tea or Lip Gloss. Add no Bubbles widgets, trees, tables, mouse UI, modals, previews, themes, or animations.
- Pin Bubble Tea `v2.0.8`, Lip Gloss `v2.0.5`, and test-only PTY `v1.1.24`.
- Use TDD and commit only task files. Preserve the user's staged `AGENTS.md` and `CLAUDE.md`.

---

## File map

~~~text
internal/app                 topology, CLI modes, aggregation
internal/session             canonical identity and runtime metadata
internal/runtime             tmux inspect, create, and attach
internal/provider            shared Claude/Codex discovery
internal/protocol            bounded ARS/2 protocol
internal/ssh                 remote collect and attach transport
internal/tui                 Bubble Tea model/filter/view/run
internal/output              unchanged public JSON v1; fzf removed
cmd/ars, cmd/ars-collector   dependency wiring and remote helper
~~~

---

### Task 1: Add canonical topology and explicit local identity

**Files:**
- Modify: `internal/app/inventory.go`
- Test: `internal/app/inventory_test.go`

**Interfaces:**
- Consumes: existing `ConfigPath`, `Load`, `Add`, `validateTarget`
- Produces: `Host{Target string, Local bool}`, `LocalConfigPath`, `LoadTopology`, `SetLocal`

- [ ] **Step 1: Write failing topology tests**

Add exact coverage for one marked local entry, missing/multiline/unknown local values, and failure-atomic writes:

```go
func TestLoadTopologyMarksExactlyOneConfiguredLocalHost(t *testing.T) {
	hostsPath := writeInventory(t, "macbook\nserver\n")
	localPath := filepath.Join(t.TempDir(), "local-host")
	if err := os.WriteFile(localPath, []byte("server\n"), 0o600); err != nil { t.Fatal(err) }
	got, err := LoadTopology(hostsPath, localPath)
	if err != nil { t.Fatal(err) }
	want := []Host{{Target: "macbook"}, {Target: "server", Local: true}}
	if !reflect.DeepEqual(got, want) { t.Fatalf("got %#v, want %#v", got, want) }
}

func TestLoadTopologyRejectsInvalidLocalHost(t *testing.T) {
	hostsPath := writeInventory(t, "macbook\nserver\n")
	for _, test := range []struct{ name, contents, want string }{
		{"missing", "", "read local host"},
		{"multiple", "macbook\nserver\n", "exactly one"},
		{"unknown", "other\n", "not configured"},
	} {
		t.Run(test.name, func(t *testing.T) {
			localPath := filepath.Join(t.TempDir(), "local-host")
			if test.contents != "" {
				if err := os.WriteFile(localPath, []byte(test.contents), 0o600); err != nil { t.Fatal(err) }
			}
			_, err := LoadTopology(hostsPath, localPath)
			if err == nil || !strings.Contains(err.Error(), test.want) { t.Fatalf("error = %v", err) }
		})
	}
}

func TestSetLocalWritesOnlyExactConfiguredTarget(t *testing.T) {
	hostsPath := writeInventory(t, "macbook\nserver\n")
	localPath := filepath.Join(t.TempDir(), "ars", "local-host")
	if err := SetLocal(hostsPath, localPath, "macbook"); err != nil { t.Fatal(err) }
	got, _ := os.ReadFile(localPath)
	if string(got) != "macbook\n" { t.Fatalf("local-host = %q", got) }
	if err := SetLocal(hostsPath, localPath, "other"); err == nil { t.Fatal("unknown accepted") }
	got, _ = os.ReadFile(localPath)
	if string(got) != "macbook\n" { t.Fatalf("failed write changed file: %q", got) }
}
```

- [ ] **Step 2: Run RED**

```sh
go test ./internal/app -run 'Test(LoadTopology|SetLocal)' -count=1
```

Expected: build failure because topology symbols do not exist.

- [ ] **Step 3: Implement topology and atomic local-host writes**

```go
type Host struct {
	Target string
	Local  bool
}

func LocalConfigPath() (string, error) {
	hosts, err := ConfigPath()
	if err != nil { return "", err }
	return filepath.Join(filepath.Dir(hosts), "local-host"), nil
}

func LoadTopology(hostsPath, localPath string) ([]Host, error) {
	hosts, err := Load(hostsPath)
	if err != nil { return nil, err }
	data, err := os.ReadFile(localPath)
	if err != nil { return nil, fmt.Errorf("read local host: %w", err) }
	value := strings.TrimSuffix(string(data), "\n")
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return nil, fmt.Errorf("local host must contain exactly one configured target")
	}
	for i := range hosts {
		if hosts[i].Target == value { hosts[i].Local = true; return hosts, nil }
	}
	return nil, fmt.Errorf("local host target is not configured")
}
```

`SetLocal` must validate with `Load`, create a `0600` temporary sibling, write `target+"\n"`, call `Sync`, close, and rename. Every failure closes/removes only that exact temporary file and leaves the old value unchanged.

```go
func SetLocal(hostsPath, localPath, target string) error {
	hosts, err := Load(hostsPath)
	if err != nil { return err }
	found := false
	for _, host := range hosts { found = found || host.Target == target }
	if !found { return fmt.Errorf("local host target is not configured") }
	if err := os.MkdirAll(filepath.Dir(localPath), 0o700); err != nil { return err }
	tmp, err := os.CreateTemp(filepath.Dir(localPath), ".local-host-*")
	if err != nil { return fmt.Errorf("create local host file: %w", err) }
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil { _ = tmp.Close(); return err }
	if _, err := io.WriteString(tmp, target+"\n"); err != nil { _ = tmp.Close(); return err }
	if err := tmp.Sync(); err != nil { _ = tmp.Close(); return err }
	if err := tmp.Close(); err != nil { return err }
	if err := os.Rename(name, localPath); err != nil { return fmt.Errorf("replace local host file: %w", err) }
	return nil
}
```

- [ ] **Step 4: Verify GREEN**

```sh
gofmt -w internal/app/inventory.go internal/app/inventory_test.go
go test ./internal/app -run 'Test(LoadTopology|SetLocal|Load|Add|Select)' -count=1
git diff --check
```

- [ ] **Step 5: Commit**

```sh
git add internal/app/inventory.go internal/app/inventory_test.go
git commit -m "feat: add explicit local host topology"
```

---

### Task 2: Add provider-neutral runtime metadata

**Files:**
- Modify: `internal/session/session.go`
- Test: `internal/session/session_test.go`
- Verify: `internal/output/json_test.go`

**Interfaces:**
- Produces: `RuntimeState`, `Runtime`, `Discovered`, `ValidateRuntime`, `BindDiscovered`
- Preserves: existing `Bind` as a saved-runtime wrapper and JSON v1 without runtime fields

- [ ] **Step 1: Write failing runtime validation tests**

```go
func TestValidateRuntime(t *testing.T) {
	started := time.Unix(10, 0).UTC()
	tests := []struct { runtime Runtime; valid bool }{
		{Runtime{State: RuntimeSaved}, true},
		{Runtime{State: RuntimeRunning, StartedAt: started}, true},
		{Runtime{State: RuntimeAttached, AttachedClients: 2, StartedAt: started}, true},
		{Runtime{State: "other"}, false},
		{Runtime{State: RuntimeSaved, AttachedClients: 1}, false},
		{Runtime{State: RuntimeRunning, AttachedClients: 1, StartedAt: started}, false},
		{Runtime{State: RuntimeAttached, StartedAt: started}, false},
		{Runtime{State: RuntimeRunning}, false},
	}
	for _, test := range tests {
		err := ValidateRuntime(test.runtime)
		if (err == nil) != test.valid { t.Fatalf("%#v error = %v", test.runtime, err) }
	}
}
```

- [ ] **Step 2: Run RED**

```sh
go test ./internal/session -run TestValidateRuntime -count=1
```

Expected: undefined runtime types.

- [ ] **Step 3: Implement the model**

```go
type RuntimeState string
const (
	RuntimeSaved RuntimeState = "saved"
	RuntimeRunning RuntimeState = "running"
	RuntimeAttached RuntimeState = "attached"
)
type Runtime struct { State RuntimeState; AttachedClients int; StartedAt time.Time }
type Discovered struct { Candidate Candidate; Runtime Runtime }
type Session struct { Host string; Candidate; Runtime Runtime }

func ValidateRuntime(value Runtime) error {
	switch value.State {
	case RuntimeSaved:
		if value.AttachedClients != 0 || !value.StartedAt.IsZero() { return fmt.Errorf("saved runtime has live metadata") }
	case RuntimeRunning:
		if value.AttachedClients != 0 || value.StartedAt.IsZero() { return fmt.Errorf("invalid running runtime") }
	case RuntimeAttached:
		if value.AttachedClients < 1 || value.StartedAt.IsZero() { return fmt.Errorf("invalid attached runtime") }
	default:
		return fmt.Errorf("invalid runtime state")
	}
	return nil
}

func BindDiscovered(host string, value Discovered) (Session, error) {
	if err := validateHost(host); err != nil { return Session{}, err }
	if err := ValidateCandidate(value.Candidate); err != nil { return Session{}, err }
	if err := ValidateRuntime(value.Runtime); err != nil { return Session{}, err }
	return Session{Host: host, Candidate: value.Candidate, Runtime: value.Runtime}, nil
}

func Bind(host string, candidate Candidate) (Session, error) {
	return BindDiscovered(host, Discovered{Candidate: candidate, Runtime: Runtime{State: RuntimeSaved}})
}
```

- [ ] **Step 4: Verify GREEN and JSON compatibility**

```sh
gofmt -w internal/session/session.go internal/session/session_test.go
go test ./internal/session ./internal/output -count=1
git diff --check
```

- [ ] **Step 5: Commit**

```sh
git add internal/session/session.go internal/session/session_test.go
git commit -m "feat: model persistent session runtime"
```

---

### Task 3: Inspect the dedicated ARS tmux server

**Files:**
- Create: `internal/runtime/runner.go`
- Create: `internal/runtime/inspect.go`
- Test: `internal/runtime/inspect_test.go`

**Interfaces:**
- Consumes: `session.Candidate`, `session.Runtime`
- Produces: `Key(provider, nativeID string) string`, `Report`, `Inspect(context.Context, Runner, []session.Candidate)`

- [ ] **Step 1: Write failing key and inspection tests**

```go
func TestKeyIsStableAndSeparatesProvider(t *testing.T) {
	a := Key("claude", claudeID)
	b := Key("codex", claudeID)
	if a == b || !strings.HasPrefix(a, "ars-") || len(a) != 68 { t.Fatalf("keys = %q %q", a, b) }
	if a != Key("claude", claudeID) { t.Fatal("unstable key") }
}

func TestInspectMapsExactRuntimeRows(t *testing.T) {
	candidates := []session.Candidate{candidate(session.Claude, claudeID), candidate(session.Codex, codexID)}
	runner := &fakeRunner{output: []byte(
		Key("claude", claudeID)+"\t0\t10\n"+
		Key("codex", codexID)+"\t2\t20\n"+
		"unowned\t1\t30\n")}
	got, report := Inspect(context.Background(), runner, candidates)
	if report.Status != StatusOK { t.Fatalf("report = %#v", report) }
	if got[Key("claude", claudeID)].State != session.RuntimeRunning { t.Fatalf("claude = %#v", got) }
	if got[Key("codex", codexID)].State != session.RuntimeAttached ||
		got[Key("codex", codexID)].AttachedClients != 2 { t.Fatalf("codex = %#v", got) }
}
```

Also assert exit code 1 means an empty ARS server, `exec.ErrNotFound` means `tmux_unavailable`, malformed/duplicate rows mean `tmux_failed`, and output is bounded.

- [ ] **Step 2: Run RED**

```sh
go test ./internal/runtime -run 'Test(Key|Inspect)' -count=1
```

Expected: package or symbols do not exist.

- [ ] **Step 3: Implement the runner, stable key, report, and parser**

```go
const SocketName = "ars-v1"

type Command struct { Name string; Args, Env []string; Dir string }
type Runner interface {
	Output(context.Context, Command) ([]byte, error)
	Run(context.Context, Command, io.Reader, io.Writer, io.Writer) error
}

type SystemRunner struct{}

type Status string
const (
	StatusOK Status = "ok"
	StatusUnavailable Status = "unavailable"
	StatusFailed Status = "failed"
)
type Report struct { Status Status; ErrorCode string }

func Key(provider, nativeID string) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%d:%s%d:%s", len(provider), provider, len(nativeID), nativeID)
	return "ars-" + hex.EncodeToString(hash.Sum(nil))
}
```

`Inspect` initializes every candidate to saved and runs:

```go
Command{
	Name: "tmux",
	Args: []string{"-L", SocketName, "-f", "/dev/null", "list-sessions", "-F",
		"#{session_name}\t#{session_attached}\t#{session_created}"},
	Env: []string{"TMUX=", "TMUX_PANE=", "TMUX_TMPDIR=/tmp"},
}
```

Accept only exact known keys, decimal non-negative client counts, and positive Unix creation seconds. Exit 1 is empty; missing executable is unavailable; all other command/parse/resource failures are failed. Do not return tmux names not derived from provider history.

- [ ] **Step 4: Verify GREEN**

```sh
gofmt -w internal/runtime
go test ./internal/runtime -count=1
go test -race ./internal/session ./internal/runtime -count=1
git diff --check
```

- [ ] **Step 5: Commit**

```sh
git add internal/runtime
git commit -m "feat: inspect ars tmux runtimes"
```

---

### Task 4: Implement local and remote start-or-attach

**Files:**
- Create: `internal/runtime/attach.go`
- Test: `internal/runtime/attach_test.go`
- Create: `internal/ssh/attach.go`
- Test: `internal/ssh/attach_test.go`
- Modify: `internal/provider/provider.go`
- Test: `internal/provider/provider_test.go`

**Interfaces:**
- Consumes: runtime key, provider `ResumeSpec`, canonical session validation
- Produces: local `runtime.AttachCommand` and remote `*exec.Cmd`; both satisfy Bubble Tea's `ExecCommand` contract without importing Bubble Tea

- [ ] **Step 1: Write failing attach tests**

```go
func TestAttachCommandCreatesBindsAndAttachesOnce(t *testing.T) {
	runner := &attachRunner{hasErrors: []error{exitError{code: 1}, nil}}
	item := attachedSession()
	cmd, err := NewAttachCommand(context.Background(), runner, item, claudeSpec())
	if err != nil { t.Fatal(err) }
	cmd.SetStdin(strings.NewReader("")); cmd.SetStdout(io.Discard); cmd.SetStderr(io.Discard)
	if err := cmd.Run(); err != nil { t.Fatal(err) }
	want := []string{"has-session", "new-session", "has-session", "bind-key", "attach-session"}
	if !slices.Equal(runner.commandNames(), want) { t.Fatalf("commands = %v", runner.commandNames()) }
}

func TestAttachCommandDoesNotRestartExistingRuntime(t *testing.T) {
	runner := &attachRunner{hasErrors: []error{nil}}
	cmd, _ := NewAttachCommand(context.Background(), runner, attachedSession(), claudeSpec())
	cmd.SetStdin(strings.NewReader("")); cmd.SetStdout(io.Discard); cmd.SetStderr(io.Discard)
	if err := cmd.Run(); err != nil { t.Fatal(err) }
	if slices.Contains(runner.commandNames(), "new-session") { t.Fatal("runtime restarted") }
}

func TestRemoteAttachUsesOneTargetAndFixedLauncher(t *testing.T) {
	item := attachedSession(); item.Host = "user@host;$literal"
	cmd, err := NewAttachCommand(context.Background(), item.Host, item, claudeSpec())
	if err != nil { t.Fatal(err) }
	if cmd.Path != "ssh" || !slices.Equal(cmd.Args[:3], []string{"ssh", "-tt", item.Host}) {
		t.Fatalf("argv = %#v", cmd.Args)
	}
	script := cmd.Args[3]
	for _, want := range []string{"TMUX_TMPDIR=/tmp", "bind-key -n C-q detach-client", "attach-session -d"} {
		if !strings.Contains(script, want) { t.Fatalf("script missing %q: %s", want, script) }
	}
}
```

- [ ] **Step 2: Run RED**

```sh
go test ./internal/runtime ./internal/ssh -run 'Test(AttachCommand|RemoteAttach)' -count=1
```

Expected: constructors are undefined.

- [ ] **Step 3: Implement exact create/race/bind/attach behavior**

`AttachCommand` implements `Run`, `SetStdin`, `SetStdout`, and `SetStderr`. Its `Run` sequence is:

```go
func (command *AttachCommand) Run() error {
	name := Key(string(command.item.Provider), command.item.NativeID)
	if err := command.runner.Run(command.ctx, hasSession(name), nil, io.Discard, io.Discard); err != nil {
		if createErr := command.runner.Run(command.ctx, newSession(name, command.item.CWD, command.spec), nil, io.Discard, command.stderr); createErr != nil {
			if checkErr := command.runner.Run(command.ctx, hasSession(name), nil, io.Discard, io.Discard); checkErr != nil {
				return fmt.Errorf("create runtime: %w", createErr)
			}
		}
	}
	if err := command.runner.Run(command.ctx, bindDetach(), nil, io.Discard, command.stderr); err != nil {
		return fmt.Errorf("bind detach key: %w", err)
	}
	return command.runner.Run(command.ctx, attachSession(name), command.stdin, command.stdout, command.stderr)
}
```

All tmux calls use `-L ars-v1 -f /dev/null`, `TMUX=`, `TMUX_PANE=`, `TMUX_TMPDIR=/tmp`, and exact `-t =<key>`. Detached creation passes CWD, provider executable, and argv as separate arguments and never uses `new-session -A`.

Move resume-spec checking to `provider.ValidResumeSpec(provider, id, spec)` so local and remote paths reject invalid sessions/specs before tmux or SSH.

Remote attach builds the same logic as one fixed `set -eu` POSIX script with all data shell-quoted, then returns:

```go
exec.CommandContext(ctx, "ssh", "-tt", target, script)
```

- [ ] **Step 4: Verify GREEN and exit-code preservation**

```sh
gofmt -w internal/runtime internal/ssh/attach.go internal/ssh/attach_test.go internal/provider
go test ./internal/runtime ./internal/ssh ./internal/provider -count=1
go test -race ./internal/runtime ./internal/ssh -count=1
git diff --check
```

- [ ] **Step 5: Commit**

```sh
git add internal/runtime internal/ssh/attach.go internal/ssh/attach_test.go internal/provider
git commit -m "feat: attach persistent ars runtimes"
```

---


### Task 5: Carry runtime state through ARS/2 and collection

**Files:**
- Modify: `internal/provider/provider.go`, `internal/provider/provider_test.go`
- Modify: `internal/protocol/protocol.go`, `internal/protocol/protocol_test.go`, `internal/protocol/fuzz_test.go`
- Modify: `cmd/ars-collector/main.go`, `cmd/ars-collector/main_test.go`
- Modify: `internal/ssh/collect.go`, `internal/ssh/collect_test.go`
- Modify: `internal/app/aggregate.go`, `internal/app/aggregate_test.go`
- Verify: `internal/output/json_test.go`

**Interfaces:**
- Produces: `provider.DiscoverAll`, ARS/2 `Encode/Decode` with `session.Discovered` and `runtime.Report`, and app warnings separate from fatal errors
- Preserves: all collection bounds and public JSON v1

- [ ] **Step 1: Write failing ARS/2 and warning tests**

```go
func TestRoundTripARS2RuntimeState(t *testing.T) {
	discovered := []session.Discovered{
		{Candidate: validCandidate(session.Claude, claudeID), Runtime: session.Runtime{State: session.RuntimeSaved}},
		{Candidate: validCandidate(session.Codex, codexID), Runtime: session.Runtime{
			State: session.RuntimeAttached, AttachedClients: 1, StartedAt: time.Unix(10, 0).UTC()}},
	}
	var encoded bytes.Buffer
	if err := Encode(&encoded, testNonce, discovered, validResults(discovered), runtime.Report{Status: runtime.StatusOK}); err != nil { t.Fatal(err) }
	if !strings.HasPrefix(encoded.String(), "ARS/2 BEGIN ") { t.Fatalf("protocol = %q", encoded.String()) }
	got, _, report, err := Decode(&encoded, testNonce, DefaultLimits())
	if err != nil { t.Fatal(err) }
	if !reflect.DeepEqual(got, discovered) || report.Status != runtime.StatusOK { t.Fatalf("decoded = %#v %#v", got, report) }
}

func TestCollectHostsKeepsSessionsBesideRuntimeWarning(t *testing.T) {
	collector := func(context.Context, Host) ([]session.Discovered, []provider.Result, runtime.Report, error) {
		return []session.Discovered{{Candidate: candidate(), Runtime: session.Runtime{State: session.RuntimeSaved}}},
			nil, runtime.Report{Status: runtime.StatusUnavailable, ErrorCode: "tmux_unavailable"}, nil
	}
	got := CollectHosts(context.Background(), []Host{{Target: "macbook", Local: true}}, 1, collector)
	if len(got.Sessions) != 1 || got.Hosts[0].Status != output.HostOK || len(got.Warnings) != 1 {
		t.Fatalf("result = %#v", got)
	}
}
```

- [ ] **Step 2: Run RED**

```sh
go test ./internal/protocol ./internal/app -run 'Test(RoundTripARS2|CollectHostsKeepsSessions)' -count=1
```

Expected: protocol and collector signatures lack runtime data.

- [ ] **Step 3: Extract shared provider discovery and define ARS/2 frames**

Move the adapter loop from `cmd/ars-collector` to:

```go
func DiscoverAll(ctx context.Context, home string, adapters []Adapter) ([]session.Candidate, []Result, error)
```

Preserve registry validation, ordering, the 10,000-session ceiling, and sanitized candidate-only results.

Use this strict private frame shape:

```go
type sessionFrame struct {
	Type string `json:"type"`
	Provider session.Provider `json:"provider"`
	NativeID string `json:"native_id"`
	UpdatedAt time.Time `json:"updated_at"`
	CWD string `json:"cwd"`
	Title string `json:"title"`
	RuntimeState session.RuntimeState `json:"runtime_state"`
	AttachedClients int `json:"attached_clients"`
	RuntimeStarted *time.Time `json:"runtime_started_at,omitempty"`
}
type runtimeFrame struct {
	Type string `json:"type"`
	Status runtime.Status `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
}
```

Require `ARS/2 BEGIN/END`, exactly one runtime summary, valid runtime combinations, matching counts/nonces, known records/providers, bounded UTF-8 lines/total/session count, and EOF after the end frame. Reject ARS/1 and add all runtime states to fuzz seeds.

- [ ] **Step 4: Wire collector, SSH decode, and warning aggregation**

`cmd/ars-collector` calls `DiscoverAll`, `runtime.Inspect`, combines every candidate with a keyed runtime or saved default, and encodes ARS/2 even when tmux is unavailable.

Change SSH and app boundaries to:

```go
func Collect(...) ([]session.Discovered, []provider.Result, runtime.Report, error)

type Collector func(context.Context, Host) ([]session.Discovered, []provider.Result, runtime.Report, error)

type Result struct {
	Hosts []output.HostResult
	Sessions []session.Session
	Errors []output.HostError
	Warnings []output.HostError
}
```

Bind with `session.BindDiscovered`. Runtime unavailable/failed becomes a sanitized warning while sessions and `HostOK` remain. Fatal SSH/protocol/resource failures keep existing behavior. Do not pass warnings to `output.WriteJSON`.

- [ ] **Step 5: Verify protocol, fuzz, race, and JSON compatibility**

```sh
gofmt -w internal/provider internal/protocol cmd/ars-collector internal/ssh/collect.go internal/ssh/collect_test.go internal/app/aggregate.go internal/app/aggregate_test.go
go test ./internal/provider ./internal/protocol ./cmd/ars-collector ./internal/ssh ./internal/app ./internal/output -count=1
go test ./internal/protocol -run '^$' -fuzz FuzzDecode -fuzztime 10s
go test -race ./internal/protocol ./internal/ssh ./internal/app -count=1
git diff --check
```

Expected: ARS/2 passes and JSON v1 golden output is unchanged.

- [ ] **Step 6: Commit**

```sh
git add internal/provider internal/protocol cmd/ars-collector internal/ssh/collect.go internal/ssh/collect_test.go internal/app/aggregate.go internal/app/aggregate_test.go internal/output/json_test.go
git commit -m "feat: collect ars runtime state"
```

---

### Task 6: Build the full-screen one-line TUI

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/tui/model.go`, `internal/tui/filter.go`, `internal/tui/view.go`, `internal/tui/run.go`
- Test: `internal/tui/model_test.go`, `internal/tui/filter_test.go`, `internal/tui/view_test.go`

**Interfaces:**
- Consumes: sessions, host results/errors/warnings, attach commands satisfying Bubble Tea `ExecCommand`
- Produces: `tui.Run(context.Context, Dependencies, io.Reader, io.Writer) error`
- Constraint: only this package imports Bubble Tea/Lip Gloss

- [ ] **Step 1: Pin dependencies and write failing model tests**

```sh
go get charm.land/bubbletea/v2@v2.0.8 charm.land/lipgloss/v2@v2.0.5
```

Define:

```go
type Result struct {
	Hosts []output.HostResult
	Sessions []session.Session
	Errors, Warnings []output.HostError
}
type Dependencies struct {
	Collect func(context.Context) Result
	Attach func(context.Context, session.Session) (tea.ExecCommand, error)
	LocalTarget string
	Now func() time.Time
	NoColor bool
}
```

Test initial collection, `j/k`, search, refresh coalescing, stale generations, attach, and canonical selection retention:

```go
func TestModelNavigatesFiltersAndAttaches(t *testing.T) {
	deps := fakeDependencies(twoSessions())
	model := newModel(context.Background(), deps)
	if model.Init() == nil || !model.collecting { t.Fatal("Init did not collect") }
	model, _ = updateModel(model, collectDoneMsg{generation: 1, result: deps.result})
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	if model.selectedKey != keyOf(deps.result.Sessions[1]) { t.Fatal("selection did not move") }
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: '/'}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "api"}))
	if len(model.visible) != 1 { t.Fatalf("visible = %#v", model.visible) }
	_, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil { t.Fatal("Enter did not attach") }
}
```

- [ ] **Step 2: Run RED**

```sh
go test ./internal/tui -count=1
```

Expected: TUI package/model does not exist.

- [ ] **Step 3: Implement model, filter, and terminal handoff**

Messages are only:

```go
type collectDoneMsg struct { generation uint64; result Result }
type attachDoneMsg struct { err error }
```

`Init` starts generation 1. While collecting, `r` is ignored. Stale generations are ignored. Search appends printable `msg.Key().Text`, removes one rune on Backspace, and exits edit mode on Esc/Enter while retaining the query. Filter with case-folded substring matching over title/provider/rendered location/project/CWD/native ID. Preserve selection by `(Host, Provider, NativeID)`, not row text/index.

Attach with:

```go
return model, tea.Exec(command, func(err error) tea.Msg {
	return attachDoneMsg{err: err}
})
```

`attachDoneMsg` stores a bounded error/status then starts exactly one fresh collection.

- [ ] **Step 4: Write failing responsive and colorless view tests**

```go
func TestViewRendersOneLineGroupsAndNeutralProviderLocation(t *testing.T) {
	model := readyModel(); model.width = 120; model.height = 24
	content := model.View().Content
	for _, want := range []string{"Active", "Recent", "claude", "local", "attached(1)", "↑↓/jk move"} {
		if !strings.Contains(content, want) { t.Fatalf("missing %q: %q", want, content) }
	}
}

func TestNarrowNoColorViewKeepsRequiredFields(t *testing.T) {
	model := readyModel(); model.width = 60; model.height = 12; model.noColor = true
	content := model.View().Content
	if ansi.Strip(content) != content { t.Fatalf("NO_COLOR emitted ANSI: %q", content) }
	for _, want := range []string{"local", "attached", "1d"} {
		if !strings.Contains(content, want) { t.Fatalf("missing %q", want) }
	}
}
```

- [ ] **Step 5: Implement view and runner**

`View` returns `tea.View{Content: content, AltScreen: true}`. Render one-line Active/Recent groups, selected footer with full CWD/ID/exact time, bounded errors, and help. Apply style only to selection/state/error; provider and local/host use normal foreground. When `NoColor` or `NO_COLOR` is set, emit no ANSI. Remove project, provider, and client count in that order at narrow widths.

```go
func Run(ctx context.Context, deps Dependencies, input io.Reader, output io.Writer) error {
	if deps.Collect == nil || deps.Attach == nil { return fmt.Errorf("invalid TUI dependencies") }
	program := tea.NewProgram(newModel(ctx, deps),
		tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output))
	_, err := program.Run()
	return err
}
```

Use Lip Gloss width/truncation so ANSI never corrupts columns.

- [ ] **Step 6: Verify TUI and import boundary**

```sh
gofmt -w internal/tui
go test ./internal/tui -count=1
go test -race ./internal/tui -count=1
test -z "$(rg -l 'charm.land/(bubbletea|lipgloss)' --glob '*.go' | grep -v '^internal/tui/')"
git diff --check
```

- [ ] **Step 7: Commit**

```sh
git add go.mod go.sum internal/tui
git commit -m "feat: add full-screen ars tui"
```

---


### Task 7: Route CLI modes and remove fzf/direct resume

**Files:**
- Modify: `internal/app/app.go`, `internal/app/app_test.go`
- Modify: `cmd/ars/main.go`
- Modify: `internal/app/e2e_test.go`
- Delete: `internal/output/fzf.go`, `internal/output/fzf_test.go`
- Delete: `internal/ssh/resume.go`, `internal/ssh/resume_test.go`

**Interfaces:**
- Consumes: topology, runtime-aware collectors, TUI, local/remote attach constructors
- Produces: `ars`, `ars <host>`, `ars list --json`, `ars local set <host>`, preserved remote add/help

- [ ] **Step 1: Replace picker/resume tests with interactive callback tests**

```go
func TestRunRoutesInteractiveAndJSONSeparately(t *testing.T) {
	deps, stdout, stderr := appDependencies()
	calls := 0
	deps.RunInteractive = func(_ context.Context, hosts []Host) error {
		calls++
		if len(hosts) != 2 || !hosts[0].Local { t.Fatalf("hosts = %#v", hosts) }
		return nil
	}
	if code := Run(context.Background(), nil, deps); code != 0 { t.Fatalf("interactive = %d: %s", code, stderr) }
	if code := Run(context.Background(), []string{"list", "--json"}, deps); code != 0 { t.Fatalf("json = %d", code) }
	if calls != 1 || !strings.Contains(stdout.String(), `"schema_version":1`) { t.Fatalf("calls/output = %d %q", calls, stdout) }
}

func TestRunSetsLocalWithoutCollectingOrStartingTUI(t *testing.T) {
	deps, _, stderr := appDependencies(); called := false
	deps.SetLocal = func(_, _, target string) error { called = target == "macbook"; return nil }
	deps.Collect = func(context.Context, []Host) Result { t.Fatal("Collect called"); return Result{} }
	deps.RunInteractive = func(context.Context, []Host) error { t.Fatal("TUI called"); return nil }
	if code := Run(context.Background(), []string{"local", "set", "macbook"}, deps); code != 0 || !called {
		t.Fatalf("code/called = %d/%v: %s", code, called, stderr)
	}
}
```

- [ ] **Step 2: Run RED**

```sh
go test ./internal/app -run 'TestRun(RoutesInteractive|SetsLocal)' -count=1
```

Expected: `RunInteractive` and `SetLocal` dependencies do not exist.

- [ ] **Step 3: Implement the headless/interactive split**

```go
type Dependencies struct {
	LoadTopology func(string, string) ([]Host, error)
	AddHost func(string, string) error
	SetLocal func(string, string, string) error
	Collect func(context.Context, []Host) Result
	RunInteractive func(context.Context, []Host) error
	Stdout, Stderr io.Writer
}
```

Parse exact forms only. Help includes `ars local set <host>`. `local set` resolves both config paths and writes without collecting. JSON loads/selects topology, collects once, and writes JSON without TUI. Interactive modes load/select topology and call `RunInteractive`; all-host failure is rendered in the TUI, not rejected before opening it.

- [ ] **Step 4: Wire local/remote collection and attach factories**

In `cmd/ars/main.go`, build one collector:

```go
collectHost := func(ctx context.Context, host app.Host) ([]session.Discovered, []provider.Result, runtime.Report, error) {
	if host.Local {
		home, err := os.UserHomeDir()
		if err != nil { return nil, nil, runtime.Report{}, err }
		candidates, results, err := provider.DiscoverAll(ctx, home, provider.Builtin())
		if err != nil { return nil, nil, runtime.Report{}, err }
		states, report := runtime.Inspect(ctx, runtimeRunner, candidates)
		return combineRuntime(candidates, states), results, report, nil
	}
	return ssh.Collect(ctx, sshRunner, assets, host.Target, collectOptions)
}
```

`RunInteractive` closes over selected hosts and calls `tui.Run`. Its Collect callback calls `app.CollectHosts`. Its Attach callback looks up the validated provider spec and returns local `runtime.NewAttachCommand` when the canonical host is marked local, otherwise `ssh.NewAttachCommand`. Never route on rendered text.

- [ ] **Step 5: Remove fzf/direct resume and update synthetic E2E**

Delete the four old files. Update E2E to cover common topology, local direct collection, remote ARS/2 collection, canonical TUI result, local/remote attach commands, attach-return refresh, JSON without TUI, and no command named `fzf`.

- [ ] **Step 6: Verify CLI and packages**

```sh
gofmt -w cmd/ars/main.go internal/app
go test ./cmd/ars ./internal/app ./internal/tui ./internal/runtime ./internal/ssh ./internal/output -count=1
go test -race ./internal/app ./internal/tui ./internal/runtime ./internal/ssh -count=1
test -z "$(rg -l 'fzf|\.Pick|\.Resume' cmd internal --glob '*.go')"
git diff --check
```

- [ ] **Step 7: Commit**

```sh
git add cmd/ars/main.go internal/app internal/output/fzf.go internal/output/fzf_test.go internal/ssh/resume.go internal/ssh/resume_test.go
git commit -m "feat: route ars through persistent tui"
```

---

### Task 8: Close PTY, tmux, SSH, docs, and release gates

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/runtime/tmux_integration_test.go`
- Create: `internal/tui/pty_integration_test.go`
- Modify: `internal/ssh/sshd_integration_test.go`
- Modify: `internal/app/e2e_test.go`
- Modify: `README.md`, `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: completed topology/runtime/ARS2/TUI/CLI
- Produces: automated terminal/runtime proof, operator contract, separate real-host checklist

- [ ] **Step 1: Pin PTY test dependency and write failing scenarios**

```sh
go get github.com/creack/pty@v1.1.24
```

Add a disposable tmux test using `t.TempDir()` as `TMUX_TMPDIR`, a unique socket, and a fake provider that writes its PID then blocks. Assert the PID and runtime survive detach:

```go
if beforePID != afterDetachPID { t.Fatalf("provider restarted: %d -> %d", beforePID, afterDetachPID) }
if attachedClients != 0 { t.Fatalf("clients after Ctrl+Q = %d", attachedClients) }
```

Add a PTY test that starts a fixture TUI, waits for the ARS header, sends Enter, waits for fake provider attach, writes byte `0x11` (`Ctrl+Q`), waits for the same TUI header, writes `q`, and expects clean EOF/exit. Failure to restore raw mode, cursor, or alternate screen must fail the test.

- [ ] **Step 2: Run integrations and verify RED**

```sh
go test ./internal/runtime -run TestDisposableTmux -count=1 -v
go test ./internal/tui -run TestPTYAttachDetachRestoresTUI -count=1 -v
```

Expected: fixture/integration wiring is incomplete.

- [ ] **Step 3: Complete disposable tmux, PTY, and sshd proof**

Keep every socket, fake provider, key, config, and log inside exact temporary paths. Cleanup only those paths/processes. Extend ephemeral sshd to verify remote tmux create, `Ctrl+Q`, PID preservation, second-client handoff, and host-key checking without persistent system changes.

App E2E covers one local node, one healthy peer, one unreachable peer, saved/running/attached rows, a runtime warning beside healthy sessions, attach-return refresh, unchanged JSON, and privacy scanning.

- [ ] **Step 4: Rewrite README and CI**

Document: tmux required/no fzf; common roster plus `ars local set`; one-line Active/Recent screen and keys; start/attach/`Ctrl+Q`/peer handoff/provider exit/reboot; metadata-only privacy; JSON v1; no adoption/polling; partial failures.

CI builds ARS/2 assets before tests and runs PTY coverage on supported runners. Real two-host acceptance remains manual and separate.

- [ ] **Step 5: Run complete automated release proof**

```sh
go run ./cmd/ars-build --assets-only
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/ars-build
go test ./internal/runtime -run TestDisposableTmux -count=1 -v
go test ./internal/tui -run TestPTYAttachDetachRestoresTUI -count=1 -v
ARS_RUN_SSHD_INTEGRATION=1 go test ./internal/ssh -run TestEphemeralSSHDCollectsAndAttaches -count=1 -v
npm test
git diff --check
```

Expected: all pass. Record automated proof separately from live acceptance.

- [ ] **Step 6: Run real two-host acceptance**

```text
A and B use the same ARS build and common roster
A shows itself local; B shows itself local; canonical JSON session sets match
A starts Claude → Ctrl+Q → TUI returns and PID remains
B attaches A/Claude → A returns to TUI and the same PID remains
repeat for Codex
network loss returns to TUI without killing provider
unreachable peer appears beside healthy sessions
default user tmux server/config/keys/sessions remain unchanged
```

If inventory, SSH, DNS, tmux, or provider readiness blocks this checklist, report live acceptance incomplete; never waive it into a pass.

- [ ] **Step 7: Commit**

```sh
git add go.mod go.sum internal/runtime/tmux_integration_test.go internal/tui/pty_integration_test.go internal/ssh/sshd_integration_test.go internal/app/e2e_test.go README.md .github/workflows/ci.yml
git commit -m "test: verify persistent ars tui flow"
```

---


+## Final review gate

Run a fresh whole-change review against `docs/superpowers/specs/2026-07-20-ars-tui-attach-design.md`. Reject completion for prompt/response transfer, display-to-command parsing, duplicate provider processes, user-tmux mutation, JSON v1 drift, fzf residue, unbounded terminal/SSH data, or simulated results reported as live proof.
