package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

func TestModelXRegistersPendingKillWithGraceStatus(t *testing.T) {
	model := readyModel()
	row, ok := model.selectedRow()
	if !ok || row.kind != rowSession || row.session.Runtime.State == session.RuntimeSaved {
		t.Fatalf("expected initial selection to be a live session: %+v", row)
	}
	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	if command == nil {
		t.Fatal("x on a live session did not schedule the grace-period tick")
	}
	want := "killing " + sessionTitle(row.session) + " in 3s · u undo"
	if model.status != want {
		t.Fatalf("status = %q, want %q", model.status, want)
	}
}

func TestModelXOnSavedSessionShowsNoLiveSessionStatus(t *testing.T) {
	model := readyModel()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	row, ok := model.selectedRow()
	if !ok || row.kind != rowSession || row.session.Runtime.State != session.RuntimeSaved {
		t.Fatalf("expected a saved session selected: %+v", row)
	}
	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	if command != nil {
		t.Fatal("x on a saved session scheduled a command")
	}
	if model.status != "no live session to kill" {
		t.Fatalf("status = %q, want %q", model.status, "no live session to kill")
	}
}

func TestModelXOnMoreRowIsNoop(t *testing.T) {
	model := readyModel()
	// Auto mode hides the saved sessions of a group that also has a live one
	// behind a more row.
	sessions := manySessions(3)
	sessions[1].Runtime.State = session.RuntimeSaved
	sessions[2].Runtime.State = session.RuntimeSaved
	model.result.Sessions = sessions
	model.refreshVisible()
	index := -1
	for position, row := range model.rows {
		if row.kind == rowMore {
			index = position
			break
		}
	}
	if index < 0 {
		t.Fatalf("test setup produced no more row: %+v", model.rows)
	}
	model.selectRow(index)
	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	if command != nil || model.status != "" {
		t.Fatalf("x on more row command=%v status=%q, want no-op", command, model.status)
	}
}

func TestModelUCancelsPendingKill(t *testing.T) {
	model := readyModel()
	row, _ := model.selectedRow()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'u', Text: "u"}))
	if command != nil {
		t.Fatal("u after pending kill produced an unexpected command")
	}
	if model.status != "kill canceled" {
		t.Fatalf("status = %q, want %q", model.status, "kill canceled")
	}
	_ = row
}

func TestModelUWithoutPendingKillIsNoop(t *testing.T) {
	model := readyModel()
	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'u', Text: "u"}))
	if command != nil || model.status != "" {
		t.Fatalf("u without pending kill command=%v status=%q, want no-op", command, model.status)
	}
}

func TestModelKillFireAfterUndoIsNoop(t *testing.T) {
	killed := 0
	model := readyModel()
	model.deps.Kill = func(context.Context, session.Session) error {
		killed++
		return nil
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	seq := model.killSeq
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'u', Text: "u"}))
	model, command := updateModel(model, killFireMsg{seq: seq})
	if command != nil {
		t.Fatal("stale killFireMsg after undo produced a command")
	}
	if killed != 0 {
		t.Fatalf("Kill invoked %d times after undo, want 0", killed)
	}
}

func TestModelKillFireInvokesDepsKillAndSucceeds(t *testing.T) {
	var killedSession session.Session
	collects := 0
	model := readyModel()
	model.deps.Kill = func(_ context.Context, item session.Session) error {
		killedSession = item
		return nil
	}
	model.deps.Collect = func(context.Context) <-chan Update {
		collects++
		channel := make(chan Update, 1)
		channel <- Update{Done: true}
		close(channel)
		return channel
	}
	row, _ := model.selectedRow()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	seq := model.killSeq

	model, command := updateModel(model, killFireMsg{seq: seq})
	if command == nil {
		t.Fatal("killFireMsg did not produce a command to run deps.Kill")
	}
	message := command()
	done, ok := message.(killDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("killFireMsg command result = %#v, want killDoneMsg with nil err", message)
	}
	if keyOf(killedSession) != keyOf(row.session) {
		t.Fatalf("Kill invoked with %#v, want %#v", killedSession, row.session)
	}

	model, command = updateModel(model, done)
	if command == nil {
		t.Fatal("killDoneMsg did not restart collection")
	}
	want := "killed " + sessionTitle(row.session) + " · enter to resume"
	if model.status != want {
		t.Fatalf("status = %q, want %q", model.status, want)
	}
	if collects != 1 {
		t.Fatalf("collects = %d, want 1", collects)
	}
}

