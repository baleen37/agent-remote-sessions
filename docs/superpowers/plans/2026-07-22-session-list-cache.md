# Session List Cache Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show the TUI session list instantly from a per-host disk cache, then refresh each host live in the background, updating rows as each host finishes.

**Architecture:** A new cache store in `internal/app` persists per-host session lists as JSON under `${XDG_CACHE_HOME:-~/.cache}/ars/hosts/`. A new streaming collector `app.CollectHostsStream` emits a merged `Snapshot` after loading the cache and again after every host's live collection completes (existing `app.CollectHosts` becomes a thin wrapper over it). The TUI's `Dependencies.Collect` changes from `func(ctx) Result` to `func(ctx) <-chan Update`; the model consumes updates one message at a time, marks hosts still showing cached data as stale, and the view renders a dim `cached` column on stale rows.

**Tech Stack:** Go (per `go.mod`), Bubble Tea v2 (`charm.land/bubbletea/v2`), lipgloss v2, stdlib `encoding/json` + `net/url`.

**Spec:** `docs/superpowers/specs/2026-07-22-list-cache-design.md`

## Global Constraints

- Cache path: `${XDG_CACHE_HOME:-~/.cache}/ars/hosts/<url.PathEscape(target)>.json`, one file per host.
- Cache dir permission `0700`, cache files `0600`.
- Cache schema version `1`; corrupt files or version mismatch are a silent cache miss.
- Writes are atomic: temp file in the same directory, then `os.Rename`.
- No TTL, no background daemon/polling. Collection runs only on TUI start, `r`, and attach-return.
- Nothing is persisted on peers; the cache exists only on the local machine.
- Failed live collection keeps cached rows, keeps the stale mark, and reports via the existing Errors channel.
- Worker limit stays 4. Generation guard semantics stay as today: messages from an old generation are discarded.
- Before running any tests in a fresh checkout: `go run ./cmd/ars-build --assets-only` (required once; `internal/ssh` embeds generated collector blobs and `go test ./internal/...` fails to compile without them).
- Repo style: descriptive lower-case variable names (`value`, `index`, `collections`), errors wrapped with `fmt.Errorf("...: %w", err)`, tests are table-free scenario functions named `TestXxx` with `t.Fatalf`.

---

### Task 1: Per-host cache store

**Files:**
- Create: `internal/app/cache.go`
- Create: `internal/app/cache_test.go`

**Interfaces:**
- Consumes: `session.Session`, `session.Discovered`, `session.BindDiscovered` from `internal/session`.
- Produces (used by Tasks 2 and 5):
  - `func CachePath() (string, error)` — `${XDG_CACHE_HOME:-~/.cache}/ars/hosts`
  - `func LoadHostCache(dir, target string) ([]session.Session, bool)` — `false` means cache miss
  - `func SaveHostCache(dir, target string, sessions []session.Session) error`

- [ ] **Step 1: Write the failing tests**

Create `internal/app/cache_test.go`:

```go
package app

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func cacheSession(host string) session.Session {
	return session.Session{
		Host: host,
		Candidate: session.Candidate{
			Provider:  session.Claude,
			NativeID:  "123e4567-e89b-42d3-a456-426614174000",
			UpdatedAt: time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC),
			CWD:       "/work/ars",
			Title:     "cache roundtrip",
		},
		Runtime: session.Runtime{State: session.RuntimeSaved},
	}
}

func TestCachePathPrefersXDGCacheHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/custom/cache")
	path, err := CachePath()
	if err != nil || path != filepath.Join("/custom/cache", "ars", "hosts") {
		t.Fatalf("CachePath() = %q, %v", path, err)
	}

	t.Setenv("XDG_CACHE_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}
	path, err = CachePath()
	if err != nil || path != filepath.Join(home, ".cache", "ars", "hosts") {
		t.Fatalf("CachePath() fallback = %q, %v", path, err)
	}
}

func TestHostCacheRoundTripEncodesTargetAndRestrictsPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hosts")
	target := "user@build box:2222"
	want := []session.Session{cacheSession(target)}

	if err := SaveHostCache(dir, target, want); err != nil {
		t.Fatalf("SaveHostCache() error = %v", err)
	}

	path := filepath.Join(dir, url.PathEscape(target)+".json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cache file mode = %v, err = %v", info, err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("cache dir mode = %v, err = %v", dirInfo, err)
	}

	got, ok := LoadHostCache(dir, target)
	if !ok || len(got) != 1 || got[0] != want[0] {
		t.Fatalf("LoadHostCache() = %#v, %t", got, ok)
	}
}

func TestHostCacheSaveOverwritesAtomically(t *testing.T) {
	dir := t.TempDir()
	target := "server"
	if err := SaveHostCache(dir, target, []session.Session{cacheSession(target)}); err != nil {
		t.Fatalf("first SaveHostCache() error = %v", err)
	}
	if err := SaveHostCache(dir, target, nil); err != nil {
		t.Fatalf("second SaveHostCache() error = %v", err)
	}
	got, ok := LoadHostCache(dir, target)
	if !ok || len(got) != 0 {
		t.Fatalf("LoadHostCache() after overwrite = %#v, %t", got, ok)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("cache dir entries = %v, err = %v", entries, err)
	}
}

func TestHostCacheMissOnAbsentCorruptOrForeignData(t *testing.T) {
	dir := t.TempDir()
	target := "server"

	if _, ok := LoadHostCache(dir, target); ok {
		t.Fatal("absent file was not a miss")
	}

	path := filepath.Join(dir, url.PathEscape(target)+".json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt cache: %v", err)
	}
	if _, ok := LoadHostCache(dir, target); ok {
		t.Fatal("corrupt file was not a miss")
	}

	stale, err := json.Marshal(map[string]any{"schema_version": 99, "sessions": nil})
	if err != nil {
		t.Fatalf("marshal stale schema: %v", err)
	}
	if err := os.WriteFile(path, stale, 0o600); err != nil {
		t.Fatalf("write stale schema: %v", err)
	}
	if _, ok := LoadHostCache(dir, target); ok {
		t.Fatal("schema mismatch was not a miss")
	}

	foreign := cacheSession("other-host")
	if err := SaveHostCache(dir, "other-host", []session.Session{foreign}); err != nil {
		t.Fatalf("SaveHostCache() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "other-host.json"))
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("copy foreign cache: %v", err)
	}
	if _, ok := LoadHostCache(dir, target); ok {
		t.Fatal("session bound to a different host was not a miss")
	}
}

func TestHostCacheMissOnInvalidSessionPayload(t *testing.T) {
	dir := t.TempDir()
	target := "server"
	invalid := cacheSession(target)
	invalid.NativeID = "not-a-uuid"
	contents, err := json.Marshal(hostCacheFile{
		SchemaVersion: cacheSchemaVersion,
		CollectedAt:   time.Now(),
		Sessions:      []session.Session{invalid},
	})
	if err != nil {
		t.Fatalf("marshal invalid payload: %v", err)
	}
	path := filepath.Join(dir, url.PathEscape(target)+".json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write invalid payload: %v", err)
	}
	if _, ok := LoadHostCache(dir, target); ok {
		t.Fatal("invalid session payload was not a miss")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app -run 'TestCachePath|TestHostCache' -v`
