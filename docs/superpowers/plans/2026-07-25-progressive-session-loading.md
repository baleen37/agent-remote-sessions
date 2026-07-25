# Progressive Session Loading Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show cached rows immediately, stream freshly discovered seven-day sessions before exhaustive history completes, and apply updates without moving the TUI during user interaction.

**Architecture:** Provider adapters enumerate their files once, parse the seven-day partition first, and then finish the remaining inventory while Claude and Codex run concurrently. A nonce-bound ARS/3 stream carries one optional early snapshot and one authoritative final snapshot over the existing single SSH collector invocation. `internal/app` overlays early sessions on the full host cache, replaces the host only on final success, and keeps collection phases private; the TUI coalesces snapshots while the user is interacting and displays only generic refresh copy.

**Tech Stack:** Go 1.26, Bubble Tea v2, lipgloss v2, OpenSSH subprocess streaming, stdlib `encoding/json`, `io.Pipe`, and `time`.

**Spec:** `docs/superpowers/specs/2026-07-25-session-load-performance-design.md`

## Global Constraints

- The user-facing UI must not expose cache provenance or progressive phase names. Loading copy is only `refreshing` and `loading sessions…`.
- The final session identities and final provider diagnostics must match exhaustive discovery before this change.
- Every eligible JSONL file is eventually read and validated. The implementation must not omit old sessions or stop parsing a file early.
- The provider inventory is enumerated once per refresh, and each history file is parsed at most once.
- The recent boundary is exactly seven days and is shared with the existing TUI stale-session cutoff.
- The existing host cache path, schema version `1`, permissions, validation, and atomic write behavior remain unchanged.
- Only an authoritative final host result may overwrite the host cache.
- The ARS/3 stream uses the existing one probe plus one collector SSH invocation and stores nothing on peers.
- Protocol limits cover the entire ARS/3 stream. Each snapshot is independently nonce-bound and validated before its callback.
- Public JSON schema version `1`, attach behavior, tmux behavior, search matching, grouping, and sort order remain unchanged.
- Existing collection generation cancellation and host worker bound remain effective.
- Before tests in a fresh checkout, run `go run ./cmd/ars-build --assets-only`; generated collectors and the root `ars` binary are local outputs and must not be committed.
- Repository style remains descriptive lower-case local names, wrapped errors, focused scenario tests, `git diff --check`, and no unrelated refactors.

## File Structure

- `internal/session/session.go`: owns the shared seven-day product window.
- `internal/provider/progressive.go`: coordinates two providers and joins their early/final events without exposing filesystem details.
- `internal/provider/claude.go`, `codex.go`: enumerate once, partition by modification time, and preserve original-order final aggregation.
- `internal/protocol/protocol.go`: owns strict ARS/3 stream framing and bounded incremental decode.
- `cmd/ars-collector/main.go`: writes early and final snapshots from one provider scan.
- `internal/ssh/collect.go`: decodes ARS/3 while the SSH process is still running.
- `internal/app/aggregate.go`, `stream.go`: bind early sessions, overlay them on cached history, and save only final results.
- `cmd/ars/main.go`: wires local and remote progressive callbacks into the app collector.
- `internal/tui/model.go`, `view.go`: coalesce updates around interaction and render generic loading copy.
- `README.md`, `docs/scenarios/progressive-session-loading.md`: document behavior and the repeatable acceptance procedure.

---

### Task 1: One-pass recent-first provider discovery

**Files:**
- Create: `internal/provider/progressive.go`
- Create: `internal/provider/progressive_test.go`
- Modify: `internal/session/session.go`
- Modify: `internal/session/session_test.go`
- Modify: `internal/tui/filter.go`
- Modify: `internal/tui/filter_test.go`
- Modify: `internal/provider/provider.go`
- Modify: `internal/provider/provider_test.go`
- Modify: `internal/provider/claude.go`
- Modify: `internal/provider/claude_test.go`
- Modify: `internal/provider/codex.go`
- Modify: `internal/provider/codex_test.go`
- Modify: `internal/provider/test_helpers_test.go`