func TestModelKillFireReportsFailureStatus(t *testing.T) {
	model := readyModel()
	model.deps.Kill = func(context.Context, session.Session) error {
		return errors.New("boom")
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	seq := model.killSeq
	model, command := updateModel(model, killFireMsg{seq: seq})
	message := command()
	done := message.(killDoneMsg)
	generation := model.generation
	model, _ = updateModel(model, done)
	// The error status now arms an auto-dismiss tick, so a command is
	// expected; what must not happen is a collection restart (unchanged
	// generation is restartCollection's tell, per model.go).
	if model.generation != generation {
		t.Fatal("failed kill should not restart collection")
	}
	want := "kill failed: boom"
	if model.status != want {
		t.Fatalf("status = %q, want %q", model.status, want)
	}
}

func TestModelNewXReplacesPendingKill(t *testing.T) {
	model := readyModel()
	model.result.Sessions = manySessions(2)
	model.refreshVisible()
	first, _ := model.selectedRow()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	firstSeq := model.killSeq

	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	second, _ := model.selectedRow()
	if keyOf(second.session) == keyOf(first.session) {
		t.Fatal("test setup did not move selection to a different session")
	}
	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	if command == nil {
		t.Fatal("second x did not schedule a new grace-period tick")
	}
	if model.killSeq == firstSeq {
		t.Fatal("second x did not bump killSeq")
	}
	want := "killing " + sessionTitle(second.session) + " in 3s · u undo"
	if model.status != want {
		t.Fatalf("status = %q, want %q", model.status, want)
	}

	// The stale first pending must not fire.
	model, command = updateModel(model, killFireMsg{seq: firstSeq})
	if command != nil {
		t.Fatal("stale first pending kill fired after being replaced")
	}
}

func TestModelRestartCollectionClearsPendingKill(t *testing.T) {
	model := readyModel()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	seq := model.killSeq
	model, _ = model.restartCollection()
	model, command := updateModel(model, killFireMsg{seq: seq})
	if command != nil {
		t.Fatal("killFireMsg fired after restartCollection cleared the pending kill")
	}
}

func TestModelXAndUAreLiteralWhileSearching(t *testing.T) {
	model := readyModel()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: '/'}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "xu"}))
	if model.query != "xu" {
		t.Fatalf("query while searching = %q, want literal xu", model.query)
	}
	if model.killSeq != 0 || model.status != "" {
		t.Fatalf("x/u while searching mutated kill state: seq=%d status=%q", model.killSeq, model.status)
	}
}