Expected: compile FAIL — `undefined: CachePath`, `undefined: SaveHostCache`, `undefined: LoadHostCache`, `undefined: hostCacheFile`.

- [ ] **Step 3: Write the implementation**

Create `internal/app/cache.go`:

```go
package app

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

const cacheSchemaVersion = 1

type hostCacheFile struct {
	SchemaVersion int               `json:"schema_version"`
	CollectedAt   time.Time         `json:"collected_at"`
	Sessions      []session.Session `json:"sessions"`
}

func CachePath() (string, error) {
	if cacheHome := os.Getenv("XDG_CACHE_HOME"); cacheHome != "" {
		return filepath.Join(cacheHome, "ars", "hosts"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".cache", "ars", "hosts"), nil
}

func hostCacheFilePath(dir, target string) string {
	return filepath.Join(dir, url.PathEscape(target)+".json")
}

func LoadHostCache(dir, target string) ([]session.Session, bool) {
	contents, err := os.ReadFile(hostCacheFilePath(dir, target))
	if err != nil {
		return nil, false
	}
	var file hostCacheFile
	if err := json.Unmarshal(contents, &file); err != nil {
		return nil, false
	}
	if file.SchemaVersion != cacheSchemaVersion {
		return nil, false
	}
	sessions := make([]session.Session, 0, len(file.Sessions))
	for _, item := range file.Sessions {
		if item.Host != target {
			return nil, false
		}
		bound, err := session.BindDiscovered(target, session.Discovered{
			Candidate: item.Candidate,
			Runtime:   item.Runtime,
		})
		if err != nil {
			return nil, false
		}
		sessions = append(sessions, bound)
	}
	return sessions, true
}

func SaveHostCache(dir, target string, sessions []session.Session) error {
	contents, err := json.Marshal(hostCacheFile{
		SchemaVersion: cacheSchemaVersion,
		CollectedAt:   time.Now(),
		Sessions:      sessions,
	})
	if err != nil {
		return fmt.Errorf("encode host cache: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create host cache directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, "host-*.tmp")
	if err != nil {
		return fmt.Errorf("create host cache temp file: %w", err)
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("restrict host cache permissions: %w", err)
	}
	if _, err := temp.Write(contents); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write host cache: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close host cache: %w", err)
	}
	if err := os.Rename(temp.Name(), hostCacheFilePath(dir, target)); err != nil {
		return fmt.Errorf("replace host cache: %w", err)
	}
	return nil
}
```

