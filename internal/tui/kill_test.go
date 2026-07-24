package tui

import (
	"context"
	"errors"
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

func TestModelXOnHeaderOrMoreRowIsNoop(t *testing.T) {
	model := readyModel()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'k', Text: "k"}))
	row, ok := model.selectedRow()
	if !ok || row.kind != rowHeader {
		t.Fatalf("expected header selected: %+v", row)
	}
	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	if command != nil || model.status != "" {
		t.Fatalf("x on header command=%v status=%q, want no-op", command, model.status)
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
	want := "killed " + sessionTitle(row.session)
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
	model, command = updateModel(model, done)
	if command != nil {
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
	want := model.errorText(model.status, 80)
	if got != want {
		t.Fatalf("kill failed status rendered = %q, want error-styled %q", got, want)
	}
	if muted := model.mutedText(model.status, 80); got == muted {
		t.Fatal("kill failed status rendered muted instead of error-styled")
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
	if !strings.Contains(overlay, "kill session (3s grace · u undo)") {
		t.Fatalf("help overlay missing kill binding:\n%s", overlay)
	}
}
