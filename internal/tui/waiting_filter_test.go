package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// waitingModel builds a ready model over one attached, one saved and count
// running sessions, with the running session at index waiting marked as needing
// input. Every other running session is left unknown, i.e. unprobed.
func waitingModel(count, waiting int) model {
	items := runningSessions(count)
	value, _ := activityModel(items, "")
	value.activity = map[sessionKey]activityEntry{
		keyOf(items[2+waiting]): {state: activityWaiting, at: value.deps.Now()},
	}
	value.refreshVisible()
	return value
}

func TestModelDollarShowsOnlyWaitingSessions(t *testing.T) {
	value := waitingModel(3, 1)
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Text: "$"}))
	if !value.waitingFilter {
		t.Fatalf("waitingFilter after $ = %t, want on", value.waitingFilter)
	}
	sessions := rowSessions(value.rows)
	if len(sessions) != 1 || sessions[0].Title != "running 1" {
		t.Fatalf("rows under $ = %+v, want only the waiting session", sessions)
	}
	if value.matched != 1 {
		t.Fatalf("matched under $ = %d, want 1", value.matched)
	}

	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Text: "$"}))
	if value.waitingFilter {
		t.Fatalf("waitingFilter after second $ = %t, want off", value.waitingFilter)
	}
	if value.matched != 5 {
		t.Fatalf("matched after clearing $ = %d, want every session back", value.matched)
	}
}

// A session with no probe result yet is not waiting, so $ can legitimately show
// nothing at all. The empty-filter guidance has to cover that state.
func TestModelDollarWithNoWaitingSessionsRendersEmptyGuidance(t *testing.T) {
	value, _ := activityModel(runningSessions(2), "")
	value.noColor = true
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Text: "$"}))
	if len(value.rows) != 0 {
		t.Fatalf("rows under $ with no probe results = %+v, want none", value.rows)
	}
	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "no sessions match filter · esc to clear") {
		t.Fatalf("empty $ filter view missing guidance: %q", content)
	}
}

// A probe landing while $ is active has to rebuild the visible rows: the
// session flips to waiting and must appear without the user pressing anything.
func TestModelActivityResultRefreshesListUnderDollarFilter(t *testing.T) {
	items := runningSessions(2)
	value, _ := activityModel(items, "")
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Text: "$"}))
	if len(value.rows) != 0 {
		t.Fatalf("rows before any probe = %+v, want none", value.rows)
	}
	value, _ = updateModel(value, activityMsg{key: keyOf(items[3]), state: activityWaiting})
	sessions := rowSessions(value.rows)
	if len(sessions) != 1 || sessions[0].Title != "running 1" {
		t.Fatalf("rows after the waiting probe = %+v, want the probed session", sessions)
	}
}

// A probe that clears the waiting state has to drop the row again, so the list
// never keeps an agent the user has already answered.
func TestModelActivityWorkingResultDropsRowUnderDollarFilter(t *testing.T) {
	items := runningSessions(2)
	value := waitingModel(2, 0)
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Text: "$"}))
	if len(rowSessions(value.rows)) != 1 {
		t.Fatalf("rows under $ = %+v, want the waiting session", rowSessions(value.rows))
	}
	value, _ = updateModel(value, activityMsg{key: keyOf(items[2]), state: activityWorking})
	if len(value.rows) != 0 {
		t.Fatalf("rows after the working probe = %+v, want none", value.rows)
	}
}

// $ joins the same union as !@#: a state filter admits its sessions and $ adds
// the waiting ones on top, rather than intersecting.
func TestModelDollarCombinesWithStateFiltersAsUnion(t *testing.T) {
	value := waitingModel(3, 1)
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Text: "#"}))
	if value.matched != 1 {
		t.Fatalf("matched under # = %d, want the saved session", value.matched)
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Text: "$"}))
	titles := make([]string, 0, 2)
	for _, item := range value.visibleSessions() {
		titles = append(titles, item.Title)
	}
	if len(titles) != 2 || value.matched != 2 {
		t.Fatalf("sessions under #$ = %v matched=%d, want the saved and the waiting one", titles, value.matched)
	}
}

// A waiting session whose runtime state is also filtered on matches both halves
// of the union and still has to appear exactly once.
func TestModelDollarWithRunningFilterDoesNotDuplicateWaitingSessions(t *testing.T) {
	value := waitingModel(3, 1)
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Text: "@"}))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Text: "$"}))
	if value.matched != 3 {
		t.Fatalf("matched under @$ = %d, want the 3 running sessions once each", value.matched)
	}
}

