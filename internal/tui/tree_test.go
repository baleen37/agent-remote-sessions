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
	modes := map[string]groupMode{"ars": groupModeOpen, "blog": groupModeOpen}
	rows := buildRows(items, modes, false)
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

func TestBuildRowsClosedHidesSessionsUnlessSearching(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	items := []session.Session{
		treeSession("ars", "ars-live", session.RuntimeRunning, base),
	}
	modes := map[string]groupMode{"ars": groupModeClosed}
	rows := buildRows(items, modes, false)
	if len(rows) != 1 || !rows[0].collapsed {
		t.Fatalf("closed rows = %+v", rows)
	}
	rows = buildRows(items, modes, true)
	if len(rows) != 2 || rows[0].collapsed {
		t.Fatalf("search rows = %+v", rows)
	}
}

func TestBuildRowsAutoShowsOnlyActiveWithMoreRow(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	items := []session.Session{
		treeSession("ars", "ars-live", session.RuntimeRunning, base),
		treeSession("ars", "ars-saved", session.RuntimeSaved, base.Add(-time.Hour)),
		treeSession("ars", "ars-older", session.RuntimeSaved, base.Add(-2*time.Hour)),
	}
	rows := buildRows(items, nil, false)
	if len(rows) != 3 {
		t.Fatalf("rows = %+v, want header, active session, more", rows)
	}
	if rows[0].kind != rowHeader || rows[0].collapsed || rows[0].count != 3 {
		t.Fatalf("header = %+v", rows[0])
	}
	if rows[1].kind != rowSession || rows[1].session.NativeID != "ars-live" || rows[1].last {
		t.Fatalf("active row = %+v", rows[1])
	}
	if rows[2].kind != rowMore || rows[2].project != "ars" || rows[2].count != 2 || !rows[2].last {
		t.Fatalf("more row = %+v", rows[2])
	}
}

func TestBuildRowsAutoCollapsesGroupsWithoutActiveSessions(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	items := []session.Session{
		treeSession("blog", "blog-old", session.RuntimeSaved, base),
	}
	rows := buildRows(items, nil, false)
	if len(rows) != 1 || rows[0].kind != rowHeader || !rows[0].collapsed {
		t.Fatalf("rows = %+v, want a single collapsed header", rows)
	}
	rows = buildRows(items, nil, true)
	if len(rows) != 2 || rows[0].collapsed || rows[1].kind != rowSession {
		t.Fatalf("search rows = %+v, want expanded group", rows)
	}
}

func TestBuildRowsAutoAllActiveHasNoMoreRow(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	items := []session.Session{
		treeSession("ars", "ars-live", session.RuntimeRunning, base),
		treeSession("ars", "ars-live2", session.RuntimeAttached, base.Add(-time.Hour)),
	}
	rows := buildRows(items, nil, false)
	if len(rows) != 3 || rows[2].kind != rowSession || !rows[2].last {
		t.Fatalf("rows = %+v, want all active sessions and no more row", rows)
	}
}

func TestRefOfDistinguishesHeadersSessionsAndMoreRows(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	item := treeSession("ars", "ars-live", session.RuntimeRunning, base)
	saved := treeSession("ars", "ars-saved", session.RuntimeSaved, base.Add(-time.Hour))
	rows := buildRows([]session.Session{item, saved}, nil, false)
	header, leaf, more := refOf(rows[0]), refOf(rows[1]), refOf(rows[2])
	if header.kind != rowHeader || header.project != "ars" || header.key != (sessionKey{}) {
		t.Fatalf("header ref = %+v", header)
	}
	if leaf.kind != rowSession || leaf.key != keyOf(item) {
		t.Fatalf("session ref = %+v", leaf)
	}
	if more.kind != rowMore || more.project != "ars" || more.key != (sessionKey{}) {
		t.Fatalf("more ref = %+v", more)
	}
}
