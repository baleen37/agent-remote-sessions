# Agent Remote Sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build one local `ars` binary that searches Claude Code and Codex sessions across configured SSH hosts in an `fzf` TUI and natively resumes the selected session without installing anything remotely.

**Architecture:** `ars` sends an embedded read-only shell probe to each host through the system SSH client, parses six-field NUL-framed metadata locally, and merges healthy host results. The system `fzf` client selects an opaque row index, then `ars` starts a fixed provider resume command through `ssh -tt` using the selected host, CWD, and native session ID.

**Tech Stack:** Go 1.26, Go standard library only, system OpenSSH, POSIX shell utilities, local `fzf`.

## Global Constraints

- Module and repository: `github.com/baleen37/agent-remote-sessions`.
- Produce one local binary named `ars`; do not create `arsd`.
- Remote hosts receive the probe through stdin and retain no binary, script, daemon, service, cache, socket, or configuration.
- V1 providers are exactly Claude Code and Codex; V1 relay is exactly SSH.
- Collect only provider, source path, mtime, native ID, CWD, and explicit native title/name. Never transfer a complete session JSONL line.
- V1 action is native `resume`; do not implement tmux, attach, start, stop, delete, fork, prompt sending, worktrees, tasks, queues, or conductor behavior.
- Invoke system `ssh` and `fzf`; do not add a Go SSH client or TUI dependency.
- Isolate host failures and continue when at least one host returns sessions.
- Use strict RED-GREEN-REFACTOR for every production behavior.

---

## File map

```text
go.mod                              module declaration; no third-party modules
cmd/ars/main.go                     process wiring and exit status
internal/model/model.go             Host and normalized Session values
internal/hosts/hosts.go             ~/.config/ars/hosts parser and selection
internal/hosts/hosts_test.go        inventory behavior
internal/probe/script.go            embedded read-only remote shell probe
internal/probe/decode.go            six-field NUL frame decoder
internal/probe/probe_test.go        probe integration and decoder tests
internal/provider/provider.go       record normalization and provider allowlist
internal/provider/provider_test.go  Claude/Codex normalization tests
internal/command/runner.go          injectable os/exec boundary
internal/relay/relay.go             Relay interface
internal/relay/ssh.go               collection and shell-safe native resume
internal/relay/ssh_test.go          SSH argv/stdin/resume tests
internal/aggregate/aggregate.go     bounded fan-out, diagnostics, sorting
internal/aggregate/aggregate_test.go partial failure and concurrency tests
internal/picker/fzf.go              row rendering, selection, cancellation
internal/picker/fzf_test.go         opaque-index mapping tests
internal/app/app.go                 CLI parsing and end-to-end orchestration
internal/app/app_test.go            list/TUI/resume flow with fakes
README.md                           install, inventory, usage, security boundary
.github/workflows/ci.yml            test, race, vet, macOS/Linux builds
```

### Task 1: Bootstrap model and host inventory

**Files:**
- Create: `go.mod`
- Create: `internal/model/model.go`
- Create: `internal/hosts/hosts.go`
- Create: `internal/hosts/hosts_test.go`

**Interfaces:**
- Produces: `model.Host{Target string}` and `model.Session{Host, Provider, NativeID, UpdatedAt, CWD, Project, Title}`.
- Produces: `hosts.Load(path string) ([]model.Host, error)` and `hosts.Select([]model.Host, string) ([]model.Host, error)`.
- Consumes: no earlier interfaces.

- [ ] **Step 1: Write failing inventory tests**

Create tests that write this exact inventory and assert comments/whitespace are ignored while order is preserved:

```go
func TestLoad(t *testing.T) {
    path := filepath.Join(t.TempDir(), "hosts")
    err := os.WriteFile(path, []byte("# managed\n devbox \nuser@agent-mac\n"), 0o600)
    if err != nil { t.Fatal(err) }
    got, err := Load(path)
    if err != nil { t.Fatal(err) }
    want := []model.Host{{Target: "devbox"}, {Target: "user@agent-mac"}}
    if !reflect.DeepEqual(got, want) { t.Fatalf("got %#v want %#v", got, want) }
}

func TestLoadRejectsDuplicateAndEmptyInventory(t *testing.T) {
    for _, body := range []string{"# none\n", "devbox\ndevbox\n"} {
        path := filepath.Join(t.TempDir(), "hosts")
        if err := os.WriteFile(path, []byte(body), 0o600); err != nil { t.Fatal(err) }
        if _, err := Load(path); err == nil { t.Fatalf("Load(%q) succeeded", body) }
    }
}

func TestSelect(t *testing.T) {
    all := []model.Host{{Target: "devbox"}, {Target: "user@agent-mac"}}
    got, err := Select(all, "devbox")
    if err != nil { t.Fatal(err) }
    if !reflect.DeepEqual(got, all[:1]) { t.Fatalf("got %#v", got) }
    if _, err := Select(all, "missing"); err == nil { t.Fatal("missing host accepted") }
}
```

- [ ] **Step 2: Run `go test ./internal/hosts` and verify RED**

Expected: build failure because `Load`, `Select`, and model types do not exist.

- [ ] **Step 3: Implement the minimum model and parser**

`go.mod`:

```go
module github.com/baleen37/agent-remote-sessions

go 1.26.0
```

`hosts.Load` must scan line-by-line, trim whitespace, skip empty/comment lines, reject duplicate targets with the line number, and include the expected path in read/empty errors. `hosts.Select` returns all hosts for an empty name and exactly one host for an exact target match.

- [ ] **Step 4: Run `gofmt -w internal && go test ./internal/hosts` and verify GREEN**

Expected: `ok .../internal/hosts`.

- [ ] **Step 5: Commit**

```bash
git add go.mod internal/model internal/hosts
git commit -m "feat: add host inventory"
```

### Task 2: Add the one-shot probe and provider normalization

**Files:**
- Create: `internal/probe/script.go`
- Create: `internal/probe/decode.go`
- Create: `internal/probe/probe_test.go`
- Create: `internal/provider/provider.go`
- Create: `internal/provider/provider_test.go`

**Interfaces:**
- Consumes: `model.Session` from Task 1.
- Produces: `probe.Script string`, `probe.Record`, and `probe.Decode([]byte) ([]probe.Record, error)`.
- Produces: `provider.Normalize(host model.Host, record probe.Record) (model.Session, error)`.

- [ ] **Step 1: Write failing NUL-frame tests**

Use a complete six-field record and a truncated record:

```go
func TestDecode(t *testing.T) {
    raw := []byte("claude\x00/home/me/a.jsonl\x001721234567\x00id-1\x00/work/app\x00Fix login\x00")
    got, err := Decode(raw)
    if err != nil { t.Fatal(err) }
    want := []Record{{Provider: "claude", Path: "/home/me/a.jsonl", MTime: "1721234567", NativeID: "id-1", CWD: "/work/app", Title: "Fix login"}}
    if !reflect.DeepEqual(got, want) { t.Fatalf("got %#v want %#v", got, want) }
}

func TestDecodeRejectsTruncatedFrame(t *testing.T) {
    if _, err := Decode([]byte("claude\x00path\x001\x00id\x00cwd\x00")); err == nil {
        t.Fatal("truncated frame accepted")
    }
}
```

- [ ] **Step 2: Run `go test ./internal/probe` and verify RED**

Expected: build failure because `Record` and `Decode` do not exist.

- [ ] **Step 3: Implement strict six-field decoding**

`Decode` splits on NUL, requires a trailing NUL and a field count divisible by
six, JSON-unescapes the native ID/CWD/title scalars with `strconv.Unquote`,
returns records in input order, and never guesses missing fields.

- [ ] **Step 4: Write failing local probe integration test**

Create temporary Claude and Codex trees under a temporary `HOME`. Fixtures must include sensitive marker strings outside the scalar fields and JSON-escaped quotes in the title/CWD. Execute `sh -s` with `probe.Script` on stdin, decode stdout, and assert:

```go
if bytes.Contains(stdout.Bytes(), []byte("SECRET_BODY_MARKER")) {
    t.Fatal("probe leaked a complete JSONL body")
}
if len(records) != 2 { t.Fatalf("records=%d", len(records)) }
```

