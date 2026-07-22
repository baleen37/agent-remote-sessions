package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/output"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func TestModelInitialCollectionNavigatesFiltersAndAttaches(t *testing.T) {
	items := twoSessions()
	result := Result{
		Hosts:    []output.HostResult{{Target: "localhost", Status: output.HostOK}},
		Sessions: items,
	}
	var attached session.Session
	deps := Dependencies{
		Collect: staticCollect(result),
		Attach: func(_ context.Context, item session.Session) (ExecCommand, error) {
			attached = item
			return &fakeExecCommand{}, nil
		},
		LocalTarget: "localhost",
		Now:         func() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) },
	}
	model := newModel(context.Background(), deps)
	command := model.Init()
	if command == nil || !model.collecting || model.generation != 1 {
		t.Fatalf("Init() collecting=%t generation=%d command=%v", model.collecting, model.generation, command)
	}
	message, hasCollection, hasBackgroundQuery := initialCommands(command)
	if !hasCollection || !hasBackgroundQuery || message.generation != 1 || !message.update.Done || len(message.update.Result.Sessions) != 2 {
		t.Fatalf("Init command message = %#v", message)
	}

	model, _ = updateModel(model, message)
	if row, ok := model.selectedRow(); !ok || row.kind != rowSession || keyOf(row.session) != keyOf(items[0]) {
		t.Fatalf("initial selection = %+v", row)
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	if row, _ := model.selectedRow(); row.kind != rowHeader || row.project != "api" || !row.collapsed {
		t.Fatalf("saved-only group not collapsed by default: %+v", row)
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	if row, _ := model.selectedRow(); row.kind != rowSession || keyOf(row.session) != keyOf(items[1]) {
		t.Fatal("selection did not reach the manually opened recent session")
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'k', Text: "k"}))
	if row, _ := model.selectedRow(); row.kind != rowHeader {
		t.Fatal("selection did not move up onto a header")
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))

	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: '/'}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "API"}))
	if sessions := rowSessions(model.rows); len(sessions) != 1 || keyOf(sessions[0]) != keyOf(items[1]) {
		t.Fatalf("visible after search = %#v", model.rows)
	}
	model, command = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command != nil || model.searching || model.query != "API" {
		t.Fatalf("search Enter command=%v searching=%t query=%q", command, model.searching, model.query)
	}
	model, command = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil || keyOf(attached) != keyOf(items[1]) {
		t.Fatalf("attach command=%v session=%#v", command, attached)
	}
}

func initialCommands(command tea.Cmd) (collected collectUpdateMsg, hasCollection, hasBackgroundQuery bool) {
	message := command()
	batch, ok := message.(tea.BatchMsg)
	if !ok {
		return collectUpdateMsg{}, false, false
	}
	backgroundQuery := reflect.ValueOf(tea.RequestBackgroundColor).Pointer()
	for _, child := range batch {
		if reflect.ValueOf(child).Pointer() == backgroundQuery {
			hasBackgroundQuery = true
			continue
		}
		if update, ok := child().(collectUpdateMsg); ok {
			collected = update
			hasCollection = true
		}
	}
	return collected, hasCollection, hasBackgroundQuery
}

func TestModelSearchBackspaceRemovesOneRuneAndEscapeClearsQuery(t *testing.T) {
	model := readyModel()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: '/'}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "배치"}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	if model.query != "배" {
		t.Fatalf("query after Backspace = %q", model.query)
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if model.searching || model.query != "" {
		t.Fatalf("Escape searching=%t query=%q", model.searching, model.query)
	}
	if len(model.rows) != 3 {
		t.Fatalf("rows after Escape = %+v", model.rows)
	}
}

func TestModelEscapeInNormalModeClearsCommittedQuery(t *testing.T) {
	model := readyModel()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: '/'}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "API"}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if model.searching || model.query != "API" || len(rowSessions(model.rows)) != 1 {
		t.Fatalf("Enter searching=%t query=%q rows=%+v", model.searching, model.query, model.rows)
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if model.query != "" || len(model.rows) != 3 {
		t.Fatalf("Escape query=%q rows=%+v", model.query, model.rows)
	}
}

