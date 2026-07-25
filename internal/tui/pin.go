package tui

import "github.com/baleen37/agent-remote-sessions/internal/session"

// pinMarker prefixes the title of a pinned session row.
const pinMarker = "*"

// togglePin flips the pin on row.session. Pins live in memory only and are
// keyed by sessionKey, so a session that leaves the inventory and comes back on
// a later refresh keeps its pin. Non-session rows are a no-op.
func (value *model) togglePin(row listRow) {
	if row.kind != rowSession {
		return
	}
	key := keyOf(row.session)
	if value.pins[key] {
		delete(value.pins, key)
	} else {
		if value.pins == nil {
			value.pins = make(map[sessionKey]bool)
		}
		value.pins[key] = true
	}
	value.refreshVisible()
}

// pinnedTitle is the title cell for a session row: the plain title, prefixed
// with the marker when pinned. The column layout measures it so the title
// column stays wide enough for the marker.
func pinnedTitle(item session.Session, pins map[sessionKey]bool) string {
	if pins[keyOf(item)] {
		return pinMarker + " " + sessionTitle(item)
	}
	return sessionTitle(item)
}

// pinnedTitleCell renders the title cell, accenting the marker alone so the
// title itself keeps its plain tone.
func (value model) pinnedTitleCell(item session.Session) string {
	if !value.pins[keyOf(item)] || value.noColor {
		return pinnedTitle(item, value.pins)
	}
	return value.styles.selectedCursor.Render(pinMarker) + " " + sessionTitle(item)
}