Note on the validation loop: sessions loaded from disk are untrusted input. Rebinding through `session.BindDiscovered` re-runs all candidate/runtime invariants, and the `item.Host != target` check rejects files copied between hosts. Any invalid entry rejects the whole file (miss) rather than filtering — a corrupted file should not be half-trusted.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app -run 'TestCachePath|TestHostCache' -v`
Expected: all 5 tests PASS.

Run: `go test ./internal/app`
Expected: PASS (no regressions).

- [ ] **Step 5: Commit**

```bash
git add internal/app/cache.go internal/app/cache_test.go
git commit -m "feat: add per-host session cache store"
```

---

### Task 2: Streaming collection with cache

**Files:**
- Create: `internal/app/stream.go`
- Create: `internal/app/stream_test.go`
- Modify: `internal/app/aggregate.go` (replace the body of `CollectHosts`, lines 37-64)

**Interfaces:**
- Consumes: `hostCollection`, `collectHost`, `failedCollection`, `mergeCollections` (all unexported, in `internal/app/aggregate.go`); Task 1's cache functions are NOT called here — the cache is injected as closures.
- Produces (used by Tasks 3 and 5):
  - `type Snapshot struct { Result Result; Stale []string; Done bool }`
  - `type HostCache struct { Load func(target string) ([]session.Session, bool); Save func(target string, sessions []session.Session) }`
  - `func CollectHostsStream(ctx context.Context, hosts []Host, workerLimit int, collector Collector, cache HostCache, emit func(Snapshot))`
  - `CollectHosts` keeps its exact current signature and final-result behavior.

Semantics of `Snapshot`:
- `Result` is the full merged view (same dedup/sort as today) of every host that has data so far — cached, live, or failed. Hosts with no cache entry and no live result yet are absent from `Result.Hosts`.
- `Stale` lists targets whose rows currently come from cache (cached and not yet live, or live collection failed). A host that failed live with no cache is not stale — it simply has an error and no rows.
- `Done` is true on the final snapshot. `emit` is always called from a single goroutine, in order.
- Emission order: one initial snapshot (cache only), then one snapshot per completed host. Zero hosts → exactly one snapshot with `Done: true`.

- [ ] **Step 1: Write the failing tests**

Create `internal/app/stream_test.go`:

```go
package app

import (
	"context"
	"errors"
	"testing"

	"github.com/baleen37/agent-remote-sessions/internal/output"
	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/runtime"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func streamSession(host, nativeID, title string) session.Session {
	value, err := session.Bind(host, session.Candidate{
		Provider:  session.Claude,
		NativeID:  nativeID,
		UpdatedAt: cacheSession(host).UpdatedAt,
		CWD:       "/work/stream",
		Title:     title,
	})
	if err != nil {
		panic(err)
	}
	return value
}

func sequentialCollector(sessionsByTarget map[string][]session.Session, errByTarget map[string]error) Collector {
	return func(_ context.Context, host Host) ([]session.Discovered, []provider.Result, runtime.Report, error) {
		if err := errByTarget[host.Target]; err != nil {
			return nil, nil, runtime.Report{}, err
		}
		discovered := make([]session.Discovered, 0, len(sessionsByTarget[host.Target]))
		for _, item := range sessionsByTarget[host.Target] {
			discovered = append(discovered, session.Discovered{Candidate: item.Candidate, Runtime: item.Runtime})
		}
		return discovered, nil, runtime.Report{Status: runtime.StatusOK}, nil
	}
}

func TestCollectHostsStreamEmitsCacheFirstThenPerHostLiveUpdates(t *testing.T) {
	hosts := []Host{{Target: "localhost", Local: true}, {Target: "server"}}
	cachedLocal := streamSession("localhost", "123e4567-e89b-42d3-a456-426614174000", "cached local")
	liveLocal := streamSession("localhost", "123e4567-e89b-42d3-a456-426614174001", "live local")
	liveServer := streamSession("server", "123e4567-e89b-42d3-a456-426614174002", "live server")

	saved := make(map[string][]session.Session)
	cache := HostCache{
		Load: func(target string) ([]session.Session, bool) {
			if target == "localhost" {
				return []session.Session{cachedLocal}, true
			}
			return nil, false
		},
		Save: func(target string, sessions []session.Session) { saved[target] = sessions },
	}
	collector := sequentialCollector(map[string][]session.Session{
		"localhost": {liveLocal},
		"server":    {liveServer},
	}, nil)

	var snapshots []Snapshot
	CollectHostsStream(context.Background(), hosts, 1, collector, cache, func(snapshot Snapshot) {
		snapshots = append(snapshots, snapshot)
	})

	if len(snapshots) != 3 {
		t.Fatalf("snapshot count = %d, want 3: %#v", len(snapshots), snapshots)
	}
	first := snapshots[0]
	if first.Done || len(first.Result.Sessions) != 1 || first.Result.Sessions[0].Title != "cached local" {
		t.Fatalf("initial snapshot = %#v", first)
	}
	if len(first.Stale) != 1 || first.Stale[0] != "localhost" {
		t.Fatalf("initial stale = %#v", first.Stale)
	}
	last := snapshots[2]
	if !last.Done || len(last.Result.Sessions) != 2 || len(last.Stale) != 0 {
		t.Fatalf("final snapshot = %#v", last)
	}
	if len(saved["localhost"]) != 1 || saved["localhost"][0].Title != "live local" {
		t.Fatalf("localhost cache save = %#v", saved["localhost"])
	}
	if len(saved["server"]) != 1 || saved["server"][0].Title != "live server" {
		t.Fatalf("server cache save = %#v", saved["server"])
	}
}