func TestModelGAndShiftGJumpToFirstAndLastRow(t *testing.T) {
	for _, bottom := range []tea.Key{
		{Code: 'g', Text: "G", Mod: tea.ModShift},
		{Code: 'G', Text: "G"},
	} {
		model := readyModel()
		model, _ = updateModel(model, tea.KeyPressMsg(bottom))
		if model.selected != len(model.rows)-1 {
			t.Fatalf("G %+v selected = %d, want %d", bottom, model.selected, len(model.rows)-1)
		}
		model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'g', Text: "g"}))
		if model.selected != 0 {
			t.Fatalf("g selected = %d, want 0", model.selected)
		}
	}
}

func TestModelPageKeysMoveByViewportWithoutWrapping(t *testing.T) {
	model := readyModel()
	model.result.Sessions = manySessions(30)
	model.refreshVisible()
	model, _ = updateModel(model, tea.WindowSizeMsg{Width: 120, Height: 14})
	top := model.selected

	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown}))
	if model.selected <= top {
		t.Fatalf("PgDn selected = %d, want > %d", model.selected, top)
	}
	first := model.selected
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'd', Mod: tea.ModCtrl}))
	if model.selected <= first {
		t.Fatalf("Ctrl+D selected = %d, want > %d", model.selected, first)
	}
	for range 10 {
		model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown}))
	}
	if model.selected != len(model.rows)-1 {
		t.Fatalf("PgDn clamp selected = %d, want %d", model.selected, len(model.rows)-1)
	}
	for range 20 {
		model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}))
	}
	if model.selected != 0 {
		t.Fatalf("PgUp clamp selected = %d, want 0", model.selected)
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	if model.selected != 0 {
		t.Fatalf("Ctrl+U at top selected = %d, want 0", model.selected)
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
	if model.selected != len(model.rows)-1 {
		t.Fatalf("End selected = %d, want %d", model.selected, len(model.rows)-1)
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}))
	if model.selected != 0 {
		t.Fatalf("Home selected = %d, want 0", model.selected)
	}
}

func TestModelCtrlUClearsQueryWhileSearching(t *testing.T) {
	model := readyModel()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: '/'}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "API"}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	if !model.searching || model.query != "" || len(model.rows) != 3 {
		t.Fatalf("Ctrl+U searching=%t query=%q rows=%d", model.searching, model.query, len(model.rows))
	}
}

func TestModelPageStepMatchesVisibleBodyHeight(t *testing.T) {
	model := readyModel()
	items := manySessions(40)
	for index := range items {
		items[index].CWD = fmt.Sprintf("/work/project-%02d", index/5)
	}
	model.result.Sessions = items
	model.refreshVisible()
	model, _ = updateModel(model, tea.WindowSizeMsg{Width: 120, Height: 20})
	start := model.selected

	// height 20 minus header(2) + list gap(1) + one detail line + no
	// search line + help block(2) leaves a 14-row session body.
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown}))
	if want := start + 14; model.selected != want {
		t.Fatalf("PgDn selected = %d, want %d", model.selected, want)
	}
}

func TestModelSearchFallbackSelectsFirstMatchingSession(t *testing.T) {
	model := readyModel()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'k', Text: "k"}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: '/'}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "API"}))
	row, ok := model.selectedRow()
	if !ok || row.kind != rowSession || row.session.Title != "API repair" {
		t.Fatalf("search fallback selection = %+v", row)
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	row, ok = model.selectedRow()
	if !ok || row.kind != rowSession {
		t.Fatalf("committed search selection = %+v", row)
	}
}

func manySessions(count int) []session.Session {
	items := make([]session.Session, 0, count)
	for index := range count {
		item := twoSessions()[1]
		item.NativeID = fmt.Sprintf("0195f5dc-9e3f-7c26-8000-%012d", index)
		item.Title = fmt.Sprintf("session %02d", index)
		item.Runtime.State = session.RuntimeRunning
		items = append(items, item)
	}
	return items
}

func TestModelRefreshCoalescesAndRejectsStaleGenerations(t *testing.T) {
	model := readyModel()
	model.generation = 1

	model, first := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	if first == nil || !model.collecting || model.generation != 2 {
		t.Fatalf("first refresh command=%v collecting=%t generation=%d", first, model.collecting, model.generation)
	}
	model, second := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	if second != nil || model.generation != 2 {
		t.Fatalf("coalesced refresh command=%v generation=%d", second, model.generation)
	}

	stale := Result{Sessions: []session.Session{twoSessions()[1]}}
	model, command := updateModel(model, collectUpdateMsg{generation: 1, update: Update{Result: stale, Done: true}})
	if command != nil || !model.collecting || len(model.result.Sessions) != 2 {
		t.Fatalf("stale collection changed model: %#v", model)
	}
	fresh := Result{Sessions: []session.Session{twoSessions()[0]}}
	model, _ = updateModel(model, collectUpdateMsg{generation: 2, update: Update{Result: fresh, Done: true}})
	if model.collecting || len(model.result.Sessions) != 1 || keyOf(model.result.Sessions[0]) != keyOf(fresh.Sessions[0]) {
		t.Fatalf("fresh collection not applied: %#v", model)
	}
}

