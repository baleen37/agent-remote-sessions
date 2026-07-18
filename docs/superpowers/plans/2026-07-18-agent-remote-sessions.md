# Agent Remote Sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a small ars CLI that discovers Claude Code and Codex sessions on configured SSH hosts, returns stable JSON, offers fzf selection, and resumes the selected native session.

**Architecture:** One Go module builds the local ars command and three embedded, one-shot collector binaries. Provider adapters normalize local metadata on each remote host. A bounded private protocol returns only validated session metadata. Collection and resume use separate SSH paths.

**Tech Stack:** Go standard library, system OpenSSH, system fzf, GitHub Actions.

## Global Constraints

- Keep one module and one user-facing binary. Do not add a daemon, cache, index, plugin system, remote installer, or background cleanup process.
- Compile in exactly two providers: Claude Code and Codex.
- Support exactly darwin/arm64, linux/amd64, and linux/arm64 collectors.
- Never transfer prompts, responses, tool output, credentials, raw transcript lines, or provider source paths.
- Add an interface only when this plan names at least two implementations or a test double is required.
- Treat inventory values, collector output, session metadata, and fzf output as untrusted.
- Keep collection non-interactive and bounded. Keep resume interactive and compatible with the user's normal SSH authentication and forwarding.
- Use test-first RED, GREEN, REFACTOR steps. Commit after every task passes its focused tests.

---

## File Map

~~~text
go.mod
cmd/
  ars/main.go
  ars-build/main.go
  ars-build/main_test.go
  ars-collector/main.go
  ars-collector/main_test.go
