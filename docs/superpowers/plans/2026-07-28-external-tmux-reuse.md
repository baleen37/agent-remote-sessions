# External tmux Reuse Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reuse one exactly identified Claude Code or Codex process already running in a standard per-user tmux pane before ARS creates its own runtime.

**Architecture:** Add one bounded POSIX resolver owned by `internal/runtime`. Local attach executes it through the existing `Runner`; remote attach embeds the exact same resolver in its quoted `ssh -tt` script. The resolver returns only `none` or one validated socket/pane locator, while conflicts and incomplete inspection fail closed. No locator enters the session model, cache, private protocol, public JSON, or TUI state.

**Tech Stack:** Go 1.26, POSIX `/bin/sh`, tmux 3.x, OpenSSH, existing PTY and ephemeral-SSHD test fixtures.

## Global Constraints

- Match only exact `claude --resume <native-id>` and `codex resume <native-id>` process arguments.
- Infer nothing from CWD, title, activity time, pane content, or a provider process without a native ID.
- Inspect only current-user sockets in standard `tmux-<uid>` directories below `${TMUX_TMPDIR:-/tmp}` and `/tmp`; arbitrary `tmux -S` paths remain out of scope.
- Preserve clients already attached to an external tmux session.
- Do not change external tmux keys, options, status lines, windows, panes, or sessions.
- Keep the existing ARS-owned runtime first in attach priority and create it only after a complete zero-match scan.
- Fail without creating a provider process on conflicts, incomplete inspection, or a vanished matched target.
- Keep external runtime state out of `Active`, preview, send, kill, cache, ARS/3, and public JSON.
- Use the same resolver script for local and SSH attach.
- Keep scans and diagnostics bounded and read no pane or transcript content.

---

## File Map

- Create `internal/runtime/external.go`: shared resolver script, bounded result parsing, external target validation, and external attach command.
- Create `internal/runtime/external_test.go`: fake-command unit tests for exact matching, ancestry, bounds, conflicts, and malformed input.
- Modify `internal/runtime/attach.go`: insert external resolution between ARS lookup and ARS creation.
- Modify `internal/runtime/attach_test.go`: verify attach priority, fallback, conflict, no mutation, and vanished-target behavior.
- Modify `internal/runtime/tmux_integration_test.go`: prove a real external tmux provider and existing client are reused and preserved.
- Modify `internal/ssh/attach.go`: embed and branch on the shared resolver result before remote ARS creation.
- Modify `internal/ssh/attach_test.go`: verify quoting, branch order, exact external attach, and unchanged ARS behavior.
- Modify `internal/ssh/sshd_integration_test.go`: prove external reuse through a real disposable SSH server.
- Modify `README.md`: document exact external reuse, normal external tmux detach, and the explicit-ID limitation.

---

### Task 1: Build the bounded shared external tmux resolver

**Files:**
- Create: `internal/runtime/external.go`
- Create: `internal/runtime/external_test.go`

**Interfaces:**
- Consumes: validated `session.Provider`, canonical native UUID, `runtime.Runner`, and standard tmux/process command output.
- Produces:

```go
type ExternalTarget struct {
	Socket string
	Pane   string
}

func ExternalResolverScript() string
func ResolveExternal(
	ctx context.Context,
	runner Runner,
	provider session.Provider,
	nativeID string,
) (ExternalTarget, bool, error)
```

- `ExternalResolverScript` is the sole resolver implementation used by local and remote attach.
- `ResolveExternal` invokes `/bin/sh -c ExternalResolverScript()` with provider and ID as positional arguments and parses its bounded stdout.

- [ ] **Step 1: Write failing tests for the resolver result protocol and input validation**

Add table tests that name the production behavior explicitly:

```go
func TestResolveExternalReturnsNoneForCompleteEmptyScan(t *testing.T) {
	runner := &externalRunner{output: []byte("none\n")}
	target, found, err := ResolveExternal(
		context.Background(),
		runner,
		session.Claude,
		"123e4567-e89b-42d3-a456-426614174000",
	)
	if err != nil || found || target != (ExternalTarget{}) {
		t.Fatalf("ResolveExternal() = (%#v, %t, %v), want zero, false, nil", target, found, err)
	}
}

func TestResolveExternalReturnsOneValidatedTarget(t *testing.T) {
	runner := &externalRunner{
		output: []byte("match\t/private/tmp/tmux-502/default\t$19:0.1\n"),
	}
	target, found, err := ResolveExternal(
		context.Background(),
		runner,
		session.Codex,
		"019fa13c-3a32-7922-b8c2-4b4adf8eadac",
	)
	want := ExternalTarget{
		Socket: "/private/tmp/tmux-502/default",
		Pane:   "$19:0.1",
	}
	if err != nil || !found || target != want {
		t.Fatalf("ResolveExternal() = (%#v, %t, %v), want %#v, true, nil", target, found, err, want)
	}
}
```

Also reject nil runners, unsupported providers, non-canonical UUIDs, extra fields, relative sockets, tabs/newlines/NUL, invalid pane targets, duplicate lines, and output over the existing 2 MiB runner limit.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
go test ./internal/runtime -run 'TestResolveExternal' -count=1
```

Expected: compilation fails because `ExternalTarget` and `ResolveExternal` do not exist.

- [ ] **Step 3: Implement the result type, command construction, and strict output parser**

Start `internal/runtime/external.go` with the exact public boundary:

```go
package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

type ExternalTarget struct {
	Socket string
	Pane   string
}

func ResolveExternal(
	ctx context.Context,
	runner Runner,
	name session.Provider,
	nativeID string,
) (ExternalTarget, bool, error) {
	if runner == nil {
		return ExternalTarget{}, false, errors.New("external tmux runner is nil")
	}
	adapter, ok := provider.Lookup(name)
	if !ok {
		return ExternalTarget{}, false, errors.New("unsupported external tmux provider")
	}
	if err := adapter.ValidateID(nativeID); err != nil {
		return ExternalTarget{}, false, err
	}
	output, err := runner.Output(ctx, Command{
		Name: "/bin/sh",
		Args: []string{"-c", ExternalResolverScript(), "ars-external", string(name), nativeID},
		Env:  []string{"TMUX=", "TMUX_PANE="},
	})
	if err != nil {
		return ExternalTarget{}, false, fmt.Errorf("resolve external tmux: %w", err)
	}
	return parseExternalResult(output)
}

func parseExternalResult(output []byte) (ExternalTarget, bool, error) {
	if len(output) == 0 || output[len(output)-1] != '\n' ||
		strings.Count(string(output), "\n") != 1 {
		return ExternalTarget{}, false, errors.New("invalid external tmux result")
	}
	line := strings.TrimSuffix(string(output), "\n")
	if line == "none" {
		return ExternalTarget{}, false, nil
	}
	fields := strings.Split(line, "\t")
	if len(fields) != 3 || fields[0] != "match" {
		return ExternalTarget{}, false, errors.New("invalid external tmux result")
	}
	target := ExternalTarget{Socket: fields[1], Pane: fields[2]}
	if !filepath.IsAbs(target.Socket) || !validExternalPane(target.Pane) {
		return ExternalTarget{}, false, errors.New("invalid external tmux target")
	}
	return target, true, nil
}