func TestModelKillDoneStaleDoesNotStompNewerPendingKill(t *testing.T) {
	killed := 0
	model := readyModel()
	model.result.Sessions = manySessions(2)
	model.refreshVisible()
	model.deps.Kill = func(context.Context, session.Session) error {
		killed++
		return nil
	}
	collects := 0
	model.deps.Collect = func(context.Context) <-chan Update {
		collects++
		channel := make(chan Update, 1)
		channel <- Update{Done: true}
		close(channel)
		return channel
	}

	// A fires first (slow async Kill still in flight).
	firstRow, _ := model.selectedRow()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	firstSeq := model.killSeq

	// Before A's killDoneMsg arrives, the user selects a different session and
	// presses x again: B becomes the newer (and only) pending kill.
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	secondRow, _ := model.selectedRow()
	if keyOf(secondRow.session) == keyOf(firstRow.session) {
		t.Fatal("test setup did not move selection to a different session")
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	secondSeq := model.killSeq
	if secondSeq == firstSeq {
		t.Fatal("second x did not bump killSeq")
	}
	wantArmedStatus := "killing " + sessionTitle(secondRow.session) + " in 3s · u undo"
	if model.status != wantArmedStatus {
		t.Fatalf("status before stale done = %q, want %q", model.status, wantArmedStatus)
	}

	// A's stale killDoneMsg now arrives.
	model, command := updateModel(model, killDoneMsg{seq: firstSeq, title: sessionTitle(firstRow.session), err: nil})

	// B's pending kill must still be armed: status restored, killPending true,
	// and the timer for B (secondSeq) must still fire.
	if !model.killPending || model.killSeq != secondSeq {
		t.Fatalf("stale done cleared the newer pending kill: killPending=%t killSeq=%d, want pending for seq %d", model.killPending, model.killSeq, secondSeq)
	}
	if model.status != wantArmedStatus {
		t.Fatalf("stale done overwrote newer pending status: got %q, want %q", model.status, wantArmedStatus)
	}
	// The stale completion still triggers its own refresh (the old session
	// really did die), so a restartCollection command is expected here.
	if command == nil {
		t.Fatal("stale killDoneMsg did not restart collection for its own outcome")
	}
	model, _ = updateModel(model, collectionFrom(command))

	// B's own grace-period timer then fires and must still kill B.
	model, command = updateModel(model, killFireMsg{seq: secondSeq})
	if command == nil {
		t.Fatal("B's killFireMsg produced no command after surviving the stale done")
	}
	message := command()
	done, ok := message.(killDoneMsg)
	if !ok || done.err != nil || done.seq != secondSeq {
		t.Fatalf("B's kill result = %#v, want killDoneMsg{seq: %d, err: nil}", message, secondSeq)
	}
	if killed != 1 {
		t.Fatalf("Kill invoked %d times, want 1 (only for B)", killed)
	}
}

func TestModelStaleKillFailureDoesNotStompNewerPendingKill(t *testing.T) {
	model := readyModel()
	model.result.Sessions = manySessions(2)
	model.refreshVisible()

	firstRow, _ := model.selectedRow()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	firstSeq := model.killSeq

	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	secondRow, _ := model.selectedRow()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	secondSeq := model.killSeq
	wantArmedStatus := "killing " + sessionTitle(secondRow.session) + " in 3s · u undo"

	model, command := updateModel(model, killDoneMsg{seq: firstSeq, title: sessionTitle(firstRow.session), err: errors.New("boom")})
	if !model.killPending || model.killSeq != secondSeq || model.status != wantArmedStatus {
		t.Fatalf("stale failed done disturbed newer pending: killPending=%t killSeq=%d status=%q", model.killPending, model.killSeq, model.status)
	}
	if command == nil {
		t.Fatal("stale failed killDoneMsg did not restart collection for its own outcome")
	}
}

func TestModelKillFailedStatusIsErrorStyled(t *testing.T) {
	model := readyModel()
	model.noColor = false
	model.styles = newViewStyles(true)
	model.deps.Kill = func(context.Context, session.Session) error {
		return errors.New("boom")
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	seq := model.killSeq
	model, command := updateModel(model, killFireMsg{seq: seq})
	done := command().(killDoneMsg)
	model, _ = updateModel(model, done)

	lines := model.diagnostics(80)
	if len(lines) == 0 {
		t.Fatal("diagnostics() returned no lines for kill failed status")
	}
	got := lines[len(lines)-1]
	// The countdown suffix is rendered-only (model.status itself stays bare),
	// so the expectation must include it too.
	want := model.errorText(model.status+" · 5s", 80)
	if got != want {
		t.Fatalf("kill failed status rendered = %q, want error-styled %q", got, want)
	}
	if muted := model.mutedText(model.status, 80); got == muted {
		t.Fatal("kill failed status rendered muted instead of error-styled")
	}
}

// groupKillModel puts three sessions in one project — two live, one saved —
// and selects that project's header row.
func groupKillModel(t *testing.T) model {
	t.Helper()
	value := readyModel()
	sessions := manySessions(3)
	sessions[2].Runtime.State = session.RuntimeSaved
	value.result.Sessions = sessions
	value.refreshVisible()
	project := session.Project(sessions[0].CWD)
	value.selectHeader(project)
	row, ok := value.selectedRow()
	if !ok || row.kind != rowHeader || row.project != project {
		t.Fatalf("expected the %q header selected: %+v", project, row)
	}
	return value
}

func liveTitles(items []session.Session) []string {
	titles := make([]string, 0, len(items))
	for _, item := range items {
		if item.Runtime.State != session.RuntimeSaved {
			titles = append(titles, sessionTitle(item))
		}
	}
	return titles
}

func TestModelXOnHeaderArmsGroupKillWithLiveSessions(t *testing.T) {
	model := readyModel()
	sessions := manySessions(2)
	for index := range sessions {
		sessions[index].Host = "localhost"
	}
	model.result.Sessions = sessions
	model.refreshVisible()
	project := session.Project(sessions[0].CWD)
	model.selectHeader(project)
	row, ok := model.selectedRow()
	if !ok || row.kind != rowHeader || row.project != project {
		t.Fatalf("expected the %q header selected: %+v", project, row)
	}

	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	want := "killing 2 sessions in " + project + " in 3s · u undo"
	if model.status != want {
		t.Fatalf("status = %q, want %q", model.status, want)
	}
}

func TestModelXOnHeaderTargetsSameProjectAcrossHosts(t *testing.T) {
	model := readyModel()
	sessions := manySessions(2)
	sessions[0].Host = "localhost"
	sessions[1].Host = "server"
	model.result.Sessions = sessions
	model.refreshVisible()
	project := session.Project(sessions[0].CWD)
	model.selectHeader(project)

	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	if len(model.killTargets) != 2 {
		t.Fatalf("killTargets = %+v, want both hosts in the project group", model.killTargets)
	}
	if model.killTargets[0].Host != "localhost" || model.killTargets[1].Host != "server" {
		t.Fatalf("kill target hosts = %q, %q, want localhost and server", model.killTargets[0].Host, model.killTargets[1].Host)
	}
	want := "killing 2 sessions in " + project + " in 3s · u undo"
	if model.status != want {
		t.Fatalf("status = %q, want %q", model.status, want)
	}
}

func TestModelXOnHeaderArmsGroupKillWithLiveSessionsOnly(t *testing.T) {
	model := groupKillModel(t)
	row, _ := model.selectedRow()

	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	if command == nil {
		t.Fatal("x on a group header did not schedule the grace-period tick")
	}
	if !model.killPending {
		t.Fatal("x on a group header did not arm a pending kill")
	}
	if len(model.killTargets) != 2 {
		t.Fatalf("killTargets = %d sessions, want the 2 live ones: %+v", len(model.killTargets), model.killTargets)
	}
	for _, target := range model.killTargets {
		if target.Runtime.State == session.RuntimeSaved {
			t.Fatalf("saved session %q included in the batch", sessionTitle(target))
		}
	}
	want := "killing 2 sessions in " + row.project + " in 3s · u undo"
	if model.status != want {
		t.Fatalf("status = %q, want %q", model.status, want)
	}
}

func TestModelUCancelsWholeGroupKill(t *testing.T) {
	killed := 0
	model := groupKillModel(t)
	model.deps.Kill = func(context.Context, session.Session) error {
		killed++
		return nil
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	seq := model.killSeq

	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'u', Text: "u"}))
	if command != nil {
		t.Fatal("u after a pending group kill produced an unexpected command")
	}
	if model.killPending || len(model.killTargets) != 0 {
		t.Fatalf("u left the batch armed: killPending=%t targets=%+v", model.killPending, model.killTargets)
	}
	if model.status != "kill canceled" {
		t.Fatalf("status = %q, want %q", model.status, "kill canceled")
	}

	model, command = updateModel(model, killFireMsg{seq: seq})
	if command != nil {
		t.Fatal("canceled group kill still fired")
	}
	if killed != 0 {
		t.Fatalf("Kill invoked %d times after undo, want 0", killed)
	}
}

