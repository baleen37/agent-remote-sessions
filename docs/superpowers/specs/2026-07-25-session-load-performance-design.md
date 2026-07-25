# Session Load Performance Design

Date: 2026-07-25
Status: Approved

## Problem

`ars` already renders cached rows quickly and refreshes hosts independently,
but every live collection still reparses every Claude and Codex JSONL file.
On the measured development machine, localhost contains:

- 1,547 Claude files totaling 570,387,899 bytes
- 1,135 Codex files totaling 725,319,505 bytes

The current collector took 19.75 seconds with a cold filesystem cache and
5.31–5.97 seconds when warm. Isolated warm measurements were 1.61 seconds for
Claude and 3.41 seconds for Codex, showing that the sequential provider loop
alone makes the combined refresh unnecessarily slow.

The initial TUI frame also renders `no sessions yet` before the asynchronous
cache snapshot arrives. This is briefly visible during real use and describes
an empty result even though collection has not produced a result yet.

## Goal

Reduce localhost and remote session refresh latency without omitting old
sessions or weakening provider validation, and make the initial loading state
truthful.

On the same machine and dataset used for the baseline:

- the median of three warm localhost collector runs must improve by at least
  30% from the 5.31-second baseline
- cached session rows must remain visible within one second of TUI startup
- the initial empty frame must say that sessions are loading, not that no
  sessions exist
- the optimized collector must return the same session identities and
  provider diagnostic outcomes as the current implementation

## Non-Goals

- Limiting discovery to recent histories or hiding additional old sessions
- Adding TTLs, polling, a daemon, or filesystem watching
- Adding persistent per-file metadata caches
- Changing the ARS/2 protocol, public JSON schema, host cache schema, or cache
  path
- Persisting any new data on remote peers
- Changing update-check, attach, tmux, search, sorting, or grouping behavior

## Design

### 1. Discover providers concurrently

`provider.DiscoverAll` will start the two validated built-in adapters
concurrently. Each adapter writes to a result slot matching registry order.
After both complete, the existing candidate validation, combined session
limit, provider ordering, and deterministic sorting run in one goroutine.

This preserves the current public behavior while reducing elapsed discovery
time from the sum of Claude and Codex work toward the slower provider's time.
Both adapters receive the same context, so cancellation and existing bounded
directory reads continue to apply. The fixed two-provider registry bounds
concurrency without a new worker-pool abstraction or configuration.

### 2. Avoid copying irrelevant JSON payloads

Every JSONL line will still be read and JSON-decoded so corrupt input remains
observable.

For Codex, the first decode will extract only the top-level `type`. The
`payload` will be decoded only for:

- every `session_meta`, preserving duplicate and invalid metadata detection
- `event_msg` records until the first usable user title is found

Unknown fields are still parsed and validated by `encoding/json`, but large
payloads on irrelevant records are no longer copied into `json.RawMessage`.

For Claude, the first decode will extract the metadata fields used on every
record without copying `message`. The `message` field will be decoded only for
eligible user records while prompt-title discovery still needs a title.
Native titles, CWD updates, internal/sidechain exclusion, mixed session IDs,
invalid records, and scanner limits continue to be evaluated across the whole
file.

No parser will stop early. This keeps exhaustive discovery and strict
validation while reducing allocation and decode work.

### 3. Render an honest initial state

The TUI model already starts collection immediately, and the cache snapshot
usually arrives on the next Bubble Tea message. While collection is active and
there are no rows or completed host results yet, the empty-state copy will be
`loading sessions…`.

After a completed healthy collection with no sessions, the existing true empty
state remains. Cached rows remain attachable immediately, and the existing
host-loading indicator and per-host streaming updates remain unchanged.

This is a view-state correction, not a new preload path or collection
abstraction.

## Error Handling and Invariants

- Adapter concurrency must not change provider result order.
- A malformed result or candidate still fails `DiscoverAll` exactly as today.
- Codex still rejects multiple decodable `session_meta` records, including
  records whose JSON uses whitespace or escaped strings.
- Claude still fails closed on mixed or invalid explicit session IDs.
- All input lines still pass through `encoding/json`; malformed lines still
  contribute the same diagnostic severity.
- Context cancellation remains checked by directory traversal and returns
  without waiting for unrelated new work.
- Scanner line-size, discovered-session, protocol, host-worker, and SSH timeout
  bounds remain unchanged.

## Testing

Provider tests will prove:

- both adapters begin discovery before either is released
- output ordering remains Claude then Codex regardless of completion order
- invalid adapter results and combined limits retain their behavior
- Codex duplicate metadata, escaped type values, corrupt records, and title
  extraction remain unchanged
- Claude mixed IDs, corrupt records, native/prompt title precedence, and
  internal-session exclusion remain unchanged

TUI tests will prove:

- an initial empty collecting model renders `loading sessions…`
- a completed healthy empty model renders the existing empty-session guidance
- cached rows still render immediately and stay selectable during refresh

Verification will include focused tests, full `go test ./...`, `go test -race
./...`, `go vet ./...`, a production build, three warm collector measurements,
identity/diagnostic comparison against the baseline implementation, and a real
PTY run against localhost plus the configured SSH peer.

