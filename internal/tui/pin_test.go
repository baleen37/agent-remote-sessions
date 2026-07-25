package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// shiftP is the key event a real terminal delivers for Shift+P: bubbletea v2
// reports the unshifted code with the shifted text, so bindings must match on
// Text.
var shiftP = tea.Key{Code: 'p', Text: "P", Mod: tea.ModShift}

func TestModelShiftPTogglesPinOnSelectedSession(t *testing.T) {
	for _, pin := range []tea.Key{shiftP, {Code: 'P', Text: "P"}} {
		value := readyModel()
		row, ok := value.selectedRow()
		if !ok || row.kind != rowSession {
			t.Fatalf("selected row = %+v, want a session row", row)
		}
		key := keyOf(row.session)

		value, _ = updateModel(value, tea.KeyPressMsg(pin))
		if !value.pins[key] {
			t.Fatalf("P %+v pins = %+v, want %+v pinned", pin, value.pins, key)
		}
		value, _ = updateModel(value, tea.KeyPressMsg(pin))
		if value.pins[key] {
			t.Fatalf("P %+v pins after second press = %+v, want %+v unpinned", pin, value.pins, key)
		}
	}
}

func TestModelShiftPOnGroupHeaderIsNoOp(t *testing.T) {
	value := readyModel()
	value.selectHeader("ars")
	row, ok := value.selectedRow()
	if !ok || row.kind != rowHeader {
		t.Fatalf("selected row = %+v, want a header row", row)
	}

	value, _ = updateModel(value, tea.KeyPressMsg(shiftP))
	if len(value.pins) != 0 {
		t.Fatalf("pins after P on header = %+v, want none", value.pins)
	}
}

func TestModelShiftPDoesNotTogglePreview(t *testing.T) {
	value := readyModel()
	before := value.previewOn
	value, _ = updateModel(value, tea.KeyPressMsg(shiftP))
	if value.previewOn != before {
		t.Fatalf("previewOn = %t after P, want unchanged %t", value.previewOn, before)
	}
}

func TestModelShiftPIsLiteralWhileSearching(t *testing.T) {
	value := readyModel()
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: '/'}))
	value, _ = updateModel(value, tea.KeyPressMsg(shiftP))
	if value.query != "P" {
		t.Fatalf("query while searching = %q, want literal P", value.query)
	}
	if len(value.pins) != 0 {
		t.Fatalf("pins mutated while searching literal P: %+v", value.pins)
	}
}

func TestModelShiftPIsLiteralWhileComposing(t *testing.T) {
	value := readyModel()
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	value, _ = updateModel(value, tea.KeyPressMsg(shiftP))
	if value.compose != "P" {
		t.Fatalf("compose while composing = %q, want literal P", value.compose)
	}
	if len(value.pins) != 0 {
		t.Fatalf("pins mutated while composing literal P: %+v", value.pins)
	}
}

func TestBuildRowsSortsPinnedSessionFirstWithinGroup(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	newest := treeSession("ars", "ars-newest", session.RuntimeRunning, base)
	older := treeSession("ars", "ars-older", session.RuntimeRunning, base.Add(-time.Hour))
	items := []session.Session{newest, older}
	modes := map[string]groupMode{"ars": groupModeOpen}

	rows := buildRows(items, modes, false, nil)
	if ids := sessionIDs(rows); ids[0] != "ars-newest" || ids[1] != "ars-older" {
		t.Fatalf("unpinned order = %v, want newest first", ids)
	}

	pins := map[sessionKey]bool{keyOf(older): true}
	rows = buildRows(items, modes, false, pins)
	if ids := sessionIDs(rows); ids[0] != "ars-older" || ids[1] != "ars-newest" {
		t.Fatalf("pinned order = %v, want the pinned older session first", ids)
	}

	rows = buildRows(items, modes, false, map[sessionKey]bool{keyOf(older): false})
	if ids := sessionIDs(rows); ids[0] != "ars-newest" || ids[1] != "ars-older" {
		t.Fatalf("unpinned order = %v, want the recency order restored", ids)
	}
}

func TestBuildRowsSortsGroupWithPinnedSessionBeforeOtherGroups(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	fresh := treeSession("ars", "ars-live", session.RuntimeRunning, base)
	stale := treeSession("blog", "blog-live", session.RuntimeRunning, base.Add(-3*time.Hour))
	items := []session.Session{fresh, stale}
	modes := map[string]groupMode{"ars": groupModeOpen, "blog": groupModeOpen}

	rows := buildRows(items, modes, false, nil)
	if projects := headerProjects(rows); projects[0] != "ars" || projects[1] != "blog" {
		t.Fatalf("unpinned group order = %v, want ars first", projects)
	}

	pins := map[sessionKey]bool{keyOf(stale): true}
	rows = buildRows(items, modes, false, pins)
	if projects := headerProjects(rows); projects[0] != "blog" || projects[1] != "ars" {
		t.Fatalf("pinned group order = %v, want blog (pinned) first", projects)
	}

	rows = buildRows(items, modes, false, nil)
	if projects := headerProjects(rows); projects[0] != "ars" || projects[1] != "blog" {
		t.Fatalf("group order after unpin = %v, want ars first again", projects)
	}
}