func TestModelGroupKillFireKillsEveryMember(t *testing.T) {
	var killedTitles []string
	collects := 0
	model := groupKillModel(t)
	model.deps.Kill = func(_ context.Context, item session.Session) error {
		killedTitles = append(killedTitles, sessionTitle(item))
		return nil
	}
	model.deps.Collect = func(context.Context) <-chan Update {
		collects++
		channel := make(chan Update, 1)
		channel <- Update{Done: true}
		close(channel)
		return channel
	}
	row, _ := model.selectedRow()
	wantTitles := liveTitles(model.result.Sessions)

	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	seq := model.killSeq
	model, command := updateModel(model, killFireMsg{seq: seq})
	if command == nil {
		t.Fatal("group killFireMsg did not produce a command to run deps.Kill")
	}
	message := command()
	done, ok := message.(killDoneMsg)
	if !ok || done.err != nil || done.failed != 0 {
		t.Fatalf("group kill result = %#v, want killDoneMsg with no failures", message)
	}
	if len(killedTitles) != len(wantTitles) {
		t.Fatalf("Kill invoked for %v, want %v", killedTitles, wantTitles)
	}
	for _, title := range wantTitles {
		if !slices.Contains(killedTitles, title) {
			t.Fatalf("Kill never invoked for %q, got %v", title, killedTitles)
		}
	}

	model, command = updateModel(model, done)
	if command == nil {
		t.Fatal("group killDoneMsg did not restart collection")
	}
	want := "killed 2 sessions in " + row.project + " · enter to resume"
	if model.status != want {
		t.Fatalf("status = %q, want %q", model.status, want)
	}
	if collects != 1 {
		t.Fatalf("collects = %d, want 1", collects)
	}
}