func TestModelRefreshPreservesCanonicalSelection(t *testing.T) {
	model := readyModel()
	selected := twoSessions()[1]
	model.selectedRef = rowRef{kind: rowSession, project: session.Project(selected.CWD), key: keyOf(selected)}
	model.selected = 3

	changed := selected
	changed.Title = "renamed row"
	changed.CWD = "/renamed/project"
	result := Result{Sessions: []session.Session{changed, twoSessions()[0]}}
	model.groupMode = map[string]groupMode{"project": groupModeOpen}
	model.collecting = true
	model.generation = 2
	model, _ = updateModel(model, collectUpdateMsg{generation: 2, update: Update{Result: result, Done: true}})
	row, ok := model.selectedRow()
	if !ok || row.kind != rowSession || keyOf(row.session) != keyOf(selected) {
		t.Fatalf("selection row=%+v index=%d", row, model.selected)
	}
}

func TestModelCursorCoversHeadersAndSessions(t *testing.T) {
	value := readyModel()
	kinds := make([]rowKind, 0, len(value.rows))
	for _, row := range value.rows {
		kinds = append(kinds, row.kind)
	}
	want := []rowKind{rowHeader, rowSession, rowHeader}
	if len(kinds) != len(want) {
		t.Fatalf("rows = %+v", value.rows)
	}
	for index := range want {
		if kinds[index] != want[index] {
			t.Fatalf("row kinds = %v, want %v", kinds, want)
		}
	}
	if value.selected != 1 {
		t.Fatalf("initial selection = %d, want first session row", value.selected)
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'k', Text: "k"}))
	if row, _ := value.selectedRow(); row.kind != rowHeader || row.project != "ars" {
		t.Fatalf("k did not land on header: %+v", row)
	}
}

func TestModelEnterOnHeaderTogglesWithoutAttaching(t *testing.T) {
	attached := 0
	value := readyModel()
	value.deps.Attach = func(context.Context, session.Session) (ExecCommand, error) {
		attached++
		return &fakeExecCommand{}, nil
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'k', Text: "k"}))
	value, command := updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command != nil || attached != 0 {
		t.Fatal("enter on header attached")
	}
	if len(value.rows) != 2 || !value.rows[0].collapsed {
		t.Fatalf("group did not collapse: %+v", value.rows)
	}
	if row, _ := value.selectedRow(); row.kind != rowHeader || row.project != "ars" {
		t.Fatalf("selection left the toggled header: %+v", row)
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	if len(value.rows) != 3 {
		t.Fatalf("space did not expand: %+v", value.rows)
	}
}

func TestModelCollapseSurvivesRefreshAndSearchOverridesIt(t *testing.T) {
	value := readyModel()
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'k', Text: "k"}))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	value, command := updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	value, _ = updateModel(value, command().(collectUpdateMsg))
	if len(value.rows) != 2 || !value.rows[0].collapsed {
		t.Fatalf("collapse lost on refresh: %+v", value.rows)
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: '/'}))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "connection"}))
	if len(value.rows) != 2 || value.rows[1].session.Title != "connection check" {
		t.Fatalf("search did not override collapse: %+v", value.rows)
	}
	for range "connection" {
		value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if len(value.rows) != 2 || !value.rows[0].collapsed {
		t.Fatalf("collapse not restored after search: %+v", value.rows)
	}
}

func TestModelSelectionFallsBackToHeaderWhenRowVanishes(t *testing.T) {
	value := readyModel()
	if header := value.rows[0]; header.project != "ars" {
		t.Fatalf("unexpected first group: %+v", header)
	}
	value.toggle("ars")
	if row, _ := value.selectedRow(); row.kind != rowHeader || row.project != "ars" {
		t.Fatalf("selection did not fall back to header: %+v", row)
	}
}