**Interfaces:**

```go
// internal/session/session.go
const RecentWindow = 7 * 24 * time.Hour

// internal/provider/progressive.go
type Phase uint8

const (
	PhaseRecent Phase = iota + 1
	PhaseComplete
)

type Snapshot struct {
	Phase      Phase
	Candidates []session.Candidate
	Results    []Result // nil for PhaseRecent; authoritative for PhaseComplete
}

// Added to Adapter. Builtins and the two existing test fakes implement it.
DiscoverStream(
	context.Context,
	string,
	time.Time,
	func(Phase, Result) error,
) error

func DiscoverAllStream(
	ctx context.Context,
	home string,
	adapters []Adapter,
	recentAfter time.Time,
	emit func(Snapshot) error,
) error
```

Each adapter records file descriptors in original traversal order. It parses recent descriptors first and stores their per-file outcomes by original index. After the early callback, it parses old descriptors into the remaining slots. Final aggregation replays every outcome in original traversal order so the 10,000-session bound, deduplication, skipped count, and diagnostic precedence remain identical to the current implementation.

- [ ] **Step 1: Write failing shared-window and provider coordination tests**

Add tests with controlled adapters that block on channels:

```go
func TestDiscoverAllStreamStartsProvidersConcurrentlyAndKeepsRegistryOrder(t *testing.T) {
	started := make(chan session.Provider, 2)
	release := make(chan struct{})
	adapters := progressiveAdapters(started, release)
	var snapshots []Snapshot
	done := make(chan error, 1)
	go func() {
		done <- DiscoverAllStream(context.Background(), "/home", adapters, time.Unix(100, 0), func(value Snapshot) error {
			snapshots = append(snapshots, value)
			return nil
		})
	}()
	first, second := <-started, <-started
	if first == second {
		t.Fatalf("providers did not start independently: %q %q", first, second)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if snapshots[0].Phase != PhaseRecent || snapshots[1].Phase != PhaseComplete {
		t.Fatalf("phases = %#v", snapshots)
	}
	if snapshots[1].Results[0].Provider != session.Claude || snapshots[1].Results[1].Provider != session.Codex {
		t.Fatalf("result order = %#v", snapshots[1].Results)
	}
}
```

Also add:

- `TestRecentWindowMatchesTUIBoundaryContract`
- `TestDiscoverAllStreamPropagatesCallbackAndContextErrors`
- `TestDiscoverAllKeepsFinalOnlyCompatibility`

Run:

```bash
go test ./internal/session ./internal/provider -run 'TestRecentWindow|TestDiscoverAllStream|TestDiscoverAllKeeps' -v
```

Expected: compile failure because the new constant, phases, method, and function do not exist.

- [ ] **Step 2: Write failing Claude and Codex one-pass partition tests**

Use `os.Chtimes` around a fixed `recentAfter` and an adapter-level read hook or FIFO guard to prove the old file is not opened before the early callback:

```go
func TestCodexDiscoverStreamEmitsRecentBeforeOpeningOldHistory(t *testing.T) {
	home := progressiveCodexHome(t)
	cutoff := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	setHistoryTimes(t, home, cutoff)
	var phases []Phase
	err := (codexAdapter{}).DiscoverStream(context.Background(), home, cutoff, func(phase Phase, result Result) error {
		phases = append(phases, phase)
		if phase == PhaseRecent && len(result.Sessions) != 1 {
			t.Fatalf("recent sessions = %#v", result.Sessions)
		}
		return nil
	})
	if err != nil || !slices.Equal(phases, []Phase{PhaseRecent, PhaseComplete}) {
		t.Fatalf("phases/error = %v/%v", phases, err)
	}
}
```

Add equivalent Claude coverage and boundary cases:

- a file exactly at `recentAfter` is early
- a file one nanosecond older is final-only
- root absence emits empty early data followed by the existing `Absent` final result
- symlinks, FIFOs, depth limits, corrupt histories, mixed Claude IDs, and duplicate Codex metadata retain their existing outcomes
- a small session limit produces the same final identities as the old traversal order

Run:

```bash
go test ./internal/provider -run 'TestClaudeDiscoverStream|TestCodexDiscoverStream|TestDiscoverAllStream' -v
```

Expected: failure because discovery still parses in one sequential pass.

- [ ] **Step 3: Implement inventory partitioning and concurrent phase joins**

Implement a small internal descriptor and outcome model:

```go
type historyFile struct {
	path     string
	modified time.Time
}

type historyOutcome struct {
	candidate session.Candidate
	include   bool
	issue     string
}
```

Keep provider-specific traversal rules in their existing files. `DiscoverStream` emits exactly `PhaseRecent` then `PhaseComplete`. `DiscoverAllStream` starts the two adapters in goroutines, waits for both provider events for a phase, validates candidates, sorts the combined data, and invokes its callback from one goroutine. Each adapter's recent callback stays blocked at this join barrier until the combined recent callback returns, so neither adapter starts old-file parsing before useful recent data has been emitted. The early `Snapshot.Results` is nil; the final snapshot carries both ordered provider results.

Change `filter.go` to use `session.RecentWindow` instead of its private duration while keeping the existing `staleAfter` name if tests depend on it:

```go
const staleAfter = session.RecentWindow
```

Keep `DiscoverAll` as a final-only compatibility wrapper over the new machinery.

- [ ] **Step 4: Run focused and provider regression tests**

```bash
go test ./internal/session ./internal/provider
```

Expected: PASS with existing discovery identities and diagnostics unchanged.

- [ ] **Step 5: Commit**

```bash
git add internal/session internal/provider internal/tui/filter.go
git commit -m "feat: discover recent sessions before full history"
```

---

### Task 2: Allocation-light JSONL parsing

**Files:**
- Modify: `internal/provider/claude.go`
- Modify: `internal/provider/claude_test.go`
- Modify: `internal/provider/codex.go`
- Modify: `internal/provider/codex_test.go`

**Interfaces:**

No public signatures change. Each line is first decoded into a header that omits large raw payloads. A second decode happens only when the record can affect the candidate.

```go
type codexHeader struct {
	Type string `json:"type"`
}

type claudeHeader struct {
	Type        string `json:"type"`
	SessionID   string `json:"sessionId"`
	CWD         string `json:"cwd"`
	Title       string `json:"title"`
	CustomTitle string `json:"customTitle"`
	AgentName   string `json:"agentName"`
	AgentID     string `json:"agentId"`
	IsInternal  bool   `json:"isInternal"`
	IsMeta      bool   `json:"isMeta"`
	IsSidechain bool   `json:"isSidechain"`
}
```

- [ ] **Step 1: Write semantic fast-path tests**

Add cases proving that the header decode still validates the whole line and handles escaped JSON string values:

```go
func TestCodexReadHistoryRecognizesEscapedSessionMetaType(t *testing.T) {
	path := writeCodexHistory(t,
		`{"type":"session\u005fmeta","payload":{"id":"123e4567-e89b-42d3-a456-426614174000","cwd":"/work","source":"cli","thread_source":"user"}}`,
		`{"type":"session_meta","payload":{"id":"123e4567-e89b-42d3-a456-426614174000","cwd":"/work","source":"cli","thread_source":"user"}}`,
	)
	_, include, issue := (codexAdapter{}).readHistory(path)
	if include || issue != "incompatible" {
		t.Fatalf("duplicate escaped metadata = %t/%q", include, issue)
	}
}
```

Add:

- corrupt irrelevant lines still produce `corrupt`
- Codex ignores large irrelevant payload content without changing title/metadata
- Claude decodes prompt content only until the first substantial prompt title, while later native titles and mixed IDs still win/fail exactly as today
- malformed Claude `message` on an eligible user record preserves the existing title fallback behavior

