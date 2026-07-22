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

func buildRows(items []session.Session, modes map[string]groupMode, searchActive bool) []listRow {
	var rows []listRow
	for _, group := range groupSessions(items) {
		mode := modes[group.project]
		if searchActive {
			mode = groupModeOpen
		}
		visible := group.sessions
		hidden := 0
		if mode == groupModeAuto {
			active := activeSessions(group.sessions)
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

func activeSessions(items []session.Session) []session.Session {
	var active []session.Session
	for _, item := range items {
		if item.Runtime.State != session.RuntimeSaved {
			active = append(active, item)
		}
	}
	return active
}

func groupSessions(items []session.Session) []sessionGroup {
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
			leftSaved := members[left].Runtime.State == session.RuntimeSaved
			rightSaved := members[right].Runtime.State == session.RuntimeSaved
			if leftSaved != rightSaved {
				return rightSaved
			}
			return members[left].UpdatedAt.After(members[right].UpdatedAt)
		})
	}
	sort.SliceStable(groups, func(left, right int) bool {
		leftActive := groupState(groups[left].sessions) != session.RuntimeSaved
		rightActive := groupState(groups[right].sessions) != session.RuntimeSaved
		if leftActive != rightActive {
			return leftActive
		}
		return latestActivity(groups[left].sessions).After(latestActivity(groups[right].sessions))
	})
	return groups
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
