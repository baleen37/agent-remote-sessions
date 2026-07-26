package tui

import (
	"strings"
	"time"
	"unicode"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

// staleAfter is the age past which a saved (non-live) session is hidden by
// default; the a key toggles showing it.
const staleAfter = session.RecentWindow

type sessionKey struct {
	host     string
	provider session.Provider
	nativeID string
}

func keyOf(item session.Session) sessionKey {
	return sessionKey{host: item.Host, provider: item.Provider, nativeID: item.NativeID}
}

// filterByState keeps sessions whose runtime state is enabled in states. An
// empty or nil set means no filter is active, so every session passes.
func filterByState(items []session.Session, states map[session.RuntimeState]bool) []session.Session {
	if len(states) == 0 {
		return append([]session.Session(nil), items...)
	}
	filtered := make([]session.Session, 0, len(items))
	for _, item := range items {
		if states[item.Runtime.State] {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// filterByWaiting keeps the sessions whose last probe saw the agent waiting on a
// reply. Waiting is a view-layer state, so it comes from the activity map rather
// than the session: a session with no probe result yet is not waiting and drops
// out until one lands.
func filterByWaiting(items []session.Session, activity map[sessionKey]activityEntry) []session.Session {
	filtered := make([]session.Session, 0, len(items))
	for _, item := range items {
		if item.Runtime.State == session.RuntimeRunning && activity[keyOf(item)].state == activityWaiting {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// filterByStale hides saved sessions whose latest activity is older than
// staleAfter, returning the visible sessions and how many were hidden. Live
// sessions (running/attached) and pinned sessions are always kept regardless
// of age; showAll disables the cutoff entirely.
func filterByStale(items []session.Session, now time.Time, showAll bool, pins map[sessionKey]bool) ([]session.Session, int) {
	if showAll {
		return append([]session.Session(nil), items...), 0
	}
	visible := make([]session.Session, 0, len(items))
	hidden := 0
	for _, item := range items {
		if item.Runtime.State != session.RuntimeSaved || pins[keyOf(item)] || now.Sub(item.UpdatedAt) <= staleAfter {
			visible = append(visible, item)
			continue
		}
		hidden++
	}
	return visible, hidden
}

func filterSessions(items []session.Session, query, localTarget string) []session.Session {
	if query == "" {
		return append([]session.Session(nil), items...)
	}
	query = foldCase(query)
	filtered := make([]session.Session, 0, len(items))
	for _, item := range items {
		fields := []string{
			item.Title,
			string(item.Provider),
			location(item, localTarget),
			session.Project(item.CWD),
			item.CWD,
			item.NativeID,
		}
		for _, field := range fields {
			if strings.Contains(foldCase(field), query) {
				filtered = append(filtered, item)
				break
			}
		}
	}
	return filtered
}

func foldCase(value string) string {
	return strings.Map(func(character rune) rune {
		folded := character
		for next := unicode.SimpleFold(character); next != character; next = unicode.SimpleFold(next) {
			if next < folded {
				folded = next
			}
		}
		return folded
	}, value)
}

func runtimeOrder(state session.RuntimeState) int {
	switch state {
	case session.RuntimeAttached:
		return 0
	case session.RuntimeRunning:
		return 1
	default:
		return 2
	}
}

func location(item session.Session, localTarget string) string {
	if item.Host == localTarget {
		return ""
	}
	return "[" + item.Host + "]"
}