func TestCollectHostsStreamKeepsCachedRowsAndStaleMarkOnFailure(t *testing.T) {
	hosts := []Host{{Target: "server"}}
	cached := streamSession("server", "123e4567-e89b-42d3-a456-426614174000", "cached row")
	saves := 0
	cache := HostCache{
		Load: func(string) ([]session.Session, bool) { return []session.Session{cached}, true },
		Save: func(string, []session.Session) { saves++ },
	}
	collector := sequentialCollector(nil, map[string]error{"server": errors.New("ssh boom")})

	var snapshots []Snapshot
	CollectHostsStream(context.Background(), hosts, 1, collector, cache, func(snapshot Snapshot) {
		snapshots = append(snapshots, snapshot)
	})

	last := snapshots[len(snapshots)-1]
	if !last.Done {
		t.Fatalf("final snapshot not done: %#v", last)
	}
	if len(last.Result.Sessions) != 1 || last.Result.Sessions[0].Title != "cached row" {
		t.Fatalf("cached rows dropped on failure: %#v", last.Result.Sessions)
	}
	if len(last.Stale) != 1 || last.Stale[0] != "server" {
		t.Fatalf("failed host lost stale mark: %#v", last.Stale)
	}
	if len(last.Result.Errors) != 1 || last.Result.Errors[0].Code != "ssh_failed" {
		t.Fatalf("failure not reported: %#v", last.Result.Errors)
	}
	if len(last.Result.Hosts) != 1 || last.Result.Hosts[0].Status != output.HostStatusError {
		t.Fatalf("host status = %#v", last.Result.Hosts)
	}
	if saves != 0 {
		t.Fatalf("failed collection wrote cache %d times", saves)
	}
}

func TestCollectHostsStreamWithoutCacheOmitsPendingHosts(t *testing.T) {
	hosts := []Host{{Target: "server"}}
	live := streamSession("server", "123e4567-e89b-42d3-a456-426614174000", "live row")
	collector := sequentialCollector(map[string][]session.Session{"server": {live}}, nil)

	var snapshots []Snapshot
	CollectHostsStream(context.Background(), hosts, 1, collector, HostCache{}, func(snapshot Snapshot) {
		snapshots = append(snapshots, snapshot)
	})

	if len(snapshots) != 2 {
		t.Fatalf("snapshot count = %d, want 2", len(snapshots))
	}
	if len(snapshots[0].Result.Hosts) != 0 || len(snapshots[0].Stale) != 0 || snapshots[0].Done {
		t.Fatalf("initial snapshot leaked pending host: %#v", snapshots[0])
	}
	if !snapshots[1].Done || len(snapshots[1].Result.Sessions) != 1 {
		t.Fatalf("final snapshot = %#v", snapshots[1])
	}
}

func TestCollectHostsStreamZeroHostsEmitsSingleDoneSnapshot(t *testing.T) {
	var snapshots []Snapshot
	CollectHostsStream(context.Background(), nil, 1, sequentialCollector(nil, nil), HostCache{}, func(snapshot Snapshot) {
		snapshots = append(snapshots, snapshot)
	})
	if len(snapshots) != 1 || !snapshots[0].Done {
		t.Fatalf("snapshots = %#v", snapshots)
	}
}

