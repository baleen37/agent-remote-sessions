# Progressive Session Loading Design

Date: 2026-07-25
Status: Approved

## Problem

`ars` already renders cached rows quickly and refreshes hosts independently,
but every live collection still reparses every Claude and Codex JSONL file
before it can return any fresh data. On the measured development machine,
localhost contains:

- 1,547 Claude files totaling 570,387,899 bytes
- 1,135 Codex files totaling 725,319,505 bytes

The current collector took 19.75 seconds with a cold filesystem cache and
5.31–5.97 seconds when warm. Isolated warm measurements were 1.61 seconds for
Claude and 3.41 seconds for Codex, showing that the sequential provider loop
alone makes the combined refresh unnecessarily slow.

Only 283 files totaling about 148 MB were modified in the last seven days.
That is roughly 11% of the bytes in the full inventory and matches the TUI's
existing seven-day window for showing recent saved sessions.

The initial TUI frame also renders `no sessions yet` before the asynchronous
cache snapshot arrives. Incremental updates can create a second problem: if
each partial result is immediately sorted into the tree while the user is
navigating, groups move and the screen feels unstable.

## Goal

Show useful sessions immediately, refresh the sessions most likely to be
visible first, and complete exhaustive discovery in the background without
weakening validation or making the TUI move under the user's cursor.

On the same machine and dataset used for the baseline:

- cached session rows must remain visible within one second of TUI startup
- the recent live result must arrive materially before full discovery, with a
  target of 1.5 seconds for the median of three warm localhost runs
- the median of three warm full localhost runs must improve by at least 30%
  from the 5.31-second baseline
- the final result must contain the same session identities and provider
  diagnostic outcomes as the current exhaustive implementation
- navigation, search, and group operations must not be interrupted by
  intermediate row reordering

## User-Facing Contract

Progressive loading is an implementation detail. The UI must not expose cache
provenance or phase labels such as `cached`, `refreshing recent`, `finishing
history`, or `complete`. Users only see:

- available rows immediately
- a generic `refreshing` spinner while fresher data is being collected
- the final rows after interaction-safe updates
- an actionable error if refresh cannot finish

The UI does not explain cache provenance or collection phases. Search and
attach continue to operate on whatever rows are currently visible, and newer
results appear automatically without requiring an apply key.

## Non-Goals

- Limiting discovery to recent histories or omitting old sessions from the
  eventual result
- Adding TTLs, polling, a daemon, or filesystem watching
- Adding persistent per-file metadata caches
- Changing the public JSON schema, host cache schema, or cache path
- Persisting any new data on remote peers
- Changing update-check, attach, tmux, search matching, sorting, or grouping
  semantics

## Design

### 1. Reuse the existing full host cache

Startup, manual refresh, and attach-return continue to load the existing
per-host cache first. The cache contains the last successful exhaustive host
result, so it can populate both new and old sessions without waiting for live
I/O.

Only a successful exhaustive result replaces the cache. A recent-first
snapshot is never persisted by itself, preventing a failed refresh from
shrinking a previously complete cache.

The cache schema and validation remain unchanged.

### 2. Enumerate once, discover in two phases

Each provider enumerates its metadata tree once and records the path, size, and
modification time of every eligible regular JSONL file. It partitions that
bounded inventory at the same seven-day boundary used by the TUI:

1. parse files modified within the last seven days
2. emit a recent-first snapshot
3. parse the remaining files
4. merge both partitions and emit the exhaustive result

Files are parsed at most once per refresh. The second phase continues from the
same enumeration instead of starting another scan.

Claude and Codex run concurrently in each phase. Results are stored in fixed
registry-order slots, and the existing candidate validation, combined session
limit, provider order, and deterministic sorting run after the phase joins.
The fixed two-provider registry bounds concurrency without new configuration
or a general worker-pool abstraction.

The full phase evaluates every eligible file and is authoritative. Recent
snapshots do not claim final provider diagnostics because older files may still
contain stronger errors.

### 3. Avoid copying irrelevant JSON payloads

Every JSONL line is still read and JSON-decoded so corrupt input remains
observable.

For Codex, the first decode extracts only the top-level `type`. The `payload`
is decoded only for:

- every `session_meta`, preserving duplicate and invalid metadata detection
- `event_msg` records until the first usable user title is found

Unknown fields are still parsed and validated by `encoding/json`, but large
payloads on irrelevant records are no longer copied into `json.RawMessage`.

For Claude, the first decode extracts the metadata fields used on every record
without copying `message`. The `message` field is decoded only for eligible
user records while prompt-title discovery still needs a title. Native titles,
CWD updates, internal/sidechain exclusion, mixed session IDs, invalid records,
and scanner limits continue to be evaluated across the whole file.

No parser stops early. This preserves exhaustive discovery and strict
validation while reducing allocation and decode work.