Also run `sh -n` against `Script` and require success. The integration test must assert that removing both provider directories still exits zero with empty stdout.

- [ ] **Step 5: Run `go test ./internal/probe` and verify RED**

Expected: failure because `Script` is empty or missing.

- [ ] **Step 6: Implement the embedded POSIX shell probe**

The script must:

```text
1. use find for ~/.claude/projects and ~/.codex/sessions
2. use an awk JSON-string extractor that respects backslash escapes
3. extract Claude sessionId, cwd, and last aiTitle or agentName
4. extract Codex session_meta payload id and cwd; title is empty
5. use stat -f %m first and stat -c %Y as fallback
6. printf exactly provider/path/mtime/id/cwd/title plus NUL after every field
```

It must not print source lines, shell traces, or diagnostics to stdout. Missing roots are normal and produce no record.

- [ ] **Step 7: Write failing provider normalization tests**

Cover both allowlisted providers, Unix-seconds conversion, `path.Base(CWD)` project derivation, missing ID/CWD rejection, and unknown provider rejection:

```go
func TestNormalizeClaude(t *testing.T) {
    rec := probe.Record{Provider: "claude", MTime: "1721234567", NativeID: "id-1", CWD: "/work/app", Title: "Fix login"}
    got, err := Normalize(model.Host{Target: "devbox"}, rec)
    if err != nil { t.Fatal(err) }
    if got.Host != "devbox" || got.Project != "app" || got.Provider != "claude" { t.Fatalf("got %#v", got) }
}
```

- [ ] **Step 8: Run `go test ./internal/provider` and verify RED**

Expected: build failure because `Normalize` does not exist.

- [ ] **Step 9: Implement normalization and verify GREEN**

Run:

```bash
gofmt -w internal/probe internal/provider
go test ./internal/probe ./internal/provider
```

Expected: both packages pass and the sensitive marker never appears in probe output.

- [ ] **Step 10: Commit**

```bash
git add internal/probe internal/provider
git commit -m "feat: discover native agent sessions"
```

### Task 3: Implement system-command and SSH relay boundaries

**Files:**
- Create: `internal/command/runner.go`
- Create: `internal/relay/relay.go`
- Create: `internal/relay/ssh.go`
- Create: `internal/relay/ssh_test.go`

**Interfaces:**
- Consumes: `model.Host`, `model.Session`, `probe.Script`, `probe.Decode`, and `provider.Normalize`.
- Produces: `command.Runner.Run(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error`.
- Produces: `relay.Relay` and `relay.SSH{Runner command.Runner}`.

- [ ] **Step 1: Write failing SSH collection tests**

Use a recording fake runner. Assert the call is exactly:

```go
wantName := "ssh"
wantArgs := []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "devbox", "sh", "-s"}
```

The fake writes two encoded probe records to stdout and a warning to stderr. Assert `Collect` returns two normalized sessions with `Host == "devbox"`; assert a nonzero runner error contains the host and captured stderr.

- [ ] **Step 2: Run `go test ./internal/relay -run Collect` and verify RED**

Expected: build failure because `SSH.Collect` does not exist.

- [ ] **Step 3: Implement the runner and collection path**

The production runner uses `exec.CommandContext`, assigns the supplied streams, and returns `cmd.Run()`. `SSH.Collect` sends only `probe.Script` as stdin, captures stdout/stderr separately, decodes complete frames, skips individually invalid normalized records while counting them, and returns an error only for command or frame failure.

- [ ] **Step 4: Write failing resume safety tests**

Table-test Claude, Codex, unknown provider, and hostile values such as:

```go
session := model.Session{
    Host: "devbox", Provider: "claude", NativeID: "id'; touch /tmp/pwned; '",
    CWD: "/work/it's here",
}
```

Assert the runner receives `ssh`, `[]string{"-tt", "devbox", remoteCommand}`, the process stdin/stdout/stderr, and a remote command whose single-quote escaping is:

```text
cd -- '/work/it'"'"'s here' && exec claude --resume 'id'"'"'; touch /tmp/pwned; '"'"''
```