func TestCollectHostsStreamInvalidWorkerLimitFailsAllHostsOnce(t *testing.T) {
	hosts := []Host{{Target: "server"}}
	var snapshots []Snapshot
	CollectHostsStream(context.Background(), hosts, 0, sequentialCollector(nil, nil), HostCache{}, func(snapshot Snapshot) {
		snapshots = append(snapshots, snapshot)
	})
	if len(snapshots) != 1 || !snapshots[0].Done {
		t.Fatalf("snapshots = %#v", snapshots)
	}
	if len(snapshots[0].Result.Errors) != 1 || snapshots[0].Result.Errors[0].Code != "resource_limit" {
		t.Fatalf("errors = %#v", snapshots[0].Result.Errors)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app -run TestCollectHostsStream -v`
Expected: compile FAIL — `undefined: CollectHostsStream`, `undefined: HostCache`, `undefined: Snapshot`.

- [ ] **Step 3: Write the implementation**

Create `internal/app/stream.go`:

```go
package app

import (
	"context"
	"sync"

	"github.com/baleen37/agent-remote-sessions/internal/output"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

type Snapshot struct {
	Result Result
	Stale  []string
	Done   bool
}

type HostCache struct {
	Load func(target string) ([]session.Session, bool)
	Save func(target string, sessions []session.Session)
}

func CollectHostsStream(ctx context.Context, hosts []Host, workerLimit int, collector Collector, cache HostCache, emit func(Snapshot)) {
	collections := make([]hostCollection, len(hosts))
	hasData := make([]bool, len(hosts))
	stale := make([]bool, len(hosts))

	if workerLimit <= 0 || collector == nil {
		for index, host := range hosts {
			collections[index] = failedCollection(host.Target, "resource_limit", "Collector resource limit exceeded")
		}
		emit(Snapshot{Result: mergeCollections(collections), Stale: []string{}, Done: true})
		return
	}

	for index, host := range hosts {
		if cache.Load == nil {
			break
		}
		sessions, ok := cache.Load(host.Target)
		if !ok {
			continue
		}
		collections[index] = hostCollection{
			host:     output.HostResult{Target: host.Target, Status: output.HostOK},
			sessions: sessions,
		}
		hasData[index] = true
		stale[index] = true
	}

	snapshot := func(done bool) Snapshot {
		present := make([]hostCollection, 0, len(collections))
		staleTargets := make([]string, 0, len(hosts))
		for index := range collections {
			if !hasData[index] {
				continue
			}
			present = append(present, collections[index])
			if stale[index] {
				staleTargets = append(staleTargets, hosts[index].Target)
			}
		}
		return Snapshot{Result: mergeCollections(present), Stale: staleTargets, Done: done}
	}

	emit(snapshot(len(hosts) == 0))
	if len(hosts) == 0 {
		return
	}

	type completion struct {
		index      int
		collection hostCollection
	}
	completions := make(chan completion)
	jobs := make(chan int)
	workers := min(workerLimit, len(hosts))
	var waitGroup sync.WaitGroup
	waitGroup.Add(workers)
	for range workers {
		go func() {
			defer waitGroup.Done()
			for index := range jobs {
				completions <- completion{index: index, collection: collectHost(ctx, hosts[index], collector)}
			}
		}()
	}
	go func() {
		for index := range hosts {
			jobs <- index
		}
		close(jobs)
		waitGroup.Wait()
		close(completions)
	}()

	remaining := len(hosts)
	for done := range completions {
		collection := done.collection
		if collection.err == nil {
			if cache.Save != nil {
				cache.Save(hosts[done.index].Target, collection.sessions)
			}
			stale[done.index] = false
		} else if stale[done.index] {
			collection.sessions = collections[done.index].sessions
		}
		collections[done.index] = collection
		hasData[done.index] = true
		remaining--
		emit(snapshot(remaining == 0))
	}
}
```

In `internal/app/aggregate.go`, replace the entire body of `CollectHosts` (currently lines 37-64, the worker-pool code) with a delegation — the pool now lives in `CollectHostsStream`:

```go
func CollectHosts(ctx context.Context, hosts []Host, workerLimit int, collector Collector) Result {
	var last Snapshot
	CollectHostsStream(ctx, hosts, workerLimit, collector, HostCache{}, func(update Snapshot) {
		last = update
	})
	return last.Result
}
```

After this replacement `"sync"` may become unused in `aggregate.go` — remove it from that file's imports if the compiler flags it.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app -v -run 'TestCollectHostsStream|TestCollectHosts'`
Expected: new stream tests PASS and every pre-existing `CollectHosts` test in `aggregate_test.go` still PASSES (the delegation must not change final-result behavior).

Run: `go test -race ./internal/app`
Expected: PASS (worker pool + emit ordering is concurrency-sensitive; `-race` is required here).

- [ ] **Step 5: Commit**

```bash
git add internal/app/stream.go internal/app/stream_test.go internal/app/aggregate.go
git commit -m "feat: stream per-host collection snapshots with cache"
```

---

### Task 3: TUI consumes streaming updates

**Files:**
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/pty_integration_test.go:154` (Collect closure signature)

**Interfaces:**
- Consumes: nothing from app — tui must not import `internal/app` (enforced by `internal/tui/import_boundary_test.go`). It defines its own mirror types.
- Produces (used by Tasks 4 and 5):
  - `type Update struct { Result Result; Stale []string; Done bool }`
  - `Dependencies.Collect` becomes `func(context.Context) <-chan Update` (was `func(context.Context) Result`)
  - model field `stale map[string]struct{}` — targets currently rendered from cache (read by `renderRow` in Task 4)
  - model field `cancelCollect context.CancelFunc`
  - test helper `func staticCollect(result Result) func(context.Context) <-chan Update`
  - `collectDoneMsg` is deleted; the new message is `collectUpdateMsg{generation uint64; update Update; channel <-chan Update}`

Behavior:
- `newModel` starts the first collection stream itself (deriving a cancellable child context) so `Init()` can stay a pure value method; `Init()` returns the stored first wait command.
- Each `collectUpdateMsg` applies `Result` + `Stale`, clears `collecting` only when `Done`, and re-issues a wait command on the same channel. A closed channel yields a `nil` message and the loop ends.
- `restartCollection` cancels the previous stream context (`cancelCollect`) before starting a new one, so an abandoned generation's SSH work is torn down instead of leaking.
- Generation guard unchanged: mismatched `collectUpdateMsg` is dropped and its channel is not re-waited.

- [ ] **Step 1: Update the tests to the streaming contract**

In `internal/tui/model_test.go`, apply all of the following.

Add the helper (below `twoSessions`):

```go
func staticCollect(result Result) func(context.Context) <-chan Update {
	return func(context.Context) <-chan Update {
		channel := make(chan Update, 1)
		channel <- Update{Result: result, Done: true}
		close(channel)
		return channel
	}
}
```

Replace `readyModel` with:

```go
func readyModel() model {
	result := Result{Sessions: twoSessions()}
	deps := Dependencies{
		Collect:     staticCollect(result),
		Attach:      func(context.Context, session.Session) (ExecCommand, error) { return &fakeExecCommand{}, nil },
		LocalTarget: "localhost",
		Now:         func() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) },
		NoColor:     true,
	}
	value := newModel(context.Background(), deps)
	message, ok := value.Init()().(collectUpdateMsg)
	if !ok {
		panic("readyModel: Init did not produce collectUpdateMsg")
	}
	value, _ = updateModel(value, message)
	return value
}
```

In `TestModelInitialCollectionNavigatesFiltersAndAttaches`, replace the `Collect` line and the Init/message assertions:

```go
	deps := Dependencies{
		Collect: staticCollect(result),
		Attach: func(_ context.Context, item session.Session) (ExecCommand, error) {
			attached = item
			return &fakeExecCommand{}, nil
		},
		LocalTarget: "localhost",
		Now:         func() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) },
	}
	model := newModel(context.Background(), deps)
	command := model.Init()
	if command == nil || !model.collecting || model.generation != 1 {
		t.Fatalf("Init() collecting=%t generation=%d command=%v", model.collecting, model.generation, command)
	}
	message, ok := command().(collectUpdateMsg)
	if !ok || message.generation != 1 || !message.update.Done || len(message.update.Result.Sessions) != 2 {
		t.Fatalf("Init command message = %#v", message)
	}