### 4. Stream localhost and SSH peers through one collection

Local discovery exposes recent-first and exhaustive callbacks from the same
provider scan.

The private collector protocol advances from ARS/2 to a version-matched ARS/3
stream. A single collector process emits:

1. one recent-first snapshot
2. one exhaustive snapshot with final provider and runtime summaries

The local binary decodes the stream while the SSH process is still running,
rather than buffering all stdout until process exit. The existing probe and
collector upload remain one invocation each; progressive loading adds no SSH
round trip and no peer-side state.

Protocol limits apply to the complete stream, not independently to each
snapshot. Both snapshots are nonce-bound, strictly framed, bounded, and
validated before becoming host updates. A malformed or truncated stream fails
closed.

### 5. Merge recent data without discarding cached history

For a host with cached data, the recent-first snapshot overlays sessions by
canonical `(host, provider, native ID)` identity:

- a matching session receives fresher title, CWD, timestamp, and runtime state
- a new session is added
- cached sessions absent from the recent snapshot remain until the exhaustive
  result arrives

For a host without cache, the recent-first snapshot is immediately usable by
search and attach. The exhaustive snapshot later replaces the whole host
collection and is atomically cached.

If the recent phase succeeds but exhaustive discovery fails, the overlaid
cache or recent rows remain usable. The incomplete result is not written to
cache, and the host reports the existing actionable collection failure.

### 6. Coalesce UI updates around interaction

The application publishes at most one initial cache snapshot and two live
snapshots per host. It never emits per-file or per-session updates.

Before the first user interaction, snapshots apply immediately so startup
benefits from recent-first results. Once the user presses a navigation, search,
fold, filter, pin, or show-all key during collection:

- incoming snapshots replace one pending snapshot instead of rebuilding rows
  immediately
- a short idle timer starts after the latest interaction
- when the timer fires, only the newest pending snapshot applies
- a later exhaustive snapshot supersedes an unapplied recent snapshot

The existing canonical row reference preserves the selected session or group.
If the selected session disappears in the exhaustive result, the existing
same-group and nearest-row fallback applies. This produces at most one
controlled update after the user pauses instead of repeated movement while
keys are being pressed.

The header shows only a generic spinner with `refreshing`; it does not expose
host phases or cache state. While there are no rows and collection is active,
the body says `loading sessions…`. A completed healthy empty collection keeps
the existing new-user guidance.

## Error Handling and Invariants

- Adapter and phase concurrency must not change provider result order.
- A malformed result or candidate still fails exhaustive discovery exactly as
  today.
- Codex still rejects multiple decodable `session_meta` records, including
  records whose JSON uses whitespace or escaped strings.
- Claude still fails closed on mixed or invalid explicit session IDs.
- All input lines still pass through `encoding/json`; malformed lines still
  contribute the same final diagnostic severity.
- Recent snapshots never erase cached sessions and never overwrite the cache.
- Only the exhaustive phase can declare a host successful and complete its
  cache write.
- Context cancellation stops both phases and discards later messages from the
  canceled TUI generation.
- Scanner line-size, discovered-session, protocol, host-worker, and SSH timeout
  bounds remain enforced.
- A protocol failure cannot expose an unvalidated snapshot.

## Testing

Provider tests will prove:

- files are enumerated once and partitioned at the exact seven-day boundary
- recent files are emitted before old files are parsed
- both adapters begin each phase before either phase is released
- exhaustive output ordering and identities match the current implementation
- invalid adapter results and combined limits retain their behavior
- Codex duplicate metadata, escaped type values, corrupt records, and title
  extraction remain unchanged
- Claude mixed IDs, corrupt records, native/prompt title precedence, and
  internal-session exclusion remain unchanged

Protocol and SSH tests will prove:

- a valid ARS/3 stream yields recent then exhaustive snapshots before process
  exit
- nonce, phase order, count, summary, runtime, line, and total-size violations
  fail closed
- one probe and one collector invocation serve both snapshots
- cancellation and temporary collector cleanup remain bounded

Application tests will prove:

- cache emits first
- recent sessions overlay without removing cached history
- exhaustive sessions replace the host and write the cache once
- a failed exhaustive phase retains usable rows and does not write cache
- multiple hosts continue to refresh independently within the worker bound

TUI tests will prove:

- initial empty collection renders `loading sessions…`
- loading copy uses only generic `refreshing`, without cache provenance or
  progressive phase names
- snapshots apply immediately before interaction
- navigation and search stage updates until the idle timer
- multiple staged snapshots collapse to the newest result
- canonical selection survives an applied update
- a completed healthy empty model renders the existing empty-session guidance

Verification will include focused tests, full `go test ./...`, `go test -race
./...`, `go vet ./...`, a production build, three warm recent/full collector
measurements, identity/diagnostic comparison against the baseline
implementation, and a real PTY run against localhost plus the configured SSH
peer.