// TestBuildRowsPinnedGroupsKeepRecencyOrderAmongThemselves guards the two-tier
// sort: pinning promotes a group past unpinned ones but does not reshuffle
// pinned groups relative to each other.
func TestBuildRowsPinnedGroupsKeepRecencyOrderAmongThemselves(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	first := treeSession("ars", "ars-live", session.RuntimeRunning, base)
	second := treeSession("blog", "blog-live", session.RuntimeRunning, base.Add(-time.Hour))
	third := treeSession("api", "api-live", session.RuntimeRunning, base.Add(-2*time.Hour))
	items := []session.Session{first, second, third}
	modes := map[string]groupMode{"ars": groupModeOpen, "blog": groupModeOpen, "api": groupModeOpen}

	pins := map[sessionKey]bool{keyOf(second): true, keyOf(third): true}
	rows := buildRows(items, modes, false, pins)
	projects := headerProjects(rows)
	want := []string{"blog", "api", "ars"}
	for index, expect := range want {
		if projects[index] != expect {
			t.Fatalf("group order = %v, want %v", projects, want)
		}
	}
}

// TestBuildRowsAutoKeepsPinnedSavedSessionVisible covers the auto-mode
// interaction: auto mode normally hides saved sessions behind a more row, but a
// pinned session must stay on screen or the pin silently does nothing.
func TestBuildRowsAutoKeepsPinnedSavedSessionVisible(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	live := treeSession("ars", "ars-live", session.RuntimeRunning, base)
	saved := treeSession("ars", "ars-saved", session.RuntimeSaved, base.Add(-time.Hour))
	other := treeSession("ars", "ars-other", session.RuntimeSaved, base.Add(-2*time.Hour))
	items := []session.Session{live, saved, other}

	rows := buildRows(items, nil, false, nil)
	if ids := sessionIDs(rows); len(ids) != 1 || ids[0] != "ars-live" {
		t.Fatalf("unpinned auto rows = %v, want only the active session", ids)
	}

	rows = buildRows(items, nil, false, map[sessionKey]bool{keyOf(saved): true})
	ids := sessionIDs(rows)
	if len(ids) != 2 || ids[0] != "ars-saved" || ids[1] != "ars-live" {
		t.Fatalf("pinned auto rows = %v, want the pinned saved session first then the active one", ids)
	}
	more := rows[len(rows)-1]
	if more.kind != rowMore || more.count != 1 {
		t.Fatalf("more row = %+v, want 1 remaining hidden session", more)
	}
}

// TestBuildRowsAutoPinnedSavedSessionKeepsGroupExpanded guards a group whose
// only pinned session is saved: auto mode collapses groups with no active
// session, which would hide the pin entirely.
func TestBuildRowsAutoPinnedSavedSessionKeepsGroupExpanded(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	saved := treeSession("blog", "blog-saved", session.RuntimeSaved, base)
	items := []session.Session{saved}

	rows := buildRows(items, nil, false, nil)
	if len(rows) != 1 || !rows[0].collapsed {
		t.Fatalf("unpinned rows = %+v, want a single collapsed header", rows)
	}

	rows = buildRows(items, nil, false, map[sessionKey]bool{keyOf(saved): true})
	if len(rows) != 2 || rows[0].collapsed || rows[1].kind != rowSession {
		t.Fatalf("pinned rows = %+v, want an expanded group showing the pinned session", rows)
	}
}

func TestPinnedRowRendersMarkerAndUnpinnedDoesNot(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, true
	value = openAllGroups(value)
	row, ok := value.selectedRow()
	if !ok || row.kind != rowSession {
		t.Fatalf("selected row = %+v, want a session row", row)
	}
	title := sessionTitle(row.session)

	before := sessionRows(value.View().Content)
	for _, line := range before {
		if strings.Contains(line, pinMarker+" ") {
			t.Fatalf("unpinned rows already render a pin marker: %q", before)
		}
	}

	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'p', Text: "P", Mod: tea.ModShift}))
	after := sessionRows(value.View().Content)
	pinnedLines := 0
	for _, line := range after {
		if strings.Contains(line, pinMarker+" "+title) {
			pinnedLines++
		}
	}
	if pinnedLines != 1 {
		t.Fatalf("rows with %q marker on %q = %d, want 1:\n%s", pinMarker, title, pinnedLines, strings.Join(after, "\n"))
	}
}

// TestHelpOverlayAndFooterAdvertisePin asserts at a width that fits every
// hint: "P pin" is the first item joinFooterItems drops, so a narrower
// terminal legitimately hides it (see the narrow-width test below).
func TestHelpOverlayAndFooterAdvertisePin(t *testing.T) {
	value := readyModel()
	value.width = 170
	content := ansi.Strip(value.help(value.contentWidth()))
	if !strings.Contains(content, "P pin") {
		t.Fatalf("footer help missing pin hint: %q", content)
	}

	value.showHelp = true
	overlay := ansi.Strip(value.View().Content)
	if !strings.Contains(overlay, "pin / unpin session") {
		t.Fatalf("help overlay missing pin binding:\n%s", overlay)
	}
}

// TestFooterDropsPinHintBeforeNavigationOnNarrowWidths keeps the pin hint in
// the droppable set: it is secondary, so it must give way before the
// navigation and quit hints do.
func TestFooterDropsPinHintBeforeNavigationOnNarrowWidths(t *testing.T) {
	value := readyModel()
	value.width = 60
	content := ansi.Strip(value.help(value.contentWidth()))
	if strings.Contains(content, "P pin") {
		t.Fatalf("footer at 60 cols kept the pin hint: %q", content)
	}
	if !strings.Contains(content, "? help") {
		t.Fatalf("footer at 60 cols dropped help: %q", content)
	}
}

func sessionIDs(rows []listRow) []string {
	var ids []string
	for _, row := range rows {
		if row.kind == rowSession {
			ids = append(ids, row.session.NativeID)
		}
	}
	return ids
}

func headerProjects(rows []listRow) []string {
	var projects []string
	for _, row := range rows {
		if row.kind == rowHeader {
			projects = append(projects, row.project)
		}
	}
	return projects
}
