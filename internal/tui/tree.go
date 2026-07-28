package tui

import (
	"sort"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

type rowKind int

const (
	rowHeader rowKind = iota
	rowSession
	rowMore
)

type groupMode int

const (
	groupModeAuto groupMode = iota
	groupModeOpen
	groupModeClosed
)

type listRow struct {
	kind      rowKind
	project   string
	count     int
	state     session.RuntimeState
	collapsed bool
	last      bool
	session   session.Session
}

type rowRef struct {
	kind    rowKind
	project string
	key     sessionKey
}

func refOf(row listRow) rowRef {
	if row.kind == rowSession {
		return rowRef{kind: rowSession, project: row.project, key: keyOf(row.session)}
	}
	return rowRef{kind: row.kind, project: row.project}
}

type sessionGroup struct {
	project  string
	sessions []session.Session
}

func buildRows(items []session.Session, modes map[string]groupMode, searchActive bool, pins map[sessionKey]bool) []listRow {
	var rows []listRow
	for _, group := range groupSessions(items, pins) {
		mode := modes[group.project]
		if searchActive {
			mode = groupModeOpen
		}
		visible := group.sessions
		hidden := 0
		if mode == groupModeAuto {
			active := activeSessions(group.sessions, pins)
			if len(active) == 0 {
				mode = groupModeClosed
			} else {
				visible = active
				hidden = len(group.sessions) - len(active)
			}
		}
		rows = append(rows, listRow{
			kind:      rowHeader,
			project:   group.project,
			count:     len(group.sessions),
			state:     groupState(group.sessions),
			collapsed: mode == groupModeClosed,
		})
		if mode == groupModeClosed {
			continue
		}
		for position, item := range visible {
			rows = append(rows, listRow{
				kind:    rowSession,
				project: group.project,
				session: item,
				last:    position == len(visible)-1 && hidden == 0,
			})
		}
		if hidden > 0 {
			rows = append(rows, listRow{kind: rowMore, project: group.project, count: hidden, last: true})
		}
	}
	return rows
}

// activeSessions returns the sessions auto mode keeps on screen: the live ones,
// plus pinned sessions whatever their state — a pin that auto mode folded away
// behind the more row would do nothing.
func activeSessions(items []session.Session, pins map[sessionKey]bool) []session.Session {
	var active []session.Session
	for _, item := range items {
		if item.Runtime.State != session.RuntimeSaved || pins[keyOf(item)] {
			active = append(active, item)
		}
	}
	return active
}

// groupSessions buckets sessions by project and orders both tiers by pin then
// recency. A pinned session leads its group and a group holding one leads the
// list; unpinning restores newest-first order.
func groupSessions(items []session.Session, pins map[sessionKey]bool) []sessionGroup {
	positions := make(map[string]int)
	var groups []sessionGroup
	for _, item := range items {
		project := session.Project(item.CWD)
		position, seen := positions[project]
		if !seen {
			position = len(groups)
			positions[project] = position
			groups = append(groups, sessionGroup{project: project})
		}
		groups[position].sessions = append(groups[position].sessions, item)
	}
	for _, group := range groups {
		members := group.sessions
		sort.SliceStable(members, func(left, right int) bool {
			leftPinned := pins[keyOf(members[left])]
			rightPinned := pins[keyOf(members[right])]
			if leftPinned != rightPinned {
				return leftPinned
			}
			return members[left].UpdatedAt.After(members[right].UpdatedAt)
		})
	}
	sort.SliceStable(groups, func(left, right int) bool {
		leftPinned := hasPinned(groups[left].sessions, pins)
		rightPinned := hasPinned(groups[right].sessions, pins)
		if leftPinned != rightPinned {
			return leftPinned
		}
		return latestActivity(groups[left].sessions).After(latestActivity(groups[right].sessions))
	})
	return groups
}

func hasPinned(items []session.Session, pins map[sessionKey]bool) bool {
	for _, item := range items {
		if pins[keyOf(item)] {
			return true
		}
	}
	return false
}

func groupState(items []session.Session) session.RuntimeState {
	strongest := session.RuntimeSaved
	for _, item := range items {
		if runtimeOrder(item.Runtime.State) < runtimeOrder(strongest) {
			strongest = item.Runtime.State
		}
	}
	return strongest
}

func latestActivity(items []session.Session) time.Time {
	var latest time.Time
	for _, item := range items {
		if item.UpdatedAt.After(latest) {
			latest = item.UpdatedAt
		}
	}
	return latest
}
