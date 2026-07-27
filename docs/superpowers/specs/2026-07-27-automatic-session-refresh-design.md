# Automatic Session Refresh Design

## Goal

Keep a long-running `ars` TUI current without requiring the user to press
`r`. A newly discoverable local or remote session should appear within one
minute plus the time needed for the existing progressive collection to finish.

## Design

Add one fixed one-minute refresh tick to the TUI model. `Init` schedules the
first tick alongside the existing initial collection, spinner, and activity
tick.

When the refresh tick arrives:

- schedule the next one-minute tick;
- if a collection is already running, do not start another one;
- otherwise, start a collection through the existing `restartCollection`
  path.

This reuses the current cache overlay, recent-first snapshot, exhaustive final
snapshot, selection preservation, interaction coalescing, cancellation, and
spinner behavior. The timer does not call providers or caches directly.

## Error and Lifecycle Behavior

Collection errors continue to use the existing result and status handling. A
slow collection can span refresh ticks; those ticks are skipped, so collection
work never overlaps. Bubble Tea owns the timer commands, so quitting the TUI
requires no new cleanup path.

## Testing

Model tests will send the refresh message directly instead of waiting one
minute:

- an idle model starts a new collection and advances its generation;
- a collecting model does not start a duplicate collection;
- both paths keep the next refresh tick scheduled.

The focused TUI tests, full Go test suite, race test, vet, and production build
must remain green.

## Non-goals

- Configurable refresh intervals
- File-system watchers or provider-specific polling
- Changes to manual `r` behavior
- Changes to collection, cache, or wire formats