Run:

```bash
go test ./internal/provider -run 'TestCodexReadHistory|TestClaudeReadHistory|TestCodexDiscover|TestClaudeDiscover' -v
```

- [ ] **Step 2: Implement two-stage decoding**

For Codex, decode `codexHeader` on every line. Decode the full `codexEnvelope` only for every `session_meta` and for `event_msg` while the title is empty. For Claude, decode `claudeHeader` on every line and decode a message-only struct only when `Type == "user"`, `!IsMeta`, and a substantial prompt title is still missing.

Do not use substring matching as a shortcut: escaped JSON type strings and whitespace must retain `encoding/json` semantics.

- [ ] **Step 3: Run provider tests and capture a local parser baseline**

```bash
go test ./internal/provider
go build -o /tmp/ars-collector-progressive ./cmd/ars-collector
```

The collector build is a compile check here; Task 7 records final measurements after ARS/3 wiring.

- [ ] **Step 4: Commit**

```bash
git add internal/provider/claude.go internal/provider/claude_test.go internal/provider/codex.go internal/provider/codex_test.go
git commit -m "perf: avoid copying irrelevant session payloads"
```

---

### Task 3: Strict bounded ARS/3 snapshot stream

**Files:**
- Modify: `internal/protocol/protocol.go`
- Modify: `internal/protocol/protocol_test.go`
- Modify: `internal/protocol/fuzz_test.go`

**Interfaces:**

```go
type Snapshot struct {
	Phase      provider.Phase
	Discovered []session.Discovered
	Results    []provider.Result
	Report     runtime.Report
}

type StreamEncoder struct {
	// private nonce, boundedEncoder, phase state, and closed state
}

func NewStreamEncoder(output io.Writer, nonce string, limits Limits) (*StreamEncoder, error)
func (encoder *StreamEncoder) Encode(snapshot Snapshot) error
func (encoder *StreamEncoder) Close() error
func DecodeStream(input io.Reader, nonce string, limits Limits, emit func(Snapshot) error) error
```

`Encode` and `Decode` keep their existing signatures as complete-only wrappers. A progressive stream contains an optional recent snapshot followed by exactly one complete snapshot. Recent snapshots contain sessions plus a runtime summary and no provider summaries. Complete snapshots retain exactly two provider summaries and the current candidate/report consistency checks.

- [ ] **Step 1: Write failing progressive round-trip tests**

```go
func TestARS3StreamEmitsValidatedRecentBeforeComplete(t *testing.T) {
	var output bytes.Buffer
	encoder, err := NewStreamEncoder(&output, testNonce, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(recentProtocolSnapshot()); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(completeProtocolSnapshot()); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	var phases []provider.Phase
	if err := DecodeStream(&output, testNonce, DefaultLimits(), func(value Snapshot) error {
		phases = append(phases, value.Phase)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(phases, []provider.Phase{provider.PhaseRecent, provider.PhaseComplete}) {
		t.Fatalf("phases = %v", phases)
	}
}
```

Add fail-closed tests for:

- complete before recent followed by another snapshot
- duplicate recent or complete snapshots
- recent provider summaries
- complete missing either provider summary
- snapshot count mismatch
- nonce mismatch, non-canonical framing, trailing bytes, truncation, and callback error
- per-snapshot session bound and whole-stream byte bound
- invalid runtime fields in either snapshot
- `Decode` returning no data on final stream failure

- [ ] **Step 2: Implement ARS/3 framing and incremental decode**

Use one outer exact envelope and strict JSON control frames:

```text
ARS/3 BEGIN <nonce>
{"type":"snapshot","phase":"recent"}
...session frames...
{"type":"runtime",...}
{"type":"snapshot_end","phase":"recent","sessions":N}
{"type":"snapshot","phase":"complete"}
...session, summary, runtime frames...
{"type":"snapshot_end","phase":"complete","sessions":N}
ARS/3 END <nonce>
```

