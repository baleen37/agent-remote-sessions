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