Unknown providers must fail before invoking the runner. Codex must use `exec codex resume`, never `claude --resume`.

- [ ] **Step 5: Run `go test ./internal/relay -run Resume` and verify RED**

Expected: failure because `SSH.Resume` and shell quoting do not exist.

- [ ] **Step 6: Implement fixed resume commands and verify GREEN**

Use one unexported `shellQuote(string) string` function that wraps values in single quotes and replaces each single quote with `'"'"'`. Select the executable and verb with a `switch` over exactly `claude` and `codex`; do not concatenate the provider into an executable name.

Run:

```bash
gofmt -w internal/command internal/relay
go test ./internal/relay
```

Expected: collection and hostile-value resume tests pass.

- [ ] **Step 7: Commit**

```bash
git add internal/command internal/relay
git commit -m "feat: collect and resume sessions over ssh"
```

### Task 4: Add bounded multi-host aggregation

**Files:**
- Create: `internal/aggregate/aggregate.go`
- Create: `internal/aggregate/aggregate_test.go`

**Interfaces:**
- Consumes: `relay.Relay`, `model.Host`, and `model.Session`.
- Produces: `aggregate.Collect(context.Context, relay.Relay, []model.Host, int) ([]model.Session, []aggregate.HostError, error)`.

- [ ] **Step 1: Write failing aggregation tests**

Use a blocking fake relay with atomic active/max counters. With six hosts and limit two, assert `maxActive <= 2`. Return sessions in deliberately mixed timestamps and assert newest-first ordering. Make one host fail and assert healthy sessions plus one `HostError` remain. Make every host fail and assert the final error is non-nil.

- [ ] **Step 2: Run `go test ./internal/aggregate` and verify RED**

Expected: build failure because `Collect` does not exist.

- [ ] **Step 3: Implement the minimum worker pool**

Reject `limit < 1`. Start `min(limit, len(hosts))` workers, send hosts through one channel, collect one result per host, and sort successful sessions with `sort.SliceStable` by `UpdatedAt` descending. Do not retry or cache. Return host errors in inventory order for deterministic diagnostics.

- [ ] **Step 4: Verify GREEN and race safety**

```bash
gofmt -w internal/aggregate
go test ./internal/aggregate
go test -race ./internal/aggregate
```

Expected: all tests pass with no race report.

- [ ] **Step 5: Commit**

```bash
git add internal/aggregate
git commit -m "feat: aggregate sessions across hosts"
```

### Task 5: Add the searchable `fzf` picker

**Files:**
- Create: `internal/picker/fzf.go`
- Create: `internal/picker/fzf_test.go`

**Interfaces:**
- Consumes: `command.Runner` and `[]model.Session`.
- Produces: `picker.FZF.Select(context.Context, []model.Session) (*model.Session, error)`; nil selection means cancellation.

- [ ] **Step 1: Write failing opaque-index tests**

Use titles and paths containing tabs/control characters. Assert candidates are sanitized for display, begin with `0\t` and `1\t`, and use arguments:

```go
[]string{"--delimiter=\t", "--with-nth=2..", "--no-multi", "--header=host  updated  provider  project  title  id"}
```

Make the fake return `1\tmodified display text\n` and assert the second original structured session is selected. Return an exit error with code 130 and assert `(nil, nil)`. Return index 99 and assert an error.

- [ ] **Step 2: Run `go test ./internal/picker` and verify RED**

Expected: build failure because `FZF.Select` does not exist.

- [ ] **Step 3: Implement rendering and selection**

Render one line per session with opaque decimal index, host, local-time `2006-01-02 15:04`, provider, project/CWD, title or `-`, and at most the first 12 ID characters. Replace tabs/newlines/carriage returns and other control runes in display values with spaces. Parse only the first output field as an integer and index the original slice.

- [ ] **Step 4: Verify GREEN**

```bash
gofmt -w internal/picker
go test ./internal/picker
```