func TestModelGroupKillPartialFailureSurfacesInStatus(t *testing.T) {
	model := groupKillModel(t)
	first := true
	model.deps.Kill = func(context.Context, session.Session) error {
		if first {
			first = false
			return errors.New("boom")
		}
		return nil
	}
	row, _ := model.selectedRow()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	seq := model.killSeq
	model, command := updateModel(model, killFireMsg{seq: seq})
	done := command().(killDoneMsg)
	if done.failed != 1 {
		t.Fatalf("done.failed = %d, want 1", done.failed)
	}

	model, command = updateModel(model, done)
	want := "kill failed: 1 of 2 sessions in " + row.project + ": boom"
	if model.status != want {
		t.Fatalf("status = %q, want %q", model.status, want)
	}
	// One member really did die, so the inventory needs a refresh even though
	// the batch reports a failure.
	if command == nil {
		t.Fatal("a partly successful batch did not restart collection")
	}
}

func TestModelGroupKillTotalFailureDoesNotRestartCollection(t *testing.T) {
	model := groupKillModel(t)
	model.deps.Kill = func(context.Context, session.Session) error {
		return errors.New("boom")
	}
	row, _ := model.selectedRow()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	seq := model.killSeq
	model, command := updateModel(model, killFireMsg{seq: seq})
	done := command().(killDoneMsg)

	generation := model.generation
	model, _ = updateModel(model, done)
	// The error status now arms an auto-dismiss tick, so a command is
	// expected; what must not happen is a collection restart (unchanged
	// generation is restartCollection's tell, per model.go).
	if model.generation != generation {
		t.Fatal("a wholly failed batch should not restart collection")
	}
	want := "kill failed: 2 of 2 sessions in " + row.project + ": boom"
	if model.status != want {
		t.Fatalf("status = %q, want %q", model.status, want)
	}
}

func TestModelGroupKillFailedStatusIsErrorStyled(t *testing.T) {
	model := groupKillModel(t)
	model.noColor = false
	model.styles = newViewStyles(true)
	model.deps.Kill = func(context.Context, session.Session) error {
		return errors.New("boom")
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	seq := model.killSeq
	model, command := updateModel(model, killFireMsg{seq: seq})
	done := command().(killDoneMsg)
	model, _ = updateModel(model, done)

	lines := model.diagnostics(80)
	if len(lines) == 0 {
		t.Fatal("diagnostics() returned no lines for the group kill failed status")
	}
	got := lines[len(lines)-1]
	// The countdown suffix is rendered-only (model.status itself stays bare),
	// so the expectation must include it too.
	want := model.errorText(model.status+" · 5s", 80)
	if got != want {
		t.Fatalf("group kill failed status rendered = %q, want error-styled %q", got, want)
	}
	if muted := model.mutedText(model.status, 80); got == muted {
		t.Fatal("group kill failed status rendered muted instead of error-styled")
	}
}

func TestModelHeaderXReplacesPendingSingleKill(t *testing.T) {
	killed := 0
	model := groupKillModel(t)
	model.deps.Kill = func(context.Context, session.Session) error {
		killed++
		return nil
	}
	// Arm a single session first, then re-arm as a group batch from the header.
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	single, _ := model.selectedRow()
	if single.kind != rowSession {
		t.Fatalf("expected a session row after j: %+v", single)
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	singleSeq := model.killSeq

	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'k', Text: "k"}))
	header, _ := model.selectedRow()
	if header.kind != rowHeader {
		t.Fatalf("expected the header row after k: %+v", header)
	}
	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	if command == nil {
		t.Fatal("header x did not schedule a new grace-period tick")
	}
	batchSeq := model.killSeq
	if batchSeq == singleSeq {
		t.Fatal("header x did not bump killSeq")
	}
	wantLabel := header.project
	if len(model.killTargets) != 2 || model.killGroup != wantLabel {
		t.Fatalf("header x did not replace the single target with the batch: targets=%+v group=%q", model.killTargets, model.killGroup)
	}
	want := "killing 2 sessions in " + wantLabel + " in 3s · u undo"
	if model.status != want {
		t.Fatalf("status = %q, want %q", model.status, want)
	}

	// The replaced single kill must not fire.
	model, command = updateModel(model, killFireMsg{seq: singleSeq})
	if command != nil {
		t.Fatal("the replaced single pending kill fired after the header re-armed a batch")
	}

	// u cancels the batch that replaced it, leaving nothing armed at all.
	model, command = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'u', Text: "u"}))
	if command != nil {
		t.Fatal("u after the batch produced an unexpected command")
	}
	if model.killPending || len(model.killTargets) != 0 {
		t.Fatalf("u left something armed: pending=%t targets=%+v", model.killPending, model.killTargets)
	}
	model, command = updateModel(model, killFireMsg{seq: batchSeq})
	if command != nil {
		t.Fatal("the canceled batch fired")
	}
	if killed != 0 {
		t.Fatalf("Kill invoked %d times, want 0 — every armed kill was replaced or canceled", killed)
	}
}

