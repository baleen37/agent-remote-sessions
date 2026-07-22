package tui

import (
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func treeSession(project, id string, state session.RuntimeState, updated time.Time) session.Session {
	return session.Session{
		Host: "localhost",
		Candidate: session.Candidate{
			Provider:  session.Claude,
			NativeID:  id,
			UpdatedAt: updated,
			CWD:       "/work/" + project,
			Title:     id,
		},
		Runtime: session.Runtime{State: state},
	}
}

func TestBuildRowsGroupsAndOrdersByStateThenActivity(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	items := []session.Session{
		treeSession("blog", "blog-old", session.RuntimeSaved, base.Add(-3*time.Hour)),
		treeSession("ars", "ars-saved", session.RuntimeSaved, base),
		treeSession("ars", "ars-live", session.RuntimeRunning, base.Add(-2*time.Hour)),
	}
	rows := buildRows(items, nil, false)
	want := []struct {
		kind    rowKind
		project string
		id      string
		last    bool
	}{
		{rowHeader, "ars", "", false},
		{rowSession, "ars", "ars-live", false},
		{rowSession, "ars", "ars-saved", true},
		{rowHeader, "blog", "", false},
		{rowSession, "blog", "blog-old", true},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %d, want %d", len(rows), len(want))
	}
	for index, expect := range want {
		row := rows[index]
		if row.kind != expect.kind || row.project != expect.project || row.last != expect.last {
			t.Fatalf("row %d = %+v, want %+v", index, row, expect)
		}
		if expect.kind == rowSession && row.session.NativeID != expect.id {
			t.Fatalf("row %d id = %s, want %s", index, row.session.NativeID, expect.id)
		}
	}
	if rows[0].count != 2 || rows[0].state != session.RuntimeRunning {
		t.Fatalf("ars header = %+v", rows[0])
	}
	if rows[3].count != 1 || rows[3].state != session.RuntimeSaved {
		t.Fatalf("blog header = %+v", rows[3])
	}
}

func TestBuildRowsCollapseHidesSessionsUnlessSearching(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	items := []session.Session{
		treeSession("ars", "ars-live", session.RuntimeRunning, base),
	}
	collapsed := map[string]bool{"ars": true}
	rows := buildRows(items, collapsed, false)
	if len(rows) != 1 || !rows[0].collapsed {
		t.Fatalf("collapsed rows = %+v", rows)
	}
	rows = buildRows(items, collapsed, true)
	if len(rows) != 2 || rows[0].collapsed {
		t.Fatalf("search rows = %+v", rows)
	}
}

func TestRefOfDistinguishesHeadersAndSessions(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	item := treeSession("ars", "ars-live", session.RuntimeRunning, base)
	rows := buildRows([]session.Session{item}, nil, false)
	header, leaf := refOf(rows[0]), refOf(rows[1])
	if header.kind != rowHeader || header.project != "ars" || header.key != (sessionKey{}) {
		t.Fatalf("header ref = %+v", header)
	}
	if leaf.kind != rowSession || leaf.key != keyOf(item) {
		t.Fatalf("session ref = %+v", leaf)
	}
}