Expected: selection, cancellation, sanitization, and invalid-index tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/picker
git commit -m "feat: add searchable session picker"
```

### Task 6: Wire `ars`, JSON listing, and resume

**Files:**
- Create: `internal/app/app.go`
- Create: `internal/app/app_test.go`
- Create: `cmd/ars/main.go`

**Interfaces:**
- Consumes: host inventory, aggregator, `relay.Relay`, picker, and standard streams.
- Produces: `app.Run(context.Context, []string, app.Dependencies) error` and the `ars` executable.

- [ ] **Step 1: Write failing CLI flow tests**

Dependency-inject `LoadHosts`, `Relay`, `Picker`, stdout, and stderr. Cover:

```text
ars                    loads all hosts, aggregates, selects, resumes once
ars devbox             selects only devbox before collection
ars list --json        emits sorted JSON and never calls picker/resume
ars unknown            fails before collection
ars list --json extra  prints usage error
picker cancellation    exits without resume
partial host failure   prints warning and still opens picker
zero sessions          fails without opening picker
```

Decode JSON output into `[]model.Session` rather than comparing indentation.

- [ ] **Step 2: Run `go test ./internal/app` and verify RED**

Expected: build failure because `Run` and dependencies do not exist.

- [ ] **Step 3: Implement exact CLI grammar and orchestration**

Use `~/.config/ars/hosts`, overridden only by `ARS_HOSTS_FILE` for tests and automation. Accept only zero args, one host arg, or exactly `list --json`. Use aggregation concurrency four. Print one concise `warning: <host>: <error>` per failed host before TUI launch. Encode JSON to stdout with `json.Encoder.SetIndent("", "  ")`.

Before interactive execution, `cmd/ars/main.go` verifies `exec.LookPath("ssh")` and `exec.LookPath("fzf")`; `list --json` requires only SSH. It builds the OS runner, SSH relay, and FZF picker, calls `app.Run`, prints one error to stderr, and maps cancellation to exit zero.

- [ ] **Step 4: Verify unit and command behavior GREEN**

```bash
gofmt -w cmd internal/app
go test ./...
go run ./cmd/ars --bad
```

Expected: tests pass; invalid CLI prints usage and exits nonzero without SSH.

- [ ] **Step 5: Commit**

```bash
git add cmd/ars internal/app
git commit -m "feat: add ars command"
```

### Task 7: Document, cross-build, and verify the finished V1

**Files:**
- Create: `README.md`
- Create: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: completed `ars` command.
- Produces: installation/usage documentation and reproducible CI checks.

- [ ] **Step 1: Write README acceptance documentation**

Document Go install/build, the exact `~/.config/ars/hosts` format, `ars`, `ars <host>`, and `ars list --json`. State local prerequisites (`ssh`, `fzf`), remote prerequisites (SSH, POSIX shell utilities, provider CLI/history), collected scalar fields, and the guarantee that no remote state is installed or retained.

- [ ] **Step 2: Add CI with exact checks**

The workflow runs on pull requests and pushes to `main` with Go 1.26 and these commands:

```bash
go test ./...
go test -race ./...
go vet ./...
GOOS=darwin GOARCH=arm64 go build ./cmd/ars
GOOS=linux GOARCH=amd64 go build ./cmd/ars
```

- [ ] **Step 3: Run final automated verification**

```bash
gofmt -w cmd internal
go test ./...
go test -race ./...
go vet ./...
GOOS=darwin GOARCH=arm64 go build -o /tmp/ars-darwin-arm64 ./cmd/ars
GOOS=linux GOARCH=amd64 go build -o /tmp/ars-linux-amd64 ./cmd/ars
git diff --check
```

Expected: every command exits zero and both `/tmp/ars-*` files are nonempty.

- [ ] **Step 4: Run manual live acceptance**

With at least two configured SSH aliases containing real histories:

```bash
go run ./cmd/ars list --json
go run ./cmd/ars
```

Verify both providers/hosts appear, search filters rows, Escape does not resume, Claude and Codex Enter selections resume the exact native ID in the recorded CWD, one unreachable host does not hide healthy results, and no remote `ars` artifact exists after the run.

- [ ] **Step 5: Commit**

```bash
git add README.md .github/workflows/ci.yml
git commit -m "docs: document agent remote sessions"
```