func mixedProjectSessions() []session.Session {
	items := twoSessions()
	items[1].CWD = items[0].CWD
	return items
}

func TestModelEnterOnMoreRowRevealsRecentSessionsWithoutAttaching(t *testing.T) {
	value := readyModel()
	attached := 0
	value.deps.Attach = func(context.Context, session.Session) (ExecCommand, error) {
		attached++
		return &fakeExecCommand{}, nil
	}
	value.result.Sessions = mixedProjectSessions()
	value.refreshVisible()
	if len(value.rows) != 3 || value.rows[2].kind != rowMore || value.rows[2].count != 1 {
		t.Fatalf("auto rows = %+v, want header, active, more", value.rows)
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	if row, _ := value.selectedRow(); row.kind != rowMore {
		t.Fatalf("selection did not reach more row: %+v", row)
	}
	value, command := updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command != nil || attached != 0 {
		t.Fatal("enter on more row attached")
	}
	if len(value.rows) != 3 || value.rows[2].kind != rowSession {
		t.Fatalf("more row did not open group: %+v", value.rows)
	}
	if row, _ := value.selectedRow(); row.kind != rowSession || keyOf(row.session) != keyOf(mixedProjectSessions()[1]) {
		t.Fatalf("selection did not land on first revealed session: %+v", row)
	}
}

func TestModelSpaceOnMoreRowOpensGroup(t *testing.T) {
	value := readyModel()
	value.result.Sessions = mixedProjectSessions()
	value.refreshVisible()
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	if len(value.rows) != 3 || value.rows[2].kind != rowSession {
		t.Fatalf("space on more row did not open group: %+v", value.rows)
	}
}

func TestModelHeaderToggleClosesAutoPartialThenOpensFull(t *testing.T) {
	value := readyModel()
	value.result.Sessions = mixedProjectSessions()
	value.refreshVisible()
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'k', Text: "k"}))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if len(value.rows) != 1 || !value.rows[0].collapsed {
		t.Fatalf("auto-partial header did not close: %+v", value.rows)
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if len(value.rows) != 3 || value.rows[1].kind != rowSession || value.rows[2].kind != rowSession {
		t.Fatalf("closed header did not open fully: %+v", value.rows)
	}
}

func TestModelAttachCompletionStoresBoundedStatusAndCollectsExactlyOnce(t *testing.T) {
	collects := 0
	model := readyModel()
	model.deps.Collect = func(context.Context) <-chan Update {
		collects++
		channel := make(chan Update, 1)
		channel <- Update{Done: true}
		close(channel)
		return channel
	}
	want := errors.New(strings.Repeat("attach failed ", 100))
	model, command := updateModel(model, attachDoneMsg{err: want})
	if command == nil || !model.collecting || model.generation != 2 {
		t.Fatalf("attach completion command=%v collecting=%t generation=%d", command, model.collecting, model.generation)
	}
	if model.status == "" || len(model.status) > maxStatusBytes {
		t.Fatalf("bounded status length=%d status=%q", len(model.status), model.status)
	}
	message, ok := command().(collectUpdateMsg)
	if !ok || message.generation != 2 || collects != 1 {
		t.Fatalf("refresh message=%#v collects=%d", message, collects)
	}
}

func TestModelAttachCompletionSupersedesCollectionInFlight(t *testing.T) {
	model := readyModel()
	model.collecting = true
	model.generation = 2
	model, command := updateModel(model, attachDoneMsg{})
	if command == nil || !model.collecting || model.generation != 3 {
		t.Fatalf("attach completion command=%v collecting=%t generation=%d", command, model.collecting, model.generation)
	}
	message, ok := command().(collectUpdateMsg)
	if !ok || message.generation != 3 {
		t.Fatalf("refresh message = %#v", message)
	}
}

func TestModelAppliesIncrementalUpdatesAndStaleHostsUntilDone(t *testing.T) {
	items := twoSessions()
	model := readyModel()
	model.collecting = true
	model.generation = 2

	channel := make(chan Update, 2)
	partial := Update{Result: Result{Sessions: items}, Stale: []string{"server"}}
	model, command := updateModel(model, collectUpdateMsg{generation: 2, update: partial, channel: channel})
	if command == nil || !model.collecting {
		t.Fatalf("partial update command=%v collecting=%t", command, model.collecting)
	}
	if _, ok := model.stale["server"]; !ok || len(model.stale) != 1 {
		t.Fatalf("stale set = %#v", model.stale)
	}

	final := Update{Result: Result{Sessions: items[:1]}, Done: true}
	model, _ = updateModel(model, collectUpdateMsg{generation: 2, update: final, channel: channel})
	if model.collecting || len(model.stale) != 0 || len(model.result.Sessions) != 1 {
		t.Fatalf("final update not applied: collecting=%t stale=%#v", model.collecting, model.stale)
	}
}

