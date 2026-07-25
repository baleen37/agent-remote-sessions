package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// staleModelNow is the fixed clock every stale-cutoff test measures against.
var staleModelNow = time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

func staleModelSession(project, id string, state session.RuntimeState, updated time.Time) session.Session {
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

// staleModel builds a ready model with one fresh saved session, one old
// (stale) saved session, and one old live (running) session, so the cutoff,
// the live exception and the age boundary all have a session to exercise.
func staleModel() model {
	items := []session.Session{
		staleModelSession("ars", "fresh-saved", session.RuntimeSaved, staleModelNow.Add(-6*24*time.Hour)),
		staleModelSession("ars", "old-saved", session.RuntimeSaved, staleModelNow.Add(-8*24*time.Hour)),
		staleModelSession("ars", "old-running", session.RuntimeRunning, staleModelNow.Add(-30*24*time.Hour)),
	}
	result := Result{Sessions: items}
	deps := Dependencies{
		Collect:     staticCollect(result),
		Attach:      func(context.Context, session.Session) (ExecCommand, error) { return &fakeExecCommand{}, nil },
		Kill:        func(context.Context, session.Session) error { return nil },
		LocalTarget: "localhost",
		Now:         func() time.Time { return staleModelNow },
		NoColor:     true,
	}
	value := newModel(context.Background(), deps)
	message, hasCollection, _ := initialCommands(value.Init())
	if !hasCollection {
		panic("staleModel: Init did not produce collectUpdateMsg")
	}
	value, _ = updateModel(value, message)
	value.width, value.height = 120, 40
	return value
}

func TestModelHidesStaleSavedSessionsByDefault(t *testing.T) {
	value := staleModel()
	ids := map[string]bool{}
	for _, item := range value.visibleSessions() {
		ids[item.NativeID] = true
	}
	if ids["old-saved"] {
		t.Fatalf("visibleSessions() included the stale saved session: %+v", value.visibleSessions())
	}
	if !ids["fresh-saved"] || !ids["old-running"] {
		t.Fatalf("visibleSessions() = %+v, want the fresh saved and old-running sessions kept", value.visibleSessions())
	}
}

func TestModelAToggleShowsAllSessions(t *testing.T) {
	value := staleModel()
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'a', Text: "a"}))
	if !value.showAll {
		t.Fatalf("showAll after a = %t, want true", value.showAll)
	}
	ids := map[string]bool{}
	for _, item := range value.visibleSessions() {
		ids[item.NativeID] = true
	}
	if !ids["old-saved"] {
		t.Fatalf("visibleSessions() under showAll = %+v, want the stale session revealed", value.visibleSessions())
	}

	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'a', Text: "a"}))
	if value.showAll {
		t.Fatalf("showAll after second a = %t, want false", value.showAll)
	}
	ids = map[string]bool{}
	for _, item := range value.visibleSessions() {
		ids[item.NativeID] = true
	}
	if ids["old-saved"] {
		t.Fatalf("visibleSessions() after toggling off = %+v, want the stale session hidden again", value.visibleSessions())
	}
}

func TestModelAIsLiteralWhileSearching(t *testing.T) {
	value := staleModel()
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: '/'}))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "a"}))
	if value.query != "a" {
		t.Fatalf("query while searching = %q, want literal a", value.query)
	}
	if value.showAll {
		t.Fatalf("a while searching toggled showAll")
	}
}

func TestModelAIsLiteralWhileComposing(t *testing.T) {
	value := staleModel()
	value.selectRow(firstSessionRow(value.rows))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "a"}))
	if value.compose != "a" {
		t.Fatalf("compose while composing = %q, want literal a", value.compose)
	}
	if value.showAll {
		t.Fatalf("a while composing toggled showAll")
	}
}

// An active search query bypasses the cutoff entirely, so a query matching
// the stale session's id must surface it even with showAll off.
func TestModelActiveSearchBypassesStaleCutoff(t *testing.T) {
	value := staleModel()
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: '/'}))
	for _, character := range "old-saved" {
		value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: string(character)}))
	}
	sessions := value.visibleSessions()
	if len(sessions) != 1 || sessions[0].NativeID != "old-saved" {
		t.Fatalf("visibleSessions() under search = %+v, want the stale session found", sessions)
	}
}

// The stale cutoff applies before the !@#$ state filters: turning on the
// saved filter must not resurrect a session the cutoff already hid.
func TestModelStaleCutoffAppliesBeforeStateFilters(t *testing.T) {
	value := staleModel()
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Text: "#"}))
	for _, item := range value.visibleSessions() {
		if item.NativeID == "old-saved" {
			t.Fatalf("# filter revealed the stale session: %+v", value.visibleSessions())
		}
	}
}

func TestHeaderShowsOlderHiddenCountWhenAny(t *testing.T) {
	value := staleModel()
	value.noColor = true
	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "· 1 older hidden") {
		t.Fatalf("header missing older hidden count: %q", content)
	}

	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'a', Text: "a"}))
	content = ansi.Strip(value.View().Content)
	if strings.Contains(content, "older hidden") {
		t.Fatalf("header still shows older hidden count under showAll: %q", content)
	}
	if !strings.Contains(content, "· showing all") {
		t.Fatalf("header missing showing all indicator: %q", content)
	}
}

func TestHeaderOmitsOlderHiddenWhenNoneHidden(t *testing.T) {
	value := readyModel()
	value.noColor = true
	content := ansi.Strip(value.View().Content)
	if strings.Contains(content, "older hidden") {
		t.Fatalf("header shows older hidden count with nothing stale: %q", content)
	}
	if strings.Contains(content, "showing all") {
		t.Fatalf("header shows showing all with showAll off: %q", content)
	}
}

func TestHelpOverlayAndFooterAdvertiseStaleToggle(t *testing.T) {
	value := staleModel()
	value.width = 170
	content := ansi.Strip(value.help(value.contentWidth()))
	if !strings.Contains(content, "a older") {
		t.Fatalf("footer help missing the stale-toggle hint: %q", content)
	}

	value.showHelp = true
	overlay := ansi.Strip(value.View().Content)
	if !strings.Contains(overlay, "show / hide sessions older than 7d") {
		t.Fatalf("help overlay missing the stale-toggle binding:\n%s", overlay)
	}
}