func ExternalResolverScript() string {
	return "printf 'none\\n'"
}
```

Keep `validExternalPane` deliberately narrow: `$` plus positive decimal session ID, `:`, non-negative window index, `.`, and non-negative pane index. Reject control characters and all extra syntax.

- [ ] **Step 4: Run the result-protocol tests and verify GREEN**

Run:

```bash
go test ./internal/runtime -run 'TestResolveExternal' -count=1
```

Expected: all result and validation tests pass.

- [ ] **Step 5: Write failing end-to-end resolver tests with fake `ps` and `tmux` executables**

Create a helper that:

- makes `${TMUX_TMPDIR}/tmux-<uid>/default` as a real Unix socket
- prepends a temporary `bin` directory to `PATH`
- provides fake `id`, `ps`, and `tmux` executables
- records tmux arguments without reading pane content
- emits bounded deterministic process and pane rows

Drive the fixture through one table so every case executes the real shell
script:

```go
func TestExternalResolverScript(t *testing.T) {
	const selected = "123e4567-e89b-42d3-a456-426614174000"
	tests := []struct {
		name      string
		provider  session.Provider
		fixture   externalFixture
		wantPane  string
		wantSocket string
		wantFound bool
		wantError string
	}{
		{
			name:     "exact Claude descendant",
			provider: session.Claude,
			fixture: externalFixture{
				processes: "100 1 zsh\n101 100 claude\n",
				args:      map[int]string{101: "claude --resume " + selected},
				panes:     map[string]string{"default": "$1:0.0\t100\n"},
			},
			wantPane:   "$1:0.0",
			wantSocket: "default",
			wantFound: true,
		},
		{
			name:     "exact Codex through wrapper",
			provider: session.Codex,
			fixture: externalFixture{
				processes: "200 1 zsh\n201 200 env\n202 201 codex\n",
				args:      map[int]string{202: "codex resume " + selected},
				panes:     map[string]string{"default": "$2:1.0\t200\n"},
			},
			wantPane:   "$2:1.0",
			wantSocket: "default",
			wantFound: true,
		},
		{
			name:     "bare provider",
			provider: session.Claude,
			fixture: externalFixture{
				processes: "300 1 claude\n",
				args:      map[int]string{300: "claude"},
				panes:     map[string]string{"default": "$3:0.0\t300\n"},
			},
		},
		{
			name:     "shell text is not provider process",
			provider: session.Claude,
			fixture: externalFixture{
				processes: "400 1 sh\n",
				args:      map[int]string{400: "sh -c echo claude --resume " + selected},
				panes:     map[string]string{"default": "$4:0.0\t400\n"},
			},
		},
		{
			name:     "multiple exact panes",
			provider: session.Claude,
			fixture: externalFixture{
				processes: "500 1 claude\n600 1 claude\n",
				args: map[int]string{
					500: "claude --resume " + selected,
					600: "claude --resume " + selected,
				},
				panes: map[string]string{
					"default": "$5:0.0\t500\n",
					"other":   "$6:0.0\t600\n",
				},
			},
			wantError: "external tmux conflict",
		},
		{
			name:     "eligible socket inspection failure",
			provider: session.Claude,
			fixture: externalFixture{
				panes:      map[string]string{"default": ""},
				failSocket: "default",
			},
			wantError: "resolve external tmux",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target, found, err := runExternalFixture(
				t, test.fixture, test.provider, selected,
			)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil || found != test.wantFound ||
				target.Pane != test.wantPane ||
				(found && filepath.Base(target.Socket) != test.wantSocket) {
				t.Fatalf(
					"ResolveExternal() = (%#v, %t, %v), want socket %q pane %q found %t",
					target, found, err, test.wantSocket, test.wantPane, test.wantFound,
				)
			}
		})
	}
}
```

Add separate boundary tables that generate 65 sockets, 16,385 panes, 65,537
processes, a 257-deep parent chain, malformed rows, and output over 2 MiB; each
must return the named bounded error and never `match`.

The Claude fixture should expose `shell PID 100 -> claude PID 101` and tmux pane PID `100`. The Codex fixture should expose a wrapper chain `200 -> 201 -> 202` and pane PID `200`. A shell command containing the literal text `claude --resume <id>` must retain `comm=sh` and produce `none`.

- [ ] **Step 6: Run the script tests and verify RED**

Run:

```bash
go test ./internal/runtime -run 'TestExternalResolverScript' -count=1
```

Expected: tests fail because `ExternalResolverScript` does not yet inspect sockets, panes, and process ancestry.

- [ ] **Step 7: Implement the minimal bounded POSIX resolver**

Implement `ExternalResolverScript` as one constant script with these exact operations:

```sh
set -eu
provider=$1
native_id=$2
uid=$(id -u)
tmp_base=${TMPDIR:-/tmp}
case "$tmp_base" in /*) ;; *) tmp_base=/tmp ;; esac
work=$(mktemp -d "$tmp_base/ars-external.XXXXXX")
cleanup() {
	rm -f -- "$work/processes" "$work/panes"
	rmdir -- "$work"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

ps -U "$uid" -o pid=,ppid=,comm= >"$work/processes"
process_bytes=$(wc -c <"$work/processes" | tr -d ' ')
[ "$process_bytes" -le 2097152 ] || {
	echo "ars: external process table exceeds limit" >&2
	exit 1
}
```

Then:

- cap process rows at 65,536
- inspect only `comm` basenames equal to `claude` or `codex`
- query `ps -p <pid> -o args=` only for those candidates
- accept only the whole strings `claude --resume <id>`, `<absolute-path>/claude --resume <id>`, `codex resume <id>`, or `<absolute-path>/codex resume <id>`
- enumerate at most 64 owned Unix sockets across the deduplicated `${TMUX_TMPDIR:-/tmp}/tmux-<uid>` and `/tmp/tmux-<uid>` directories
- call `tmux -S <socket> -f /dev/null list-panes -a -F '#{session_id}:#{window_index}.#{pane_index}\t#{pane_pid}'`
- cap all pane output at 2 MiB and 16,384 rows
- validate every pane target and positive pane PID before ancestry matching
- walk at most 256 parents per candidate and reject cycles
- print exactly `none\n` for zero matches
- print exactly `match<TAB><absolute-socket><TAB><validated-pane>\n` for one match
- print `ars: external tmux conflict` to stderr and exit nonzero for multiple matches
- fail nonzero when any eligible socket cannot be inspected or any eligible output is malformed

Do not call `eval`, `find`, `pgrep`, `lsof`, `capture-pane`, or provider commands.

- [ ] **Step 8: Run all resolver tests and verify GREEN**

Run:

```bash
go test ./internal/runtime -run 'Test(ResolveExternal|ExternalResolverScript)' -count=1
```

Expected: all resolver tests pass with no unexpected stderr.

- [ ] **Step 9: Commit the resolver**

```bash
git add internal/runtime/external.go internal/runtime/external_test.go
git commit -m "feat(runtime): resolve external tmux sessions"
```

---

### Task 2: Reuse a unique external pane in local attach

**Files:**
- Modify: `internal/runtime/attach.go:59-130`
- Modify: `internal/runtime/attach_test.go:18-315`
- Modify: `internal/runtime/tmux_integration_test.go`

**Interfaces:**
- Consumes: `ResolveExternal(ctx, runner, provider, nativeID) (ExternalTarget, bool, error)`.
- Produces:

```go
func externalAttach(target ExternalTarget) Command
func (command *AttachCommand) createARS(name string) error
func (command *AttachCommand) attachARS(name string) error
```

- [ ] **Step 1: Write failing unit tests for attach priority and failure behavior**

Extend `attachRunner` with `outputs [][]byte` and `outputErrors []error`;
`Output` must record the command, pop one result, and fail the test on an
unexpected call. Then add:

```go
func TestAttachCommandReusesUniqueExternalPane(t *testing.T)
func TestAttachCommandCreatesARSRuntimeAfterCompleteExternalMiss(t *testing.T)
func TestAttachCommandKeepsExistingARSRuntimeAheadOfExternalProbe(t *testing.T)
func TestAttachCommandDoesNotCreateAfterExternalResolverFailure(t *testing.T)
func TestAttachCommandDoesNotFallbackAfterExternalAttachFailure(t *testing.T)
```

Implement the unique path with real command and stream assertions:

```go
func TestAttachCommandReusesUniqueExternalPane(t *testing.T) {
	runner := &attachRunner{
		hasErrors: []error{attachExitError{code: 1}},
		outputs: [][]byte{
			[]byte("match\t/private/tmp/tmux-502/default\t$19:0.1\n"),
		},
	}
	command, err := NewAttachCommand(
		context.Background(), runner, attachedSession(), claudeSpec(),
	)
	if err != nil {
		t.Fatal(err)
	}
	stdin := strings.NewReader("input")
	command.SetStdin(stdin)
	command.SetStdout(io.Discard)
	command.SetStderr(io.Discard)
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(runner.commandNames(), "new-session") ||
		slices.Contains(runner.commandNames(), "bind-key") ||
		slices.Contains(runner.commandNames(), "set-option") {
		t.Fatalf("external attach mutated ARS server: %v", runner.commandNames())
	}
	got := runner.commands[len(runner.commands)-1]
	if !reflect.DeepEqual(got, externalAttach(ExternalTarget{
		Socket: "/private/tmp/tmux-502/default",
		Pane:   "$19:0.1",
	})) {
		t.Fatalf("external attach = %#v", got)
	}
	if runner.calls[len(runner.calls)-1].stdin != stdin {
		t.Fatal("external attach lost terminal streams")
	}
}
```

Use a table for the other four tests with queued ARS `has-session` errors,
resolver outputs/errors, and external attach errors. Assert the exact operation
sequence:

```text
complete miss: has-session, resolve-external, new-session, bind-key,
               set-option, set-option, attach-session
existing ARS:  has-session, bind-key, set-option, set-option, attach-session
resolver fail: has-session, resolve-external
attach fail:   has-session, resolve-external, external attach
```

Update the test runner's operation helper so resolver calls cannot be indexed
as ARS tmux commands:

```go
func commandOperation(command Command) string {
	if command.Name == "/bin/sh" {
		return "resolve-external"
	}
	if command.Name == "tmux" && len(command.Args) > 4 {
		return command.Args[4]
	}
	return command.Name
}
```

For the unique match, assert the final command exactly:

```go
want := Command{
	Name: "tmux",
	Args: []string{
		"-S", "/private/tmp/tmux-502/default", "-f", "/dev/null",
		"attach-session", "-t", "$19:0.1",
	},
	Env: []string{"TMUX=", "TMUX_PANE="},
}
```

Also assert there is no `new-session`, `bind-key`, `set-option`, `-d`, or `Ctrl+Q` command on the external path and that stdin/stdout/stderr reach the external attach.

- [ ] **Step 2: Write the failing real-tmux test before production changes**

Add `TestDisposableTmuxReusesExternalProviderWithoutDetachingExistingClient`:

- create a unique temporary `TMUX_TMPDIR`
- start an external `tmux -L external -f /dev/null` server
- run a temporary executable named `claude` as `claude --resume <canonical-id>`
- record its PID
- attach one PTY client and wait for `session_attached=1`
- run `AttachCommand` through a second PTY
- wait for `session_attached=2`
- verify the provider PID is unchanged and the `ars-v1` server has no matching runtime
- detach only the ARS-driven client with the external server's normal `Ctrl+B`, `d`
- verify the original client remains and external keys/options/session inventory equal their pre-attach snapshot

- [ ] **Step 3: Run unit and real-tmux tests and verify RED**

Run:

```bash
go test ./internal/runtime -run 'TestAttachCommand(ReusesUniqueExternalPane|CreatesARSRuntimeAfterCompleteExternalMiss|KeepsExistingARSRuntimeAheadOfExternalProbe|DoesNotCreateAfterExternalResolverFailure|DoesNotFallbackAfterExternalAttachFailure)' -count=1
go test ./internal/runtime -run TestDisposableTmuxReusesExternalProviderWithoutDetachingExistingClient -count=1 -v
```

Expected: the unit test shows ARS creation and the real-tmux test fails before
the local attach path reaches the external pane.

- [ ] **Step 4: Insert external resolution after the exact ARS lookup**

Restructure only the absent-ARS branch:

```go
func (command *AttachCommand) Run() error {
	name := Key(string(command.item.Provider), command.item.NativeID)
	if err := command.runner.Run(command.ctx, hasSession(name), nil, io.Discard, io.Discard); err == nil {
		return command.attachARS(name)
	}

	external, found, err := ResolveExternal(
		command.ctx,
		command.runner,
		command.item.Provider,
		command.item.NativeID,
	)
	if err != nil {
		return err
	}
	if found {
		return command.runner.Run(
			command.ctx,
			externalAttach(external),
			command.stdin,
			command.stdout,
			command.stderr,
		)
	}
	if err := command.createARS(name); err != nil {
		return err
	}
	return command.attachARS(name)
}
```

Keep the existing create-race recheck inside `createARS`. Keep binding, status options, and `attach-session -d` inside `attachARS` so they are unreachable from external attach.

Build external attach without the ARS socket, forced detach, or server-global
settings:

```go
func externalAttach(target ExternalTarget) Command {
	return Command{
		Name: "tmux",
		Args: []string{
			"-S", target.Socket,
			"-f", "/dev/null",
			"attach-session", "-t", target.Pane,
		},
		Env: []string{"TMUX=", "TMUX_PANE="},
	}
}
```

- [ ] **Step 5: Run all local attach unit tests and verify GREEN**

Run:

```bash
go test ./internal/runtime -run 'Test(AttachCommand|NewAttachCommand)' -count=1
```

Expected: new and existing attach tests pass.

- [ ] **Step 6: Run the real-tmux test and verify GREEN**

Run:

```bash
go test ./internal/runtime -run TestDisposableTmuxReusesExternalProviderWithoutDetachingExistingClient -count=1 -v
```

Expected: the existing provider PID and first client survive while the second
client attaches and detaches normally.

- [ ] **Step 7: Correct only behavior proven wrong by a new failing test**

Reuse `tempTmuxRunner`, `isolatedTmuxEnv`, PTY helpers, bounded waits, and
cleanup ownership checks. If integration exposes a production command or target
bug, add the smallest failing unit test first, observe RED, apply the minimal
production fix, and rerun both tests.

- [ ] **Step 8: Run runtime verification**

Run:

```bash
go test ./internal/runtime -count=1
go test ./internal/runtime -run 'TestDisposableTmux' -count=1 -v
go test -race ./internal/runtime
```

Expected: all runtime tests pass; race reports no data races.

- [ ] **Step 9: Commit local reuse**

```bash
git add internal/runtime/attach.go internal/runtime/attach_test.go internal/runtime/tmux_integration_test.go
git commit -m "feat(runtime): reuse external tmux panes"
```

---

### Task 3: Apply the same resolver to remote attach

**Files:**
- Modify: `internal/ssh/attach.go:35-150`
- Modify: `internal/ssh/attach_test.go:26-253`
- Modify: `internal/ssh/sshd_integration_test.go`

**Interfaces:**
- Consumes: `arsruntime.ExternalResolverScript()` and the same validated provider/native ID used by local attach.
- Produces: one remote shell branch that attaches a unique external target or continues through the existing ARS create-race path.

- [ ] **Step 1: Write failing generated-script tests**

Add:

```go
func TestRemoteAttachEmbedsSharedExternalResolverBeforeCreate(t *testing.T)
func TestRemoteExternalAttachPreservesClientsAndServerOptions(t *testing.T)
func TestRemoteExternalConflictCannotReachNewSession(t *testing.T)
func TestRemoteVanishedExternalTargetCannotReachNewSession(t *testing.T)
```

Implement the branch-order test directly:

```go
func TestRemoteAttachEmbedsSharedExternalResolverBeforeCreate(t *testing.T) {
	item := remoteAttachedSession()
	command, err := NewAttachCommand(
		context.Background(), item.Host, item, remoteClaudeSpec(),
	)
	if err != nil {
		t.Fatal(err)
	}
	script := command.command.Args[3]
	has := strings.Index(script, "has-session -t '="+remoteRuntimeKey(item)+"'")
	resolve := strings.Index(
		script,
		quotePOSIX(arsruntime.ExternalResolverScript()),
	)
	create := strings.Index(script, "new-session -d")
	if has == -1 || resolve <= has || create <= resolve {
		t.Fatalf("attach order has=%d resolve=%d create=%d:\n%s", has, resolve, create, script)
	}
}
```

For the three behavior tests, add a package-local SSH test fixture with fake
`id`, `ps`, and `tmux` executables, then run `remoteAttachScript` directly under
`/bin/sh`. Record all fake `tmux` calls and assert:

```text
unique match: list-panes, attach-session (no new-session, -d, bind-key, set-option)
conflict:     list-panes (nonzero exit, no later tmux call)
vanished:     list-panes, attach-session failure (no new-session)
```

Assert that:

- `quotePOSIX(arsruntime.ExternalResolverScript())` occurs after the initial
  exact ARS `has-session` check and before `new-session`
- external `tmux -S "$socket" -f /dev/null attach-session -t "$pane"` has no `-d`
- external match exits through `exec` before ARS `bind-key` and `set-option`
- `none` alone reaches the existing create-race branch
- resolver or external attach failure cannot reach native resume
- provider and ID are passed as separately POSIX-quoted words, never interpolated into resolver code

Execute `remoteAttachScript` under `/bin/sh` with fake `tmux`, `ps`, and
provider commands for the conflict and vanished-target cases; string inspection
alone is used only for branch order and quoting.

- [ ] **Step 2: Write the failing ephemeral-SSHD external reuse scenario**

Extend `TestEphemeralSSHDCollectsAndAttaches` with a separately named helper:

```go
func exerciseRemoteExternalTmuxReuse(t *testing.T, server *ephemeralSSHD)
```

The helper must:

- start a fake exact-resume Claude process in `tmux -L external`
- attach a first client and prove `session_attached=1`
- run `ssh.NewAttachCommand` as a second PTY client
- prove `session_attached=2`
- detach the second client with normal external tmux `Ctrl+B`, `d`
- prove the first client and provider PID survive
- prove no ARS-owned runtime was created
- snapshot and compare the external server's keys, status options, and session list

- [ ] **Step 3: Run unit and ephemeral-SSHD tests and verify RED**

Run:

```bash
go test ./internal/ssh -run 'TestRemote(AttachEmbedsSharedExternalResolverBeforeCreate|ExternalAttachPreservesClientsAndServerOptions|ExternalConflictCannotReachNewSession|VanishedExternalTargetCannotReachNewSession)' -count=1
ARS_RUN_SSHD_INTEGRATION=1 go test ./internal/ssh -run TestEphemeralSSHDCollectsAndAttaches -count=1 -v
```

Expected: unit tests fail because `remoteAttachScript` goes directly from ARS
lookup to ARS creation, and the external reuse helper fails before the remote
script reaches the existing pane.

- [ ] **Step 4: Add the external branch without changing ARS launchers**

Build the resolver command from fixed words:

```go
resolver := strings.Join([]string{
	"/bin/sh",
	"-c",
	quotePOSIX(arsruntime.ExternalResolverScript()),
	"ars-external",
	quotePOSIX(string(prov)),
	quotePOSIX(nativeID),
}, " ")
```

Pass `item.NativeID` from `NewAttachCommand` into
`remoteAttachScript(name, cwd, prov, nativeID, spec)`. In the absent-ARS
branch:

```sh
external_result=$(/bin/sh -c '<shared-script>' ars-external '<provider>' '<id>')
case "$external_result" in
  none)
    create_ars_runtime
    ;;
  match	*)
    tab=$(printf '\t')
    kind=
    socket=
    pane=
    extra=
    IFS="$tab" read -r kind socket pane extra <<EOF
$external_result
EOF
    [ "$kind" = match ] || exit 1
    [ -n "$socket" ] && [ -n "$pane" ] && [ -z "$extra" ] || exit 1
    TMUX= TMUX_PANE= exec tmux -S "$socket" -f /dev/null attach-session -t "$pane"
    ;;
  *)
    echo "ars: invalid external tmux result" >&2
    exit 1
    ;;
esac
```

Extract the current detached create plus exact race recheck into the emitted
`create_ars_runtime` shell function. It must run the same `new-session -d`,
capture the same `create_status`, recheck the same exact ARS target, and return
that status when the recheck fails. Keep `set -eu`. Preserve the current Claude
keychain guard, Codex interactive login-shell launcher, ARS `Ctrl+Q`, status
text, and `attach-session -d` byte-for-byte inside the ARS-only path.

- [ ] **Step 5: Run all SSH attach unit tests and verify GREEN**

Run:

```bash
go test ./internal/ssh -run 'Test(RemoteAttach|AttachCommand)' -count=1
```

Expected: new external branching tests and all existing quoting/keychain/login-shell tests pass.

- [ ] **Step 6: Run the remote integration and verify GREEN**

Run:

```bash
ARS_RUN_SSHD_INTEGRATION=1 go test ./internal/ssh -run TestEphemeralSSHDCollectsAndAttaches -count=1 -v
```

Expected: the provider PID and first client survive while the SSH client
attaches and detaches normally.

- [ ] **Step 7: Correct only behavior proven wrong by a new failing test**

Use only the generated POSIX resolver already embedded in the SSH command. Do
not stage the collector, write persistent remote files, change SSH
authentication flags, or add a remote ARS dependency to attach. If integration
exposes a production defect, first add the smallest unit regression, observe
RED, apply the minimal production fix, and rerun both tests.

- [ ] **Step 8: Run SSH verification**

Run:

```bash
go test ./internal/ssh -count=1
go test -race ./internal/ssh
ARS_RUN_SSHD_INTEGRATION=1 go test ./internal/ssh -run TestEphemeralSSHDCollectsAndAttaches -count=1 -v
```

Expected: unit, race, and ephemeral-SSHD tests pass.

- [ ] **Step 9: Commit remote reuse**

```bash
git add internal/ssh/attach.go internal/ssh/attach_test.go internal/ssh/sshd_integration_test.go
git commit -m "feat(ssh): reuse remote external tmux panes"
```

---

### Task 4: Document behavior and run release-level verification

**Files:**
- Modify: `README.md:192-210`
- Verify: all changed Go files and generated collector assets

**Interfaces:**
- Consumes: completed local and remote external reuse behavior.
- Produces: user-facing documentation and fresh verification evidence.

- [ ] **Step 1: Write the README change**

Replace the claim that ARS never adopts outside providers with:

```markdown
Before creating an ARS runtime, selection checks standard per-user tmux
servers for one exact `claude --resume <native-id>` or
`codex resume <native-id>` process. A unique match is attached without
detaching existing clients or changing that tmux server. Detach from an
external runtime with its normal tmux binding, usually `prefix d`.

Bare `claude` or `codex` processes, arbitrary `tmux -S` sockets, and ambiguous
matches are not adopted. External runtimes remain under `Recent` and do not
gain ARS preview, send, or kill behavior.
```

Keep the existing explanation of ARS-owned `Ctrl+Q`, client handoff, process exit, reboot, and native history.

- [ ] **Step 2: Run formatting and diff checks**

Run:

```bash
gofmt -w internal/runtime/external.go internal/runtime/external_test.go internal/runtime/attach.go internal/runtime/attach_test.go internal/runtime/tmux_integration_test.go internal/ssh/attach.go internal/ssh/attach_test.go internal/ssh/sshd_integration_test.go
git diff --check
```

Expected: no output from `git diff --check`.

- [ ] **Step 3: Refresh embedded collector assets**

Run:

```bash
go run ./cmd/ars-build --assets-only
git status --short
```

Expected: assets build successfully. Generated collector binaries should remain unchanged because the collector and private protocol were not modified; if they change, inspect and explain the cause before proceeding.

- [ ] **Step 4: Run the full automated gate**

Run:

```bash
go test ./...
go test -race ./...
go vet ./...
npm test
```

Expected: every command exits 0 with no race, vet, or npm test failures.

- [ ] **Step 5: Build a production binary**

Run:

```bash
ars_external_tmp=$(mktemp -d)
go build -o "$ars_external_tmp/ars" ./cmd/ars
"$ars_external_tmp/ars" list --json >/dev/null
```

Expected: build and headless startup both exit 0.

- [ ] **Step 6: Perform live disposable-tmux acceptance**

Using a temporary `TMUX_TMPDIR`, fake provider executable, and fixture native history:

1. Start an external tmux server with one exact-resume provider.
2. Attach one client.
3. Start the fresh production `ars` binary in a separate fixed-size tmux client.
4. Select the fixture session and confirm the provider PID remains unchanged.
5. Confirm the external tmux reports two clients.
6. Detach the ARS-driven client with `Ctrl+B`, `d`.
7. Confirm the original client remains, ARS returns to the same TUI, and no `ars-v1` runtime was created.
8. Compare the external server's pre/post `list-keys`, `show-options -g`, and `list-sessions` output.

Stop and report the exact failing gate if the platform cannot create the disposable client or fixed fixture; do not substitute a unit-test claim for this live gate.

- [ ] **Step 7: Commit documentation and any test-only acceptance fixture**

```bash
git add README.md
git commit -m "docs: explain external tmux reuse"
```

- [ ] **Step 8: Record final evidence**

Capture:

```bash
git status --short --branch
git log --oneline --decorate -5
```

Expected: clean `feat/reuse-session` worktree with the design, resolver, local attach, remote attach, tests, and README commits present.