func TestModelStaleGroupKillDoneDoesNotStompNewerPendingKill(t *testing.T) {
	model := groupKillModel(t)
	model.deps.Collect = func(context.Context) <-chan Update {
		channel := make(chan Update, 1)
		channel <- Update{Done: true}
		close(channel)
		return channel
	}
	row, _ := model.selectedRow()

	// The group batch is armed first, then replaced by a newer single kill.
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	firstSeq := model.killSeq

	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	secondRow, _ := model.selectedRow()
	if secondRow.kind != rowSession {
		t.Fatalf("expected a session row after j: %+v", secondRow)
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	secondSeq := model.killSeq
	if secondSeq == firstSeq {
		t.Fatal("second x did not bump killSeq")
	}
	wantArmedStatus := "killing " + sessionTitle(secondRow.session) + " in 3s · u undo"
	if model.status != wantArmedStatus {
		t.Fatalf("status before stale done = %q, want %q", model.status, wantArmedStatus)
	}

	// The replaced batch's completion arrives late.
	model, command := updateModel(model, killDoneMsg{seq: firstSeq, group: row.project, total: 2})
	if !model.killPending || model.killSeq != secondSeq {
		t.Fatalf("stale batch done cleared the newer pending kill: killPending=%t killSeq=%d", model.killPending, model.killSeq)
	}
	if len(model.killTargets) != 1 || keyOf(model.killTargets[0]) != keyOf(secondRow.session) {
		t.Fatalf("stale batch done replaced the newer target: %+v", model.killTargets)
	}
	if model.status != wantArmedStatus {
		t.Fatalf("stale batch done overwrote newer pending status: got %q, want %q", model.status, wantArmedStatus)
	}
	if command == nil {
		t.Fatal("stale batch killDoneMsg did not restart collection for its own outcome")
	}
	model, _ = updateModel(model, collectionFrom(command))

	// The newer single kill still fires.
	model, command = updateModel(model, killFireMsg{seq: secondSeq})
	if command == nil {
		t.Fatal("the newer pending kill produced no command after surviving the stale batch done")
	}
}

func TestModelXOnHeaderWithNoLiveSessionsIsNoop(t *testing.T) {
	model := readyModel()
	sessions := manySessions(2)
	for index := range sessions {
		sessions[index].Runtime.State = session.RuntimeSaved
	}
	model.result.Sessions = sessions
	model.refreshVisible()
	project := session.Project(sessions[0].CWD)
	model.selectHeader(project)
	row, ok := model.selectedRow()
	if !ok || row.kind != rowHeader || row.project != project {
		t.Fatalf("expected the %q header selected: %+v", project, row)
	}

	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	if command != nil {
		t.Fatal("x on an all-saved group scheduled a command")
	}
	if model.killPending {
		t.Fatal("x on an all-saved group armed a pending kill")
	}
	want := "no live sessions in " + project
	if model.status != want {
		t.Fatalf("status = %q, want %q", model.status, want)
	}
}

func TestModelGroupKillRespectsTheActiveSearchQuery(t *testing.T) {
	model := groupKillModel(t)
	// The query narrows the group to one live session, so the header stands for
	// that session alone — x must not reach the filtered-out live member.
	model.query = "session 00"
	model.refreshVisible()
	project := session.Project(model.result.Sessions[0].CWD)
	model.selectHeader(project)
	row, ok := model.selectedRow()
	if !ok || row.kind != rowHeader {
		t.Fatalf("expected the %q header selected: %+v", project, row)
	}

	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	if len(model.killTargets) != 1 {
		t.Fatalf("killTargets = %+v, want only the searched session", model.killTargets)
	}
	if got := sessionTitle(model.killTargets[0]); got != "session 00" {
		t.Fatalf("killTargets[0] = %q, want %q", got, "session 00")
	}
	want := "killing 1 sessions in " + project + " in 3s · u undo"
	if model.status != want {
		t.Fatalf("status = %q, want %q", model.status, want)
	}
}

func TestModelGroupKillRespectsTheActiveStateFilter(t *testing.T) {
	model := groupKillModel(t)
	// The saved-only filter hides every live member, so the header must offer
	// nothing to kill rather than killing sessions that are off screen.
	model.stateFilter = map[session.RuntimeState]bool{session.RuntimeSaved: true}
	model.refreshVisible()
	project := session.Project(model.result.Sessions[0].CWD)
	model.selectHeader(project)
	row, ok := model.selectedRow()
	if !ok || row.kind != rowHeader {
		t.Fatalf("expected the %q header selected: %+v", project, row)
	}

	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	if command != nil || model.killPending {
		t.Fatalf("x armed a kill for filtered-out live sessions: command=%v pending=%t", command, model.killPending)
	}
	if want := "no live sessions in " + project; model.status != want {
		t.Fatalf("status = %q, want %q", model.status, want)
	}
}

func TestModelGroupKillCountsCollapsedGroupMembers(t *testing.T) {
	model := groupKillModel(t)
	row, _ := model.selectedRow()
	// Collapse the group: x must still target every live member, not only the
	// rows currently on screen.
	model.toggle(row.project)
	collapsed, ok := model.selectedRow()
	if !ok || collapsed.kind != rowHeader || !collapsed.collapsed {
		t.Fatalf("expected a collapsed header selected: %+v", collapsed)
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	if len(model.killTargets) != 2 {
		t.Fatalf("collapsed group killTargets = %+v, want the 2 live sessions", model.killTargets)
	}
}

func TestHelpOverlayAndFooterAdvertiseKill(t *testing.T) {
	model := readyModel()
	model.width = 140
	content := ansi.Strip(model.help(model.contentWidth()))
	if !strings.Contains(content, "x kill") {
		t.Fatalf("footer help missing kill hint: %q", content)
	}

	model.showHelp = true
	overlay := ansi.Strip(model.View().Content)
	if !strings.Contains(overlay, "kill session / group (3s grace · u undo)") {
		t.Fatalf("help overlay missing kill binding:\n%s", overlay)
	}
}

// A completed kill drops the session to saved/recent, where enter resumes it.
// The success status is the only place that recovery is discoverable once the
// 3s undo window has passed, so both kill paths have to advertise it.
func TestKilledStatusHintsResume(t *testing.T) {
	single := killedStatus(killDoneMsg{title: "connection check", total: 1})
	if single != "killed connection check · enter to resume" {
		t.Fatalf("single kill status = %q, want the resume hint appended", single)
	}
	group := killedStatus(killDoneMsg{group: "ars", total: 2})
	if group != "killed 2 sessions in ars · enter to resume" {
		t.Fatalf("group kill status = %q, want the resume hint appended", group)
	}
}

// The hint belongs to success only: a failed or canceled kill leaves nothing to
// resume, so pointing at enter there would be wrong.
func TestFailedAndCanceledKillStatusesOmitResumeHint(t *testing.T) {
	failed := killFailedStatus(killDoneMsg{title: "connection check", total: 1, failed: 1, err: errors.New("boom")})
	if strings.Contains(failed, "enter to resume") {
		t.Fatalf("single failure status = %q, want no resume hint", failed)
	}
	batch := killFailedStatus(killDoneMsg{group: "ars", total: 2, failed: 1, err: errors.New("boom")})
	if strings.Contains(batch, "enter to resume") {
		t.Fatalf("batch failure status = %q, want no resume hint", batch)
	}

	model := readyModel()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'u', Text: "u"}))
	if strings.Contains(model.status, "enter to resume") {
		t.Fatalf("canceled kill status = %q, want no resume hint", model.status)
	}
}