Reuse existing session, summary, runtime, strict-field, nonce, UTF-8, canonical line, and semantic validation helpers. Move total-byte accounting into the stream encoder so both snapshots share one limit. Invoke `emit` only after the current snapshot end has validated its count and semantics.

- [ ] **Step 3: Update fuzz seeds and invariants**

Replace ARS/2 seeds with ARS/3 complete-only and progressive envelopes. Keep the invariant that any returned error from final `Decode` yields nil data. Add malformed snapshot-control and ordering seeds.

- [ ] **Step 4: Run protocol tests and fuzz smoke**

```bash
go test ./internal/protocol
go test ./internal/protocol -run=^$ -fuzz=FuzzDecode -fuzztime=10s
```

Expected: PASS and no crash, excessive allocation, or accepted malformed stream.

- [ ] **Step 5: Commit**

```bash
git add internal/protocol
git commit -m "feat: stream bounded ARS 3 snapshots"
```

---

### Task 4: One-process collector and streaming SSH decode

**Files:**
- Modify: `cmd/ars-collector/main.go`
- Modify: `cmd/ars-collector/main_test.go`
- Modify: `internal/ssh/collect.go`
- Modify: `internal/ssh/collect_test.go`
- Modify: `internal/ssh/sshd_integration_test.go`

**Interfaces:**

```go
func CollectStream(
	ctx context.Context,
	runner Runner,
	assets CollectorAssets,
	target string,
	options CollectOptions,
	emitRecent func([]session.Discovered) error,
) ([]session.Discovered, []provider.Result, runtime.Report, error)
```

Existing `Collect` keeps its signature and calls `CollectStream` with a nil callback.

- [ ] **Step 1: Write failing collector stream tests**

Update the collector fake adapters to implement `DiscoverStream`. Add a test whose final provider phase blocks after the recent callback:

```go
func TestRunWritesRecentSnapshotBeforeFullDiscoveryCompletes(t *testing.T) {
	adapter, release := blockingProgressiveAdapter(t)
	reader, writer := io.Pipe()
	done := make(chan int, 1)
	go func() {
		done <- run(context.Background(), []string{collectorNonce}, "/home", adapter, writer, io.Discard)
		_ = writer.Close()
	}()
	recent := decodeFirstSnapshot(t, reader)
	if recent.Phase != provider.PhaseRecent {
		t.Fatalf("first phase = %v", recent.Phase)
	}
	close(release)
	if code := <-done; code != 0 {
		t.Fatalf("run code = %d", code)
	}
}
```

Retain the current sorted sessions, provider diagnostics, invalid-candidate privacy, runtime-unavailable, and encoding-failure tests against the final decoded snapshot.

- [ ] **Step 2: Write failing SSH early-delivery and lifecycle tests**

Add:

- `TestCollectStreamEmitsRecentBeforeRunnerReturns`
- `TestCollectStreamUsesOneProbeAndOneCollectorForBothSnapshots`
- `TestCollectStreamReturnsFinalDataAndCollectCompatibility`
- `TestCollectStreamCallbackErrorCancelsAndCleansOwnedTemporaryPath`
- `TestCollectStreamRejectsTruncatedFinalAfterValidRecent`
- update the ephemeral sshd collector fixture to emit ARS/3 recent plus complete snapshots

The early-delivery test must hold the fake runner open after writing the recent snapshot and assert the callback fires before releasing it.

- [ ] **Step 3: Implement collector streaming**

In `cmd/ars-collector`, create one `protocol.StreamEncoder`, call `provider.DiscoverAllStream`, inspect runtime state for each candidate snapshot, write the recent snapshot immediately, then write the authoritative final snapshot and close the encoder. Print provider diagnostics only from the final results.

In `internal/ssh`, run the collector process in a goroutine with stdout connected to an `io.Pipe`. Decode with `protocol.DecodeStream` on the caller goroutine. Capture only the bounded stdout prefix needed by `parseTemporaryPath` with a writer that stores at most the control-line limit but always reports the original write length; do not buffer the whole protocol stream.

