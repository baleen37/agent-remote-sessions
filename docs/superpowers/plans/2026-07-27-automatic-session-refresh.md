# Automatic Session Refresh Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refresh a long-running `ars` TUI session inventory automatically once per minute without overlapping collection work.

**Architecture:** Add a Bubble Tea timer message to the existing TUI model. The message reschedules itself and delegates idle refreshes to `restartCollection`, preserving the existing progressive collection, cache, cancellation, selection, and interaction behavior.

**Tech Stack:** Go, Bubble Tea v2, standard `testing` package

## Global Constraints

- The refresh interval is fixed at one minute.
- A refresh tick must not start work while collection is already running.
- The existing cache, provider, collection, and wire formats must not change.
- Manual `r` behavior must not change.
- No new configuration or file-system watcher is introduced.

---

### Task 1: Add the automatic refresh timer

**Files:**
- Modify: `internal/tui/model.go:14-20,58-70,196-203,250-330,689-707`
- Test: `internal/tui/model_test.go`

**Interfaces:**
- Consumes: `model.restartCollection() (model, tea.Cmd)` and the existing `model.collecting` guard.
- Produces: `autoRefreshTickMsg`, `autoRefreshTick() tea.Cmd`, and `autoRefreshInterval = time.Minute`, all private to `internal/tui`.

- [ ] **Step 1: Write the failing model tests**

Add tests that prove the initial timer is installed, an idle tick starts one
collection, and a busy tick only reschedules itself:

```go
func TestModelInitSchedulesAutoRefresh(t *testing.T) {
	value := newModel(context.Background(), Dependencies{
		Collect: staticCollect(Result{}),
	})

	batch, ok := value.Init()().(tea.BatchMsg)
	if !ok || len(batch) != 5 {
		t.Fatalf("Init batch = %#v, want five commands including auto refresh", batch)
	}
}

func TestModelAutoRefreshStartsCollectionWhenIdle(t *testing.T) {
	value := readyModel()
	value.collecting = false
	value.generation = 1
	collects := 0
	value.deps.Collect = func(context.Context) <-chan Update {
		collects++
		return updates(Update{Done: true})
	}

	value, command := updateModel(value, autoRefreshTickMsg{})

	if command == nil || !value.collecting || value.generation != 2 || collects != 1 {
		t.Fatalf("auto refresh command=%v collecting=%t generation=%d collects=%d", command, value.collecting, value.generation, collects)
	}
}

func TestModelAutoRefreshSkipsCollectionWhenBusy(t *testing.T) {
	value := readyModel()
	value.collecting = true
	value.generation = 2
	collects := 0
	value.deps.Collect = func(context.Context) <-chan Update {
		collects++
		return updates(Update{Done: true})
	}

	value, command := updateModel(value, autoRefreshTickMsg{})

	if command == nil || !value.collecting || value.generation != 2 || collects != 0 {
		t.Fatalf("busy auto refresh command=%v collecting=%t generation=%d collects=%d", command, value.collecting, value.generation, collects)
	}
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
go test ./internal/tui -run 'TestModel(InitSchedulesAutoRefresh|AutoRefresh)' -count=1
```

Expected: FAIL because `autoRefreshTickMsg` and the fifth `Init` command do not
exist.

- [ ] **Step 3: Implement the minimal timer**

In `internal/tui/model.go`, add the fixed interval and message:

```go
const (
	autoRefreshInterval  = time.Minute
	maxStatusBytes       = 256
	interactionIdle      = 300 * time.Millisecond
	statusDismissSeconds = 5
	statusTickInterval   = time.Second
)

type autoRefreshTickMsg struct{}
```

Schedule it from `Init`:

```go
func (value model) Init() tea.Cmd {
	return tea.Batch(
		value.initialCollect,
		spinnerTick(value.generation),
		activityTick(),
		autoRefreshTick(),
		tea.RequestBackgroundColor,
	)
}
```

Route the message through `dispatchModel`:

```go
case autoRefreshTickMsg:
	next := autoRefreshTick()
	if value.collecting {
		return value, next
	}
	value, command := value.restartCollection()
	return value, tea.Batch(command, next)
```

Add the timer command near the existing tick helpers:

```go
func autoRefreshTick() tea.Cmd {
	return tea.Tick(autoRefreshInterval, func(time.Time) tea.Msg {
		return autoRefreshTickMsg{}
	})
}
```

- [ ] **Step 4: Format and verify GREEN**

Run:

```bash
gofmt -w internal/tui/model.go internal/tui/model_test.go
go test ./internal/tui -run 'TestModel(InitSchedulesAutoRefresh|AutoRefresh)' -count=1
go test ./internal/tui -count=1
```

Expected: all focused and package tests PASS.

- [ ] **Step 5: Run repository verification**

Generate the ignored embedded collector assets, then run the full verification
suite:

```bash
go run ./cmd/ars-build --assets-only
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/ars
git diff --check
```

Expected: every command exits 0.

- [ ] **Step 6: Commit the implementation**

```bash
git add internal/tui/model.go internal/tui/model_test.go
git commit -m "feat(tui): refresh sessions automatically"
```
