# Session List Cache Design

Date: 2026-07-22
Status: Approved

## Problem

Every TUI start, `r` refresh, and attach-return triggers a full live
collection of localhost plus all SSH remotes. Each remote costs two
sequential SSH invocations with a 60s host timeout, and results are only
rendered after the whole collection finishes. One slow or unreachable
remote delays the entire list, and nothing is reused between runs.

## Goal

Show the session list instantly from a per-host disk cache, then refresh
each host live in the background and update rows as each host finishes.
A slow remote must never delay fresh data from fast hosts.

## Non-Goals

- No TTL or cache expiry: stale entries are always shown, marked stale,
  and replaced when live data arrives.
- No background daemon, polling, or file watching. Collection still runs
  only on user actions (start, `r`, attach-return).
- No changes to the collection protocol itself (e.g. merging the uname
  probe into the collector round-trip).
- Nothing is installed or persisted on peers. The cache lives only on
  the local machine.

## Design

### 1. Cache store (`internal/app`, new file)

- Path: `${XDG_CACHE_HOME:-~/.cache}/ars/hosts/<encoded-host>.json`, one
  file per host. Host strings (`user@host:port`) are percent-encoded
  (`url.PathEscape`) into safe, collision-free file names.
- Contents: schema version, collected-at timestamp, and that host's
  `[]session.Session`. Existing value types serialize to JSON as-is.
- Permissions match the config store: directory `0700`, files `0600`
  (session titles and CWDs may be sensitive).
- Corrupt files or mismatched schema versions are silently treated as a
  cache miss. Successful live collections overwrite atomically
  (temp file + rename). Cache files for hosts absent from the inventory
  are not loaded.

### 2. Collection flow

Current: `Collect(ctx)` returns once after all hosts finish, delivered
as a single `collectDoneMsg`.

New: the TUI `Dependencies` gains per-host streaming.

1. On start, `r`, and attach-return, cached entries are loaded and shown
   immediately, with those hosts marked stale.
2. The existing worker pool (limit 4) collects hosts live; as each host
   finishes, a `hostCollectedMsg{generation, host, sessions, err}` is
   emitted.
3. A successful host replaces its rows, clears the stale mark, and
   rewrites its cache file. A failed host keeps its cached rows, stays
   stale, and reports through the existing Errors channel.

The model keeps `map[host] -> (sessions, stale)` and rebuilds the
visible list on every update using the existing `mergeCollections`
sort/dedup rules. The existing generation guard applies to per-host
messages: stale-generation messages are discarded.

### 3. UI

- Rows for hosts without a live result yet show a dim `(cached)` mark.
- The header keeps showing `· refreshing` until every host has reported.
- Cached rows are attachable immediately; if the session is gone, the
  existing attach failure path handles it.

### 4. Docs and tests

- Update README's "does not cache" wording: the local binary now keeps a
  session-list cache; peers remain untouched.
- Tests:
  - cache store: round-trip, corrupt-file miss, atomic overwrite,
    inventory filtering, permissions
  - per-host merge and generation guard in the TUI model
  - TUI shows cached rows before live results arrive