func TestModelRestartCancelsPreviousCollectionContext(t *testing.T) {
	model := readyModel()
	var firstCtx context.Context
	model.deps.Collect = func(ctx context.Context) <-chan Update {
		if firstCtx == nil {
			firstCtx = ctx
		}
		channel := make(chan Update)
		go func() {
			<-ctx.Done()
			close(channel)
		}()
		return channel
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	if firstCtx == nil {
		t.Fatal("refresh did not start a collection")
	}
	model.collecting = false
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	select {
	case <-firstCtx.Done():
	default:
		t.Fatal("second refresh did not cancel the first collection context")
	}
	_ = model
}

func TestModelResizeAndQuit(t *testing.T) {
	model := readyModel()
	model, _ = updateModel(model, tea.WindowSizeMsg{Width: 80, Height: 20})
	if model.width != 80 || model.height != 20 {
		t.Fatalf("size = %dx%d", model.width, model.height)
	}
	_, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'q', Text: "q"}))
	if command == nil {
		t.Fatal("q did not quit")
	}
}

func TestModelQuitCancelsCollectionContext(t *testing.T) {
	model := readyModel()
	var capturedCtx context.Context
	model.deps.Collect = func(ctx context.Context) <-chan Update {
		capturedCtx = ctx
		channel := make(chan Update)
		go func() {
			<-ctx.Done()
			close(channel)
		}()
		return channel
	}
	model, _ = model.restartCollection()
	if capturedCtx == nil {
		t.Fatal("restartCollection did not start a collection")
	}
	_, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'q', Text: "q"}))
	if command == nil {
		t.Fatal("q did not quit")
	}
	select {
	case <-capturedCtx.Done():
	default:
		t.Fatal("quitting with 'q' did not cancel the in-flight collection context")
	}
}

func staticCollect(result Result) func(context.Context) <-chan Update {
	return func(context.Context) <-chan Update {
		channel := make(chan Update, 1)
		channel <- Update{Result: result, Done: true}
		close(channel)
		return channel
	}
}

func readyModel() model {
	result := Result{Sessions: twoSessions()}
	deps := Dependencies{
		Collect:     staticCollect(result),
		Attach:      func(context.Context, session.Session) (ExecCommand, error) { return &fakeExecCommand{}, nil },
		LocalTarget: "localhost",
		Now:         func() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) },
		NoColor:     true,
	}
	value := newModel(context.Background(), deps)
	message, hasCollection, _ := initialCommands(value.Init())
	if !hasCollection {
		panic("readyModel: Init did not produce collectUpdateMsg")
	}
	value, _ = updateModel(value, message)
	return value
}

func twoSessions() []session.Session {
	return []session.Session{
		{
			Host: "localhost",
			Candidate: session.Candidate{
				Provider:  session.Claude,
				NativeID:  "123e4567-e89b-42d3-a456-426614174000",
				UpdatedAt: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
				CWD:       "/work/ars",
				Title:     "connection check",
			},
			Runtime: session.Runtime{
				State:           session.RuntimeAttached,
				AttachedClients: 1,
				StartedAt:       time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC),
			},
		},
		{
			Host: "server",
			Candidate: session.Candidate{
				Provider:  session.Codex,
				NativeID:  "0195f5dc-9e3f-7c26-8000-0123456789ab",
				UpdatedAt: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC),
				CWD:       "/work/api",
				Title:     "API repair",
			},
			Runtime: session.Runtime{State: session.RuntimeSaved},
		},
	}
}

func hostError(host, code, message string) output.HostError {
	return output.HostError{Host: host, Code: code, Message: message}
}

type fakeExecCommand struct{}

func (*fakeExecCommand) Run() error          { return nil }
func (*fakeExecCommand) SetStdin(io.Reader)  {}
func (*fakeExecCommand) SetStdout(io.Writer) {}
func (*fakeExecCommand) SetStderr(io.Writer) {}