```

(the remainder of that test is unchanged — `updateModel(model, message)` still applies.)

In `TestModelRefreshCoalescesAndRejectsStaleGenerations`, replace the two `collectDoneMsg` lines:

```go
	stale := Result{Sessions: []session.Session{twoSessions()[1]}}
	model, command := updateModel(model, collectUpdateMsg{generation: 1, update: Update{Result: stale, Done: true}})
	if command != nil || !model.collecting || len(model.result.Sessions) != 2 {
		t.Fatalf("stale collection changed model: %#v", model)
	}
	fresh := Result{Sessions: []session.Session{twoSessions()[0]}}
	model, _ = updateModel(model, collectUpdateMsg{generation: 2, update: Update{Result: fresh, Done: true}})
```

In `TestModelRefreshPreservesCanonicalSelection`, replace the `collectDoneMsg` line:

```go
	model, _ = updateModel(model, collectUpdateMsg{generation: 2, update: Update{Result: result, Done: true}})
```

Replace `TestModelAttachCompletionStoresBoundedStatusAndCollectsExactlyOnce` body's collect counter and final assertion:

```go
	collects := 0
	model := readyModel()
	model.deps.Collect = func(context.Context) <-chan Update {
		collects++
		channel := make(chan Update, 1)
		channel <- Update{Done: true}
		close(channel)
		return channel
	}
	want := errors.New(strings.Repeat("attach failed ", 100))
	model, command := updateModel(model, attachDoneMsg{err: want})
	if command == nil || !model.collecting || model.generation != 2 {
		t.Fatalf("attach completion command=%v collecting=%t generation=%d", command, model.collecting, model.generation)
	}
	if model.status == "" || len(model.status) > maxStatusBytes {
		t.Fatalf("bounded status length=%d status=%q", len(model.status), model.status)
	}
	message, ok := command().(collectUpdateMsg)
	if !ok || message.generation != 2 || collects != 1 {
		t.Fatalf("refresh message=%#v collects=%d", message, collects)
	}
```

In `TestModelAttachCompletionSupersedesCollectionInFlight`, replace the message assertion:

```go
	message, ok := command().(collectUpdateMsg)
	if !ok || message.generation != 3 {
		t.Fatalf("refresh message = %#v", message)
	}
```

Add two new tests:

```go
func TestModelAppliesIncrementalUpdatesAndStaleHostsUntilDone(t *testing.T) {
	items := twoSessions()
	model := readyModel()
	model.collecting = true
	model.generation = 2

	channel := make(chan Update, 2)
	partial := Update{Result: Result{Sessions: items}, Stale: []string{"server"}}
	model, command := updateModel(model, collectUpdateMsg{generation: 2, update: partial, channel: channel})
	if command == nil || !model.collecting {
		t.Fatalf("partial update command=%v collecting=%t", command, model.collecting)
	}
	if _, ok := model.stale["server"]; !ok || len(model.stale) != 1 {
		t.Fatalf("stale set = %#v", model.stale)
	}

	final := Update{Result: Result{Sessions: items[:1]}, Done: true}
	model, _ = updateModel(model, collectUpdateMsg{generation: 2, update: final, channel: channel})
	if model.collecting || len(model.stale) != 0 || len(model.result.Sessions) != 1 {
		t.Fatalf("final update not applied: collecting=%t stale=%#v", model.collecting, model.stale)
	}
}