On decode or callback failure, close the pipe reader to unblock the SSH writer, join the process error with context state, and preserve the existing nonce-specific cleanup attempt. Return only the final snapshot from `CollectStream`.

- [ ] **Step 4: Run SSH and collector tests**

```bash
go test ./cmd/ars-collector ./internal/ssh
ARS_TEST_EPHEMERAL_SSHD=1 go test ./internal/ssh -run TestEphemeralSSHDCollectsAndAttaches -v
```

Expected: focused tests PASS; the opt-in test may skip only when its documented sshd prerequisites are unavailable.

- [ ] **Step 5: Commit**

```bash
git add cmd/ars-collector internal/ssh
git commit -m "feat: stream recent sessions over one ssh collection"
```

---

### Task 5: Cache overlay, final replacement, and production wiring

**Files:**
- Modify: `internal/app/aggregate.go`
- Modify: `internal/app/aggregate_test.go`
- Modify: `internal/app/stream.go`
- Modify: `internal/app/stream_test.go`
- Modify: `internal/app/e2e_test.go`
- Modify: `cmd/ars/main.go`

**Interfaces:**

```go
type Collector func(
	context.Context,
	Host,
	func([]session.Discovered) error,
) (
	[]session.Discovered,
	[]provider.Result,
	runtime.Report,
	error,
)

func bindSessions(target string, discovered []session.Discovered) ([]session.Session, error)
func overlaySessions(current, recent []session.Session) []session.Session
```

The callback carries only validated early sessions. The return values remain the authoritative exhaustive collection and final diagnostics. No phase enum or cache provenance enters `tui.Update`.

- [ ] **Step 1: Write failing overlay and cache-write tests**

Replace the primary stream scenario with:

```go
func TestCollectHostsStreamEmitsCacheThenRecentOverlayThenExhaustiveReplace(t *testing.T) {
	collector := progressiveCollector(cachedOld(), freshRecent(), exhaustive())
	var snapshots []Snapshot
	CollectHostsStream(context.Background(), hosts(), 1, collector, cacheRecorder(), func(value Snapshot) {
		snapshots = append(snapshots, value)
	})
	if len(snapshots) != 3 {
		t.Fatalf("snapshot count = %d, want cache, recent, final", len(snapshots))
	}
	if !containsIdentity(snapshots[1].Result.Sessions, cachedOldID) {
		t.Fatal("recent overlay erased cached history")
	}
	if containsIdentity(snapshots[2].Result.Sessions, removedCachedID) {
		t.Fatal("final result did not replace cached history")
	}
}
```

Add:

- matching identity refreshes title, CWD, timestamp, and runtime
- early snapshot never invokes `HostCache.Save`
- final success saves exactly once
- final failure keeps cached/early rows, reports the host error, and saves zero times
- a host with no cache keeps early rows after final failure
- one host's early callback is emitted while another host remains blocked, without exceeding the worker limit
- `CollectHosts` and JSON E2E return final data only

- [ ] **Step 2: Implement binding, overlay, and stream completion events**

Extract `bindSessions` from current `collectHost`. Validate early sessions with `session.BindDiscovered`, but compute provider and runtime diagnostics only for final results.

In `CollectHostsStream`, send early and final/error events from each worker to the existing single coordinator goroutine. Early events overlay with the existing `sessionIdentity` and `sessionLess` ordering, keep `pending[index] = true`, and do not save. Final success replaces the host, saves, and clears pending. Final failure preserves any `hasData[index]` sessions, never saves, and clears pending.

- [ ] **Step 3: Wire local and remote progressive callbacks**

In `cmd/ars/main.go`:

- local TUI collection calls `provider.DiscoverAllStream` with `time.Now().Add(-session.RecentWindow)`, runs `runtime.Inspect` for each snapshot, invokes the app callback for recent candidates, and returns the final values
- remote TUI collection calls `ssh.CollectStream`
- headless JSON continues through `CollectHosts` and receives only final results
- `tui.Update` remains `{Result, Loading, Done}` with no cache or phase fields

- [ ] **Step 4: Run app and command tests**

```bash
go test ./internal/app ./cmd/ars
```

Expected: PASS, including existing canonical JSON and attach routing E2E.

- [ ] **Step 5: Commit**

```bash
git add internal/app cmd/ars/main.go
git commit -m "feat: overlay recent sessions before final refresh"
```

---

### Task 6: Interaction-safe TUI updates and generic loading copy

**Files:**
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/view.go`
- Modify: `internal/tui/view_test.go`
- Modify: `internal/tui/pty_integration_test.go`

**Interfaces:**

```go
const interactionIdle = 300 * time.Millisecond

type interactionIdleMsg struct {
	generation uint64
	sequence   uint64
}

// Added to model:
pendingUpdate  *Update
coalescing     bool
interactionSeq uint64
```

- [ ] **Step 1: Write failing model coalescing tests**

Add deterministic tests that inject `interactionIdleMsg` directly:

```go
func TestModelCollapsesStagedUpdatesToNewestAfterInteraction(t *testing.T) {
	value := readyModel()
	value.collecting = true
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	recent := Update{Result: resultWithTitle("early")}
	final := Update{Result: resultWithTitle("final"), Done: true}
	value, next := updateModel(value, collectUpdateMsg{generation: value.generation, update: recent, channel: updates(final)})
	if value.result.Sessions[0].Title == "early" || next == nil {
		t.Fatal("interaction did not stage while continuing to drain updates")
	}
	value, _ = updateModel(value, collectUpdateMsg{generation: value.generation, update: final})
	value, _ = updateModel(value, interactionIdleMsg{generation: value.generation, sequence: value.interactionSeq})
	if value.result.Sessions[0].Title != "final" || value.collecting {
		t.Fatalf("settled model = %#v", value)
	}
}
```

Add:

- snapshots apply immediately before interaction
- navigation and search both arm/reset coalescing
- stale sequence and stale generation idle messages are ignored
- canonical selection survives settled apply
- restart and attach-return clear pending state
- final pending data keeps `collecting` true until it is applied

- [ ] **Step 2: Implement interaction staging**

Factor current collect application into one helper that assigns result/loading/done, calls `refreshVisible`, `evictActivity`, and `syncPreview` exactly once.

While coalescing, replace `pendingUpdate` but immediately schedule the next `waitForUpdate` so a final snapshot supersedes an early one. Relevant navigation, search, fold, filter, pin, and show-all keys increment `interactionSeq` and schedule a generation-bound `tea.Tick`. `restartCollection` clears all pending/coalescing state.

- [ ] **Step 3: Write and implement generic view copy tests**

Add:

```go
func TestHeaderUsesOnlyGenericRefreshingCopy(t *testing.T) {
	value := readyModel()
	value.collecting = true
	value.loading = []string{"localhost", "server"}
	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "refreshing") {
		t.Fatalf("header = %q", content)
	}
	for _, forbidden := range []string{"cached", "refreshing recent", "finishing history", "complete", "loading localhost", "loading server"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("header exposed %q: %q", forbidden, content)
		}
	}
}
```

For empty rows, render `loading sessions…` only when collection is active, the underlying result has zero sessions, the query is empty, and no filter is active. Completed healthy empty state keeps `no sessions yet` and its new-user hint. Replace `loadingLabel` with a constant generic suffix.

- [ ] **Step 4: Extend PTY behavior**

Update the PTY integration fixture to send cache, early, and final `tui.Update` values. Send navigation between early and final events and assert the rendered selected canonical session does not change until idle, then assert the final title appears without any phase label.

- [ ] **Step 5: Run TUI tests**

```bash
go test ./internal/tui
```

Expected: PASS without sleep-based unit tests.

- [ ] **Step 6: Commit**

```bash
git add internal/tui
git commit -m "feat: keep session refresh stable during interaction"
```

---

### Task 7: Documentation, full verification, live performance, and actual-use QA

**Files:**
- Modify: `README.md`
- Create: `docs/scenarios/progressive-session-loading.md`

**Interfaces:**

No code interface changes. The scenario document records exact commands, expected phase-independent UI, identity comparison, and measured timings without storing private titles, CWDs, or raw transcripts.

- [ ] **Step 1: Update user documentation**

Change README to:

- describe a private version-matched ARS/3 streaming helper
- say rows appear immediately from the last successful host result, then refresh silently
- remove the stale claim that rows are marked `cached`
- describe one generic `refreshing` indicator
- state that likely-visible histories refresh first while exhaustive history continues
- preserve the no-daemon, no-peer-state, one-shot collection, and public JSON v1 contracts
- update ARS/2 bound/privacy wording to ARS/3

- [ ] **Step 2: Write the repeatable scenario**

Create `docs/scenarios/progressive-session-loading.md` with these gates:

```text
1. Build version-matched assets and ars.
2. Capture baseline final identities and diagnostics without titles/CWDs.
3. Start the PTY with a warm host cache and record first-row time.
4. Verify the only progress text is generic "refreshing".
5. Navigate and search while early/final updates arrive; selected identity stays fixed.
6. Wait for completion and compare final identities/diagnostics exactly.
7. Repeat localhost timing three times and report median early/final time.
8. Repeat against the configured SSH peer and prove one probe plus one collector lifecycle.
```

- [ ] **Step 3: Regenerate assets and run the complete automated gate**

```bash
go run ./cmd/ars-build --assets-only
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/ars-build
git diff --check
git status --short
```

Expected:

- all tests PASS
- race and vet PASS
- the root `ars` binary exists locally
- generated collectors and `ars` are ignored local outputs
- only intentional source/docs changes appear in git

- [ ] **Step 4: Compare final semantics against the baseline commit**

Build `49fb94e` in a disposable worktree or temporary checkout and the feature branch in the current worktree. Run both collectors against the same home, normalize output to:

```text
provider<TAB>native_id<TAB>updated_at<TAB>runtime_state
provider-summary<TAB>provider<TAB>status<TAB>seen<TAB>skipped<TAB>error_code
```

Sort and diff those normalized final records. Expected: no difference. Do not print title, CWD, transcript content, or host secrets.

- [ ] **Step 5: Measure warm early and final latency**

Run the feature collector three times after one warm-up. Record:

- time until the early callback
- time until the final callback

Expected acceptance:

- early median at or below 1.5 seconds on the measured localhost dataset
- final median at or below 3.71 seconds, a 30% improvement from 5.31 seconds

If either target misses, profile before changing scope; do not hide histories or weaken validation.

- [ ] **Step 6: Run actual PTY and configured-peer QA**

Run the built `./ars` in a real PTY:

- first usable rows appear within one second from the existing host cache
- the header shows only generic `refreshing`
- no cache or phase labels appear
- movement, search, fold, filter, pin, and show-all remain stable during refresh
- final sessions arrive automatically after input pauses
- attach and return-to-refresh still work

Run the same scenario with the configured SSH peer. Verify recent data arrives before final completion, final errors remain actionable, and the peer stores no collector after exit.

- [ ] **Step 7: Commit documentation and evidence**

```bash
git add README.md docs/scenarios/progressive-session-loading.md
git commit -m "docs: verify progressive session loading"
```

- [ ] **Step 8: Whole-branch completion audit**

Confirm every spec requirement against current evidence:

```bash
git log --oneline origin/main..HEAD
git diff --stat origin/main...HEAD
git diff --check origin/main...HEAD
go test ./...
go test -race ./...
go vet ./...
```

Do not claim completion if automated proof, actual PTY behavior, localhost performance, final semantic equality, or configured-peer behavior is missing.