internal/
  app/app.go
  app/app_test.go
  app/aggregate.go
  app/aggregate_test.go
  app/e2e_test.go
  app/inventory.go
  app/inventory_test.go
  output/fzf.go
  output/fzf_test.go
  output/json.go
  output/json_test.go
  protocol/protocol.go
  protocol/protocol_test.go
  protocol/fuzz_test.go
  provider/provider.go
  provider/provider_test.go
  provider/claude.go
  provider/claude_test.go
  provider/codex.go
  provider/codex_test.go
  provider/testdata/claude/*
  provider/testdata/codex/*
  session/session.go
  session/session_test.go
  ssh/runner.go
  ssh/assets.go
  ssh/generated/.keep
  ssh/collect.go
  ssh/collect_test.go
  ssh/resume.go
  ssh/resume_test.go
  ssh/sshd_integration_test.go
.github/workflows/ci.yml
.gitignore
README.md
~~~

## Task 1: Establish the session model and inventory boundary

**Files:**

- Create: go.mod
- Create: internal/session/session.go
- Create: internal/session/session_test.go
- Create: internal/app/inventory.go
- Create: internal/app/inventory_test.go

**Interfaces:**

~~~go
type Provider string

const (
    Claude Provider = "claude"
    Codex  Provider = "codex"
)

type Candidate struct {
    Provider  Provider
    NativeID  string
    UpdatedAt time.Time
    CWD       string
    Title     string
}

type Session struct {
    Host string
    Candidate
}

func ValidateCandidate(Candidate) error
func Bind(host string, Candidate) (Session, error)
func Project(cwd string) string

type Host struct {
    Target string
}

func ConfigPath() (string, error)
func Load(path string) ([]Host, error)
func Select(hosts []Host, target string) ([]Host, error)
~~~

Candidate and Session belong to internal/session. Host and the inventory
functions belong to internal/app.

- [ ] **Step 1: Write failing session validation tests**

Cover registered providers, canonical UUID native IDs, non-zero timestamps, absolute Unix CWD values, control characters, bounded UTF-8 title fields, host binding, and CWD basename project derivation.

- [ ] **Step 2: Implement the minimum session model**

Use one shared UUID validator. Limit Host, NativeID, CWD, and Title by explicit constants. Validate before constructing Session. Project returns the final cleaned CWD component and never becomes part of identity.

- [ ] **Step 3: Write failing inventory tests**

Cover XDG and home fallback paths, blank lines and comments, preserved order, duplicates, targets beginning with a dash, whitespace, control characters, an unknown host selector, and passing each target as one value.

- [ ] **Step 4: Implement inventory parsing and selection**

The default path is $XDG_CONFIG_HOME/ars/hosts when XDG_CONFIG_HOME is set, otherwise ~/.config/ars/hosts. Reject the entire file on the first invalid or duplicate target.

- [ ] **Step 5: Verify and commit**

Run:

~~~sh
go test ./internal/session ./internal/app
~~~

Commit:

~~~sh
git add go.mod internal/session internal/app/inventory.go internal/app/inventory_test.go
git commit -m "feat: add session model and host inventory"
~~~

## Task 2: Add compile-time provider adapters

**Files:**

- Create: internal/provider/provider.go
- Create: internal/provider/provider_test.go
- Create: internal/provider/claude.go
- Create: internal/provider/claude_test.go
- Create: internal/provider/codex.go
- Create: internal/provider/codex_test.go
- Create: internal/provider/testdata/claude/*
- Create: internal/provider/testdata/codex/*

**Interfaces:**

~~~go
type Status string

const (
    Absent  Status = "absent"
    OK      Status = "ok"
    Partial Status = "partial"
    Error   Status = "error"
)

type Result struct {
    Provider  session.Provider
    Sessions  []session.Candidate
    Status    Status
    Seen      int
    Skipped   int
    ErrorCode string
}

type ResumeSpec struct {
    Executable string
    Args       []string
}

type Adapter interface {
    Name() session.Provider
    Discover(context.Context, string) Result
    ValidateID(string) error
    Resume(string) (ResumeSpec, error)
}

func Builtin() []Adapter
func Lookup(session.Provider) (Adapter, bool)
~~~

ErrorCode is empty for OK and absent, otherwise one of unavailable,
incompatible, corrupt, or resource_limit.

- [ ] **Step 1: Create sanitized fixture trees**

Represent only schema shapes needed by the adapters. Use synthetic UUIDs, CWDs, titles, timestamps, and content. Include malformed, internal, sidechain, exec, subagent, and unknown-source records. Do not copy real prompts or paths.

- [ ] **Step 2: Write failing provider registry tests**

Assert Builtin returns Claude then Codex, names are unique, Lookup rejects unknown values, every adapter accepts only canonical UUIDs, and resume specs are fixed to claude --resume UUID or codex resume UUID.

- [ ] **Step 3: Write failing Claude discovery tests**

Using a temporary home, cover direct ~/.claude/projects/project/*.jsonl files only, internal and sidechain exclusion, latest valid CWD selection, explicit native title precedence, mtime UpdatedAt, absent executable, malformed lines, partial status, and no prompt-derived title.

- [ ] **Step 4: Implement streaming Claude discovery**

Read one JSONL line at a time with a 1 MiB scanner limit. Decode only fields required for ID, CWD, native title, and exclusion. Never retain or return raw lines or file names. Return absent when Claude metadata or executable is missing; use partial when valid sessions coexist with skipped corrupt records.

- [ ] **Step 5: Write failing Codex discovery tests**

Cover recursive session_meta records under ~/.codex/sessions, thread_source=user, source=cli or vscode, exclusion of exec, subagent, and unknown sources, mtime UpdatedAt, empty Title, corrupt records, and absent executable.

- [ ] **Step 6: Implement streaming Codex discovery**

Decode only session_meta fields. Do not populate preview text. Keep the same status and error-code rules as Claude.

- [ ] **Step 7: Verify and commit**

Run:

~~~sh
go test ./internal/provider
~~~

Commit:

~~~sh
git add internal/provider
git commit -m "feat: discover Claude and Codex sessions"
~~~

## Task 3: Define and implement the bounded collector protocol

**Files:**

- Create: internal/protocol/protocol.go
- Create: internal/protocol/protocol_test.go
- Create: internal/protocol/fuzz_test.go
- Create: cmd/ars-collector/main.go
- Create: cmd/ars-collector/main_test.go

**Interfaces:**

~~~go
type Limits struct {
    StartupBytes int64
    LineBytes    int
    TotalBytes   int64
    Sessions     int
}

func DefaultLimits() Limits
func Encode(io.Writer, string, []session.Candidate, []provider.Result) error
func Decode(io.Reader, string, Limits) ([]session.Candidate, []provider.Result, error)
~~~

**Wire contract:**

~~~text
ARS/1 BEGIN <nonce>
{"type":"session", ...normalized fields...}
{"type":"summary", ...provider status and counts...}
ARS/1 END <nonce> <session-count>
~~~

- [ ] **Step 1: Write failing encoder and decoder tests**

Cover a valid round trip, wrong or missing nonce, unknown major version, unknown frame type, invalid UTF-8, overlong line, startup garbage above 64 KiB, total output above 16 MiB, more than 10,000 sessions, truncated END, mismatched count, and invalid Candidate data.

- [ ] **Step 2: Implement fail-closed decoding**

Use 64 KiB startup, 64 KiB line, 16 MiB total, and 10,000-session defaults. Buffer decoded sessions privately until END validates. Reject any unknown version or frame. Require the caller to separately confirm successful SSH and collector exits.

- [ ] **Step 3: Add fuzz coverage**

Seed the valid transcript and each malformed boundary case. The fuzz property is no panic, no unbounded allocation, and no returned sessions unless the entire transcript validates.

- [ ] **Step 4: Write failing collector command tests**

Extract command execution into a testable run function. Cover required hexadecimal nonce, deterministic session ordering, provider summaries, partial provider failure, and non-zero exit when encoding fails.

- [ ] **Step 5: Implement ars-collector**

Run both built-in adapters against the remote home, validate and sort Candidates, emit one ARS/1 transcript, and write diagnostics only to stderr. Do not expose provider file paths.

- [ ] **Step 6: Verify and commit**

Run:

~~~sh
go test ./internal/protocol ./cmd/ars-collector
go test -fuzz=FuzzDecode -fuzztime=10s ./internal/protocol
~~~

Commit:

~~~sh
git add internal/protocol cmd/ars-collector
git commit -m "feat: add bounded collector protocol"
~~~

## Task 4: Embed collectors and implement bounded SSH collection

**Files:**

- Create: internal/ssh/assets.go
- Create: internal/ssh/generated/.keep
- Create: cmd/ars-build/main.go
- Create: cmd/ars-build/main_test.go
- Create: internal/ssh/runner.go
- Create: internal/ssh/collect.go
- Create: internal/ssh/collect_test.go
- Modify: .gitignore

**Interfaces:**

~~~go
type Runner interface {
    Run(
        context.Context,
        string,
        []string,
        io.Reader,
        io.Writer,
        io.Writer,
    ) error
}

type CollectorAssets interface {
    ForTarget(goos, goarch string) ([]byte, error)
}

type CollectOptions struct {
    ConnectTimeout time.Duration
    HostTimeout    time.Duration
    ProtocolLimits protocol.Limits
}

func Collect(
    context.Context,
    Runner,
    CollectorAssets,
    string,
    CollectOptions,
) ([]session.Candidate, []provider.Result, error)
~~~

- [ ] **Step 1: Write failing target and SSH argv tests**

Map Darwin arm64, Linux x86_64 or amd64, and Linux aarch64 or arm64 to the three supported collectors. Reject every other pair. Assert collection uses batch mode, no agent or port forwarding, one connection attempt, host-key verification, a five-second connect timeout, and the host target as exactly one argv element.

- [ ] **Step 2: Write failing remote lifecycle tests with a fake Runner**

Model the uname probe and collector invocation. Assert a random 128-bit hexadecimal nonce, a nonce-specific directory below TMPDIR or /tmp, umask 077, immediate EXIT/HUP/INT/TERM traps, executable upload by stdin, the nonce passed to the collector, exact path cleanup, and no glob or recursive deletion.

- [ ] **Step 3: Write failing failure-path tests**

Cover probe failure, unsupported target, upload failure, collector timeout at 60 seconds, stdout above 16 MiB, bounded stderr, protocol failure, non-zero remote exit, context cancellation, and a secondary five-second exact cleanup attempt after an interrupted primary run.

- [ ] **Step 4: Implement the system SSH Runner**

Use exec.CommandContext without a shell locally. Pass inventory targets and every SSH option as separate argv values. Capture bounded stdout and stderr. Collection must remain non-interactive.

- [ ] **Step 5: Implement the two-stage collection flow**

First probe uname -s and uname -m. Then choose and stream the embedded collector. Validate the remote temp path echoed by the bootstrap before using it. Quote only the fixed bootstrap values and validated nonce. Document that SIGKILL can leave one nonce directory; do not add a janitor.

- [ ] **Step 6: Write failing build-tool tests**

Assert ars-build compiles ars-collector with CGO_ENABLED=0 for the exact three targets, writes deterministic assets into internal/ssh/generated, and fails if any target is absent. The default mode then builds the local ars; --assets-only stops after asset generation.

- [ ] **Step 7: Implement asset generation and embedding**

Keep generated collector blobs ignored except for .keep. ssh/assets.go embeds the exact generated names and exposes only the supported target lookup.

- [ ] **Step 8: Verify and commit**

Run:

~~~sh
go test ./internal/ssh ./cmd/ars-build
go run ./cmd/ars-build --assets-only
go test ./internal/ssh
~~~

Commit:

~~~sh
git add .gitignore cmd/ars-build internal/ssh
git commit -m "feat: collect sessions through bounded SSH"
~~~

## Task 5: Aggregate hosts and publish JSON schema version 1

**Files:**

- Create: internal/app/aggregate.go
- Create: internal/app/aggregate_test.go
- Create: internal/output/json.go
- Create: internal/output/json_test.go

**Interfaces:**

~~~go
type HostStatus string

const (
    HostOK    HostStatus = "ok"
    HostError HostStatus = "error"
)

type HostResult struct {
    Target string
    Status HostStatus
}

type HostError struct {
    Host    string
    Code    string
    Message string
}

type Result struct {
    Hosts    []output.HostResult
    Sessions []session.Session
    Errors   []output.HostError
}

type Collector func(context.Context, string) (
    []session.Candidate,
    []provider.Result,
    error,
)

func CollectHosts(context.Context, []Host, int, Collector) Result
func WriteJSON(io.Writer, []HostResult, []session.Session, []HostError) error
~~~

HostStatus, HostResult, HostError, and WriteJSON belong to internal/output.
Result, Collector, and CollectHosts belong to internal/app.

- [ ] **Step 1: Write failing aggregation tests**

Cover a worker limit of four, one attempt per host, per-host timeout propagation, healthy empty hosts, partial provider results, failed hosts beside healthy sessions, all-host failure, deduplication by host plus provider plus native ID, and deterministic sorting by UpdatedAt descending with stable tie breakers.

- [ ] **Step 2: Implement aggregation**

Bind validated Candidates to their inventory host only after a successful protocol result. Record every configured host exactly once. Keep healthy data when peers fail. Sanitize error messages and assign stable machine codes such as ssh_timeout, ssh_failed, unsupported_target, protocol_error, and resource_limit.

- [ ] **Step 3: Write failing JSON contract tests**

Golden-test schema_version 1 with hosts, sessions, and errors. Assert a healthy empty host is distinguishable from an unreachable host, timestamps use RFC3339Nano, unknown internal fields never leak, HTML escaping does not alter terminal text, and a trailing newline is present.

- [ ] **Step 4: Implement public JSON DTOs**

Define dedicated output structs with explicit JSON field names. Do not serialize internal structs directly. The command succeeds when at least one host is healthy, including healthy empty results; it fails when all selected hosts fail.

- [ ] **Step 5: Verify and commit**

Run:

~~~sh
go test ./internal/app ./internal/output
go test -race ./internal/app
~~~

Commit:

~~~sh
git add internal/app/aggregate.go internal/app/aggregate_test.go internal/output
git commit -m "feat: aggregate hosts and emit stable JSON"
~~~

## Task 6: Add fzf selection, native resume, and CLI orchestration

**Files:**

- Create: internal/output/fzf.go
- Create: internal/output/fzf_test.go
- Create: internal/ssh/resume.go
- Create: internal/ssh/resume_test.go
- Create: internal/app/app.go
- Create: internal/app/app_test.go
- Create: cmd/ars/main.go

**Interfaces:**

~~~go
type CommandRunner interface {
    Run(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error
}

type FZF struct {
    Runner CommandRunner
}

func (FZF) Select(context.Context, []session.Session) (
    session.Session,
    bool,
    error,
)

func Resume(
    context.Context,
    Runner,
    string,
    session.Session,
    provider.Adapter,
) error

type Dependencies struct {
    LoadHosts func(string) ([]Host, error)
    Collect   func(context.Context, []Host) Result
    Pick      func(context.Context, []session.Session) (session.Session, bool, error)
    Resume    func(context.Context, session.Session) error
    Stdout    io.Writer
    Stderr    io.Writer
}

func Run(context.Context, []string, Dependencies) int
~~~

Run maps resume errors implementing ExitCode() int to that exact process exit
code; all other operational errors use the documented generic failure code.

- [ ] **Step 1: Write failing fzf tests**

Render a sanitized display row plus an opaque numeric index. Cover delimiter characters, tabs, newlines, ANSI escapes, duplicate labels, out-of-range output, malformed output, missing fzf, and exit 130 or 1 as successful cancellation. Selection must map only by the opaque index.

- [ ] **Step 2: Implement fzf selection**

Invoke system fzf with fixed arguments. Send display text through stdin. Parse only the returned index. Never execute or parse a host, CWD, title, or project as a command.

- [ ] **Step 3: Write failing resume tests**

Assert ssh -tt, normal interactive SSH behavior, one target argv value, fixed provider executable and arguments, validated canonical UUID, absolute CWD, POSIX single-quote escaping, and preservation of the SSH exit code. Reject any Session not bound to the selected configured host.

- [ ] **Step 4: Implement the separate resume path**

Build one remote command from the validated CWD and adapter ResumeSpec:

~~~text
Claude: cd '<cwd>' && exec claude --resume '<uuid>'
Codex:  cd '<cwd>' && exec codex resume '<uuid>'
~~~

Escape a single quote using the standard close-quote, escaped-quote, reopen sequence. Do not reuse collection flags that disable authentication or forwarding.

- [ ] **Step 5: Write failing app tests**

Cover ars, ars devbox, ars list --json, invalid arguments, unknown host, partial host failure, all-host failure without fzf, healthy empty JSON success, interactive no-sessions reporting, picker cancellation, picker failure, resume failure, and exact exit-code propagation.

- [ ] **Step 6: Implement app and main**

Keep parsing explicit for the three supported command shapes. Wire inventory, four-worker aggregation, five-second connect timeout, 60-second host timeout, JSON output, fzf, and resume. Main owns signals and os.Exit; app.Run remains unit-testable.

- [ ] **Step 7: Verify and commit**

Run:

~~~sh
go test ./internal/output ./internal/ssh ./internal/app ./cmd/ars
~~~

Commit:

~~~sh
git add internal/output internal/ssh/resume.go internal/ssh/resume_test.go internal/app cmd/ars
git commit -m "feat: select and resume remote sessions"
~~~

## Task 7: Close integration, release, and operator documentation

**Files:**

- Create: internal/ssh/sshd_integration_test.go
- Create: internal/app/e2e_test.go
- Create: README.md
- Create: .github/workflows/ci.yml
- Modify: cmd/ars-build/main.go
- Modify: internal/ssh/assets.go

- [ ] **Step 1: Add an opt-in ephemeral sshd integration test**

Start one disposable sshd using a generated host key and temporary authorized_keys. Exercise uname probing, collector upload, ARS/1 decoding, exact cleanup, and a fixed resume command. Skip with a clear reason when sshd is unavailable.

- [ ] **Step 2: Add a synthetic end-to-end app test**

Create sanitized Claude and Codex homes, run the real collector protocol through the fake SSH boundary, verify ars list --json, choose one opaque fzf index, and assert the final resume argv. Also prove one unreachable host does not discard a healthy host.

- [ ] **Step 3: Add release checks**

Make ars-build verify all three embedded collectors before producing ars. CI builds the assets first, then runs unit tests, race tests, vet, and local ars builds on Linux and macOS. It must not rely on network services after Go toolchain setup.

- [ ] **Step 4: Document the operational contract**

README covers installation, inventory examples, the three commands, prerequisites, JSON schema version 1, supported remote targets, provider inclusion rules, five-second and 60-second timeouts, limits, partial failures, host-key behavior, privacy exclusions, and SIGKILL temp-directory leftovers.

- [ ] **Step 5: Run the complete automated verification**

Run:

~~~sh
go run ./cmd/ars-build --assets-only
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/ars-build
~~~

Expected result: all commands exit zero and the local ars binary contains exactly the three supported collectors.

- [ ] **Step 6: Run the manual acceptance checklist**

On two real configured hosts:

1. Confirm one Claude session and one Codex session appear without raw content or source paths.
2. Resume each session and confirm the provider starts in the saved CWD.
3. Make one host unreachable and confirm the other host remains usable with a structured error.
4. Confirm a healthy host with no sessions reports success and does not open fzf.
5. Cancel fzf and confirm exit zero.
6. Interrupt collection and inspect only the nonce-specific temp location for cleanup.

- [ ] **Step 7: Commit the release surface**

~~~sh
git add internal/app/e2e_test.go internal/ssh/sshd_integration_test.go README.md .github/workflows/ci.yml cmd/ars-build internal/ssh/assets.go
git commit -m "test: verify remote session flow"
~~~

## Completion Gate

- [ ] Every design requirement maps to a task and focused test above.
- [ ] Public JSON uses schema_version 1 and dedicated DTOs.
- [ ] Protocol rejects nonce, count, version, truncation, UTF-8, and resource-limit violations.
- [ ] Collection supports only the three named remote targets and never uses an unbounded read.
- [ ] Provider fixtures are synthetic and no raw content or source path crosses the protocol.
- [ ] Collection and resume have separate SSH option sets.
- [ ] Partial failure, healthy empty, all-failed, cancellation, and resume exit behavior are verified.
- [ ] go test ./..., go test -race ./..., go vet ./..., and the release build all pass.
- [ ] The two-host manual checklist passes before release.