func TestModelRestartCancelsPreviousCollectionContext(t *testing.T) {
	model := readyModel()
	var firstCtx context.Context
	model.deps.Collect = func(ctx context.Context) <-chan Update {
		if firstCtx == nil {
			firstCtx = ctx
		}
		channel := make(chan Update)
		go func() {
			<-ctx.Done()
			close(channel)
		}()
		return channel
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	if firstCtx == nil {
		t.Fatal("refresh did not start a collection")
	}
	model.collecting = false
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	select {
	case <-firstCtx.Done():
	default:
		t.Fatal("second refresh did not cancel the first collection context")
	}
	_ = model
}
```

In `internal/tui/pty_integration_test.go`, replace the `Collect` closure (lines 154-166) — the existing body moves unchanged into an inner `collect` function:

```go
		Collect: func(ctx context.Context) <-chan Update {
			collect := func() Result {
				runtimes, report := arsruntime.Inspect(ctx, runner, []session.Candidate{candidate})
				state := runtimes[arsruntime.Key(string(candidate.Provider), candidate.NativeID)]
				item, bindErr := session.BindDiscovered("localhost", session.Discovered{Candidate: candidate, Runtime: state})
				if bindErr != nil {
					return Result{Errors: []output.HostError{{Host: "localhost", Code: "protocol_error", Message: bindErr.Error()}}}
				}
				result := Result{Hosts: []output.HostResult{{Target: "localhost", Status: output.HostOK}}, Sessions: []session.Session{item}}
				if report.Status == arsruntime.StatusUnavailable {
					result.Warnings = []output.HostError{{Host: "localhost", Code: report.ErrorCode, Message: "Runtime inspection unavailable"}}
				}
				return result
			}
			channel := make(chan Update, 1)
			channel <- Update{Result: collect(), Done: true}
			close(channel)
			return channel
		},
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui -run TestModel -v`
Expected: compile FAIL — `undefined: Update`, `undefined: collectUpdateMsg`, and `Dependencies.Collect` type mismatch.

- [ ] **Step 3: Write the implementation**

In `internal/tui/model.go`:

Add the `Update` type next to `Result`:

```go
type Update struct {
	Result Result
	Stale  []string
	Done   bool
}
```

Change `Dependencies.Collect`:

```go
type Dependencies struct {
	Collect     func(context.Context) <-chan Update
	Attach      func(context.Context, session.Session) (ExecCommand, error)
	LocalTarget string
	Now         func() time.Time
	NoColor     bool
}
```

Replace `collectDoneMsg` with:

```go
type collectUpdateMsg struct {
	generation uint64
	update     Update
	channel    <-chan Update
}
```

Extend the model struct — add three fields after `generation uint64`:

```go
	stale          map[string]struct{}
	cancelCollect  context.CancelFunc
	initialCollect tea.Cmd
```

Replace `newModel` and `Init`:

```go
func newModel(ctx context.Context, deps Dependencies) model {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	_, noColor := os.LookupEnv("NO_COLOR")
	value := model{
		ctx:        ctx,
		deps:       deps,
		collecting: true,
		generation: 1,
		noColor:    deps.NoColor || noColor,
	}
	collectCtx, cancel := context.WithCancel(ctx)
	value.cancelCollect = cancel
	value.initialCollect = waitForUpdate(value.generation, deps.Collect(collectCtx))
	return value
}

func (value model) Init() tea.Cmd {
	return value.initialCollect
}
```

Replace the `collectDoneMsg` case in `updateModel` with:

```go
	case collectUpdateMsg:
		if message.generation != value.generation {
			return value, nil
		}
		value.result = message.update.Result
		value.stale = make(map[string]struct{}, len(message.update.Stale))
		for _, target := range message.update.Stale {
			value.stale[target] = struct{}{}
		}
		if message.update.Done {
			value.collecting = false
		}
		value.refreshVisible()
		return value, waitForUpdate(message.generation, message.channel)
```

Replace `restartCollection` and `collectCommand` with:

```go
func (value model) restartCollection() (model, tea.Cmd) {
	if value.cancelCollect != nil {
		value.cancelCollect()
	}
	collectCtx, cancel := context.WithCancel(value.ctx)
	value.cancelCollect = cancel
	value.generation++
	value.collecting = true
	return value, waitForUpdate(value.generation, value.deps.Collect(collectCtx))
}

func waitForUpdate(generation uint64, channel <-chan Update) tea.Cmd {
	return func() tea.Msg {
		update, ok := <-channel
		if !ok {
			return nil
		}
		return collectUpdateMsg{generation: generation, update: update, channel: channel}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/tui`
Expected: PASS, including the pty integration test.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/model_test.go internal/tui/pty_integration_test.go
git commit -m "feat: stream collection updates into the tui model"
```

---

### Task 4: Stale rows render a dim cached marker

**Files:**
- Modify: `internal/tui/view.go` (`renderRow`, lines 150-187)
- Modify: `internal/tui/view_test.go`

**Interfaces:**
- Consumes: `model.stale map[string]struct{}` from Task 3; existing `stateText` faint styling (`session.RuntimeSaved` renders faint).
- Produces: rows for stale hosts gain a trailing `cached` column, rendered faint when colors are on.

- [ ] **Step 1: Write the failing test**

Add to `internal/tui/view_test.go`:

```go
func TestViewMarksStaleHostRowsAsCached(t *testing.T) {
	model := readyModel()
	model.width = 120
	model.stale = map[string]struct{}{"server": {}}
	model.refreshVisible()

	content := ansi.Strip(model.View().Content)
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		switch {
		case strings.Contains(line, "API repair"):
			if !strings.HasSuffix(strings.TrimRight(line, " "), "cached") {
				t.Fatalf("stale row missing cached marker: %q", line)
			}
		case strings.Contains(line, "connection check"):
			if strings.Contains(line, "cached") {
				t.Fatalf("fresh row has cached marker: %q", line)
			}
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui -run TestViewMarksStaleHostRowsAsCached -v`
Expected: FAIL with `stale row missing cached marker`.

- [ ] **Step 3: Write the implementation**

In `renderRow` (`internal/tui/view.go`), replace this block:

```go
	fields = append(fields, runtime, activityAge(value.deps.Now(), item.UpdatedAt))

	truncateFlexibleFields(fields, flexible, width-lipgloss.Width(prefix))
	fields[0] = value.stateText(fields[0], item.Runtime.State)
	fields[len(fields)-2] = value.stateText(fields[len(fields)-2], item.Runtime.State)
```

with:

```go
	fields = append(fields, runtime, activityAge(value.deps.Now(), item.UpdatedAt))
	runtimeIndex := len(fields) - 2
	_, isStale := value.stale[item.Host]
	if isStale {
		fields = append(fields, "cached")
	}

	truncateFlexibleFields(fields, flexible, width-lipgloss.Width(prefix))
	fields[0] = value.stateText(fields[0], item.Runtime.State)
	fields[runtimeIndex] = value.stateText(fields[runtimeIndex], item.Runtime.State)
	if isStale {
		fields[len(fields)-1] = value.stateText(fields[len(fields)-1], session.RuntimeSaved)
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui`
Expected: PASS (new test plus all existing view tests — the marker must not break width/height bounding tests).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/view.go internal/tui/view_test.go
git commit -m "feat: mark cached session rows in the tui"
```

---

### Task 5: Wire cache into main, update README, full verification

**Files:**
- Modify: `cmd/ars/main.go` (the `RunInteractive` / `tui.Dependencies` wiring, lines 51-93)
- Modify: `README.md` (lines 127-129, the "does not poll, watch, or cache" sentence)

**Interfaces:**
- Consumes: `app.CachePath`, `app.LoadHostCache`, `app.SaveHostCache` (Task 1); `app.CollectHostsStream`, `app.HostCache`, `app.Snapshot` (Task 2); `tui.Update` (Task 3).
- Produces: the shipped `ars` binary behavior. `ars list --json` stays live-only (it still calls `app.CollectHosts`, which passes an empty `HostCache`).

- [ ] **Step 1: Wire the streaming collect with cache in `cmd/ars/main.go`**

Above the `dependencies := app.Dependencies{...}` literal, build the cache (after the `collectHosts` closure):

```go
	hostCache := app.HostCache{}
	if cacheDir, err := app.CachePath(); err == nil {
		hostCache = app.HostCache{
			Load: func(target string) ([]session.Session, bool) {
				return app.LoadHostCache(cacheDir, target)
			},
			Save: func(target string, sessions []session.Session) {
				_ = app.SaveHostCache(cacheDir, target, sessions)
			},
		}
	}
```

(A cache-path failure degrades to live-only collection; it must not abort the TUI.)

Inside `RunInteractive`, replace the current `Collect` closure (lines 61-69) with:

```go
				Collect: func(ctx context.Context) <-chan tui.Update {
					updates := make(chan tui.Update)
					go func() {
						defer close(updates)
						app.CollectHostsStream(ctx, hosts, 4, collectHost, hostCache, func(snapshot app.Snapshot) {
							update := tui.Update{
								Result: tui.Result{
									Hosts:    snapshot.Result.Hosts,
									Sessions: snapshot.Result.Sessions,
									Errors:   snapshot.Result.Errors,
									Warnings: snapshot.Result.Warnings,
								},
								Stale: snapshot.Stale,
								Done:  snapshot.Done,
							}
							select {
							case updates <- update:
							case <-ctx.Done():
								return
							}
						})
					}()
					return updates
				},
```

The `select` on `ctx.Done()` matters: when the TUI restarts a collection it cancels this context, and the producer goroutine must exit instead of blocking on a send nobody reads.

- [ ] **Step 2: Verify the binary builds and the full suite passes**

```bash
go run ./cmd/ars-build --assets-only
go build ./...
go vet ./...
go test -race ./...
```

Expected: all PASS. (`go test ./...` includes the pty and e2e tests; they must stay green.)

- [ ] **Step 3: Update README**

In `README.md`, replace:

```
The screen collects at startup, on `r`, and after attach returns. It does not
poll, watch, or cache in the background. Canonical host/provider/native-ID data,
never rendered row text, determines the attach command.
```

with:

```
The screen collects at startup, on `r`, and after attach returns. Rows appear
immediately from the last collection, cached per host under
`${XDG_CACHE_HOME:-~/.cache}/ars/hosts/`, marked `cached` until that host's
live refresh lands; each host updates independently, so one slow peer never
delays the rest. It does not poll, watch, or collect in the background, and
peers still store nothing. Canonical host/provider/native-ID data, never
rendered row text, determines the attach command.
```

- [ ] **Step 4: Manual smoke test**

```bash
go run ./cmd/ars-build
./ars
```

Expected: first run collects live (no cache yet) and exits normally on `q`. Second `./ars` run shows the previous list instantly with `cached` markers that clear once collection lands; `ls ~/.cache/ars/hosts/` shows one `localhost.json` (mode `-rw-------`).

- [ ] **Step 5: Commit**

```bash
git add cmd/ars/main.go README.md
git commit -m "feat: serve session list from per-host cache with live refresh"
```