func TestModelDollarComposesWithSearchQuery(t *testing.T) {
	value := waitingModel(3, 1)
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Text: "$"}))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: '/'}))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "running 1"}))
	if len(rowSessions(value.rows)) != 1 {
		t.Fatalf("$ + matching query rows = %+v, want 1", rowSessions(value.rows))
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "0"}))
	if len(rowSessions(value.rows)) != 0 {
		t.Fatalf("$ + non-matching query rows = %+v, want none", rowSessions(value.rows))
	}
}

// Only a running session can need input, so $ never admits a saved or attached
// row even if a stale activity entry still claims waiting for its key.
func TestModelDollarIgnoresWaitingEntriesOfNonRunningSessions(t *testing.T) {
	items := runningSessions(1)
	value, _ := activityModel(items, "")
	value.activity = map[sessionKey]activityEntry{
		keyOf(items[0]): {state: activityWaiting, at: value.deps.Now()},
		keyOf(items[1]): {state: activityWaiting, at: value.deps.Now()},
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Text: "$"}))
	if len(value.rows) != 0 {
		t.Fatalf("rows under $ for attached/saved waiting entries = %+v, want none", value.rows)
	}
}

func TestModelEscapeClearsDollarFilter(t *testing.T) {
	value := waitingModel(2, 0)
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Text: "$"}))
	if !value.filterActive() {
		t.Fatalf("filterActive under $ = false, want true")
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if value.waitingFilter || value.filterActive() {
		t.Fatalf("escape left waitingFilter=%t filterActive=%t, want both cleared", value.waitingFilter, value.filterActive())
	}
	if value.matched != 4 {
		t.Fatalf("matched after escape = %d, want every session back", value.matched)
	}
}

func TestModelDollarWhileSearchingTypesIntoQuery(t *testing.T) {
	value := readyModel()
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: '/'}))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "$"}))
	if value.query != "$" {
		t.Fatalf("query while searching = %q, want %q", value.query, "$")
	}
	if value.waitingFilter {
		t.Fatalf("$ while searching toggled the filter")
	}
}

func TestModelDollarWhileComposingTypesIntoMessage(t *testing.T) {
	value := readyModel()
	value.selectRow(firstSessionRow(value.rows))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	if !value.composing {
		t.Fatalf("m did not start compose on %+v", value.rows[value.selected])
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "$"}))
	if value.compose != "$" {
		t.Fatalf("compose text = %q, want %q", value.compose, "$")
	}
	if value.waitingFilter {
		t.Fatalf("$ while composing toggled the filter")
	}
}

func TestHeaderShowsWaitingFilterIndicator(t *testing.T) {
	value := waitingModel(2, 0)
	value.noColor = true
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Text: "$"}))
	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "· filter "+activityWaitingSymbol) {
		t.Fatalf("header missing the needs-input filter indicator: %q", content)
	}

	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Text: "!"}))
	content = ansi.Strip(value.View().Content)
	if !strings.Contains(content, "· filter ●"+activityWaitingSymbol) {
		t.Fatalf("header missing the combined needs-input indicator: %q", content)
	}
}

func TestHelpOverlayAndFooterAdvertiseWaitingFilter(t *testing.T) {
	value := readyModel()
	value.width = 140
	content := ansi.Strip(value.help(value.contentWidth()))
	if !strings.Contains(content, "!@#$ filter") {
		t.Fatalf("footer help missing the needs-input filter hint: %q", content)
	}

	value.showHelp = true
	overlay := ansi.Strip(value.View().Content)
	if !strings.Contains(overlay, "! / @ / # / $") ||
		!strings.Contains(overlay, "filter attached / running / idle / needs input") {
		t.Fatalf("help overlay missing the needs-input filter binding:\n%s", overlay)
	}
}

// filterActive drives the esc hint and the help overlay's filter context, so a
// lone $ has to register there exactly like a state filter does.
func TestWaitingFilterCountsAsFilterActive(t *testing.T) {
	value := waitingModel(2, 0)
	value.noColor = true
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Text: "$"}))
	if !value.helpContexts()[helpFilterActive] {
		t.Fatalf("helpContexts under $ = %+v, want the filter context", value.helpContexts())
	}
	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "esc clear") {
		t.Fatalf("footer missing esc clear hint under $: %q", content)
	}
}

func TestFilterByWaitingKeepsOnlyRunningWaitingSessions(t *testing.T) {
	items := runningSessions(2)
	activity := map[sessionKey]activityEntry{
		keyOf(items[0]): {state: activityWaiting},
		keyOf(items[2]): {state: activityWaiting},
		keyOf(items[3]): {state: activityWorking},
	}
	got := filterByWaiting(items, activity)
	if len(got) != 1 || keyOf(got[0]) != keyOf(items[2]) {
		t.Fatalf("filterByWaiting() = %+v, want only the waiting running session", got)
	}
}
