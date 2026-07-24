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

func TestModelMEntersComposeOnLiveSession(t *testing.T) {
	model := readyModel()
	row, ok := model.selectedRow()
	if !ok || row.kind != rowSession || row.session.Runtime.State == session.RuntimeSaved {
		t.Fatalf("expected initial selection to be a live session: %+v", row)
	}
	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	if command != nil {
		t.Fatal("m entering compose produced an unexpected command")
	}
	if !model.composing || model.compose != "" {
		t.Fatalf("composing=%t compose=%q, want composing=true compose=\"\"", model.composing, model.compose)
	}
}

func TestModelMOnSavedSessionShowsNoLiveSessionStatus(t *testing.T) {
	model := readyModel()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	row, ok := model.selectedRow()
	if !ok || row.kind != rowSession || row.session.Runtime.State != session.RuntimeSaved {
		t.Fatalf("expected a saved session selected: %+v", row)
	}
	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	if command != nil {
		t.Fatal("m on a saved session scheduled a command")
	}
	if model.composing {
		t.Fatal("m on a saved session entered compose mode")
	}
	if model.status != "no live session" {
		t.Fatalf("status = %q, want %q", model.status, "no live session")
	}
}

func TestModelMOnHeaderOrMoreRowIsNoop(t *testing.T) {
	model := readyModel()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'k', Text: "k"}))
	row, ok := model.selectedRow()
	if !ok || row.kind != rowHeader {
		t.Fatalf("expected header selected: %+v", row)
	}
	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	if command != nil || model.composing || model.status != "" {
		t.Fatalf("m on header command=%v composing=%t status=%q, want no-op", command, model.composing, model.status)
	}
}

func TestModelComposeEscCancels(t *testing.T) {
	model := readyModel()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "hi"}))
	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if command != nil {
		t.Fatal("Esc during compose produced an unexpected command")
	}
	if model.composing || model.compose != "" {
		t.Fatalf("composing=%t compose=%q after Esc, want cleared", model.composing, model.compose)
	}
	if model.status != "" {
		t.Fatalf("status = %q after Esc cancel, want unchanged", model.status)
	}
}

func TestModelComposeEnterOnEmptyCancels(t *testing.T) {
	model := readyModel()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command != nil {
		t.Fatal("Enter on empty compose produced an unexpected command")
	}
	if model.composing || model.compose != "" {
		t.Fatalf("composing=%t compose=%q after empty Enter, want cleared", model.composing, model.compose)
	}
}

func TestModelComposeBackspaceRemovesLastRune(t *testing.T) {
	model := readyModel()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "hé"}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	if model.compose != "h" {
		t.Fatalf("compose after backspace = %q, want %q", model.compose, "h")
	}
}

func TestModelComposeCtrlUClearsAll(t *testing.T) {
	model := readyModel()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "hello"}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	if model.compose != "" || !model.composing {
		t.Fatalf("compose=%q composing=%t after Ctrl+U, want cleared but still composing", model.compose, model.composing)
	}
}

func TestModelComposeLiteralCharsIncludingSpecials(t *testing.T) {
	model := readyModel()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	literal := "/?p1!@#xu"
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: literal}))
	if model.compose != literal {
		t.Fatalf("compose = %q, want literal %q", model.compose, literal)
	}
	if !model.composing {
		t.Fatal("composing turned off by special characters")
	}
}

func TestModelComposeEnterWithTextSendsAsync(t *testing.T) {
	var sentTo session.Session
	var sentText string
	model := readyModel()
	model.deps.Send = func(_ context.Context, item session.Session, text string) error {
		sentTo = item
		sentText = text
		return nil
	}
	row, _ := model.selectedRow()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "hello"}))
	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil {
		t.Fatal("Enter with compose text did not produce a command")
	}
	if model.composing || model.compose != "" {
		t.Fatalf("composing=%t compose=%q after send, want cleared immediately", model.composing, model.compose)
	}
	message := command()
	done, ok := message.(sendDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("send command result = %#v, want sendDoneMsg with nil err", message)
	}
	if keyOf(sentTo) != keyOf(row.session) || sentText != "hello" {
		t.Fatalf("Send invoked with session=%#v text=%q, want session=%#v text=%q", sentTo, sentText, row.session, "hello")
	}
	if done.title != sessionTitle(row.session) {
		t.Fatalf("sendDoneMsg.title = %q, want %q", done.title, sessionTitle(row.session))
	}

	model, command = updateModel(model, done)
	if command != nil {
		t.Fatal("sendDoneMsg unexpectedly produced a follow-up command")
	}
	want := "sent to " + sessionTitle(row.session)
	if model.status != want {
		t.Fatalf("status = %q, want %q", model.status, want)
	}
}

func TestModelComposeEnterSendFailureStatus(t *testing.T) {
	model := readyModel()
	model.deps.Send = func(context.Context, session.Session, string) error {
		return errors.New("boom")
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "hello"}))
	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	message := command()
	done := message.(sendDoneMsg)
	model, command = updateModel(model, done)
	if command != nil {
		t.Fatal("failed send should not restart collection")
	}
	want := "send failed: boom"
	if model.status != want {
		t.Fatalf("status = %q, want %q", model.status, want)
	}
}

func TestModelSendStatusSurvivesSelectionChangeAfterSend(t *testing.T) {
	model := readyModel()
	model.result.Sessions = manySessions(2)
	model.refreshVisible()
	firstRow, _ := model.selectedRow()
	model.deps.Send = func(context.Context, session.Session, string) error { return nil }
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "hello"}))
	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	message := command()
	done := message.(sendDoneMsg)

	// Selection changes while the send is in flight.
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	newRow, _ := model.selectedRow()
	if keyOf(newRow.session) == keyOf(firstRow.session) {
		t.Fatal("test setup did not move selection to a different session")
	}

	model, _ = updateModel(model, done)
	want := "sent to " + sessionTitle(firstRow.session)
	if model.status != want {
		t.Fatalf("status = %q, want %q (must keep the sent-to session's title)", model.status, want)
	}
}

func TestModelComposeKeysLiteralDoNotTriggerOtherBindings(t *testing.T) {
	model := readyModel()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	model, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	if command != nil {
		t.Fatal("x while composing should not arm a kill")
	}
	if model.killPending {
		t.Fatal("x while composing armed a pending kill")
	}
	if model.compose != "x" {
		t.Fatalf("compose = %q, want literal %q", model.compose, "x")
	}
}

func TestComposeRendersSendToPrefixAndContent(t *testing.T) {
	model := readyModel()
	row, _ := model.selectedRow()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "hello"}))
	content := ansi.Strip(model.View().Content)
	want := "send to " + sessionTitle(row.session) + ": hello"
	if !strings.Contains(content, want) {
		t.Fatalf("view missing compose line %q:\n%s", want, content)
	}
}

func TestHelpOverlayAndFooterAdvertiseSend(t *testing.T) {
	model := readyModel()
	model.width = 150
	content := ansi.Strip(model.help(model.contentWidth()))
	if !strings.Contains(content, "m msg") {
		t.Fatalf("footer help missing send hint: %q", content)
	}

	model.showHelp = true
	overlay := ansi.Strip(model.View().Content)
	if !strings.Contains(overlay, "send a line without attaching") {
		t.Fatalf("help overlay missing send binding:\n%s", overlay)
	}
}

func TestFooterAt120StillShowsHelpWithSendAdded(t *testing.T) {
	model := readyModel()
	model.width = 120
	content := ansi.Strip(model.help(model.contentWidth()))
	if !strings.Contains(content, "? help") {
		t.Fatalf("footer at 120 cols dropped help: %q", content)
	}
}
