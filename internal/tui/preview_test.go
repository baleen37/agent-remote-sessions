package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

func previewModel(capture func(context.Context, session.Session) ([]byte, error)) model {
	result := Result{Sessions: twoSessions()}
	deps := Dependencies{
		Collect:     staticCollect(result),
		Attach:      func(context.Context, session.Session) (ExecCommand, error) { return &fakeExecCommand{}, nil },
		Preview:     capture,
		LocalTarget: "localhost",
		Now:         func() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) },
		NoColor:     true,
	}
	value := newModel(context.Background(), deps)
	message, hasCollection, _ := initialCommands(value.Init())
	if !hasCollection {
		panic("previewModel: Init did not produce collectUpdateMsg")
	}
	value, _ = updateModel(value, message)
	value.width = 120
	value.height = 30
	return value
}

func TestPreviewKeepsDetailAndFooterFullWidth(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	value.width = 140
	value.height = 30
	// A CWD that fits on one line at full width but would wrap once the list
	// column is narrowed for the preview.
	item := twoSessions()[0]
	item.CWD = "/" + strings.Repeat("c", 90)
	value.result.Sessions = []session.Session{item}
	value.refreshVisible()
	if !value.previewVisible() {
		t.Fatal("preview should be visible at 140 columns")
	}

	content := ansi.Strip(value.View().Content)

	// The footer help must reach its tail keys instead of being truncated to
	// the narrow list width.
	if !strings.Contains(content, "q quit") || !strings.Contains(content, "? help") {
		t.Fatalf("footer help truncated, missing tail keys:\n%s", content)
	}

	// The full CWD detail line must render on a single line, not wrapped.
	if !strings.Contains(content, item.CWD) {
		t.Fatalf("detail CWD wrapped instead of using full width:\n%s", content)
	}
}

func TestPreviewHiddenBelowMinWidth(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	value.width = previewMinWidth - 1
	if value.previewVisible() {
		t.Fatal("preview should be hidden below minimum width")
	}
	value.width = previewMinWidth
	if !value.previewVisible() {
		t.Fatal("preview should be visible at minimum width")
	}
}

func TestPreviewToggleOff(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	if !value.previewVisible() {
		t.Fatal("preview should start visible")
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'p', Text: "p"}))
	if value.previewVisible() {
		t.Fatal("p did not toggle preview off")
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'p', Text: "p"}))
	if !value.previewVisible() {
		t.Fatal("p did not toggle preview back on")
	}
}

func TestPreviewCaptureRendersLivePane(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("first line\nsecond line\n"), nil
	})
	// The first session is attached (live); syncPreview on selection issues a
	// capture. Deliver its result.
	command := value.syncPreview()
	if command == nil {
		t.Fatal("syncPreview did not schedule a capture for the live session")
	}
	message := drainPreviewMsg(command)
	value, _ = updateModel(value, message)
	content := ansi.Strip(value.View().Content)
	for _, want := range []string{"connection check", "first line", "second line"} {
		if !strings.Contains(content, want) {
			t.Fatalf("preview missing %q:\n%s", want, content)
		}
	}
}

func TestPreviewIgnoresStaleResult(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("output"), nil
	})
	value.syncPreview()
	stale := previewMsg{key: sessionKey{nativeID: "other"}, content: []byte("stale output")}
	value, _ = updateModel(value, stale)
	if len(value.previewContent) != 0 {
		t.Fatalf("stale preview result was applied: %#v", value.previewContent)
	}
}

func TestPreviewErrorShowsNotice(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return nil, errors.New("boom")
	})
	command := value.syncPreview()
	message := drainPreviewMsg(command)
	value, _ = updateModel(value, message)
	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "preview unavailable") {
		t.Fatalf("preview error notice missing:\n%s", content)
	}
}

func TestPreviewSavedSessionShowsNoLivePaneWithoutCapture(t *testing.T) {
	captured := false
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		captured = true
		return []byte("output"), nil
	})
	// Move selection to the saved-only "api" group and open it.
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	selected, ok := value.selectedSession()
	if !ok || selected.Runtime.State != session.RuntimeSaved {
		t.Fatalf("did not select a saved session: %+v ok=%t", selected, ok)
	}
	if captured {
		t.Fatal("saved session must not trigger a capture")
	}
	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "no live pane") {
		t.Fatalf("saved session preview missing notice:\n%s", content)
	}
}

func TestPreviewTickReschedulesForLiveSelection(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("output"), nil
	})
	value.syncPreview()
	selected, _ := value.selectedSession()
	_, command := value.updatePreviewTick(previewTickMsg{key: keyOf(selected)})
	if command == nil {
		t.Fatal("tick for live selection did not reschedule a capture")
	}
	_, command = value.updatePreviewTick(previewTickMsg{key: sessionKey{nativeID: "gone"}})
	if command != nil {
		t.Fatal("tick for stale key should not reschedule")
	}
}

func TestPreviewTickSkipsCaptureWhileInFlight(t *testing.T) {
	var captures int
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		captures++
		return []byte("output"), nil
	})
	// Initial selection fires the first capture; drain it so the counter
	// reflects only the capture, then leave it in-flight (no previewMsg yet).
	command := value.syncPreview()
	drainPreviewMsg(command)
	if captures != 1 {
		t.Fatalf("initial syncPreview fired %d captures, want 1", captures)
	}

	selected, _ := value.selectedSession()
	key := keyOf(selected)

	// A slow capture means the previewMsg has not arrived yet, so the model is
	// still in-flight. Ticks must not pile on additional captures.
	_, command = value.updatePreviewTick(previewTickMsg{key: key})
	runCaptureChildren(command)
	_, command = value.updatePreviewTick(previewTickMsg{key: key})
	runCaptureChildren(command)
	if captures != 1 {
		t.Fatalf("in-flight ticks fired extra captures: total %d, want 1", captures)
	}

	// Once the in-flight result lands, the next tick may capture again.
	value, _ = value.updatePreview(previewMsg{key: key, content: []byte("output")})
	_, command = value.updatePreviewTick(previewTickMsg{key: key})
	if command == nil {
		t.Fatal("tick after result did not reschedule a capture")
	}
	runCaptureChildren(command)
	if captures != 2 {
		t.Fatalf("tick after result fired %d captures total, want 2", captures)
	}
}

// runCaptureChildren runs a command's capture children (which return promptly)
// while skipping tea.Tick children, which would block for the interval. It
// exists so tests can observe capture side effects without waiting on ticks.
// The whole traversal runs off the main goroutine so a bare tick command
// (returned while a capture is in flight) never blocks the test.
func runCaptureChildren(command tea.Cmd) {
	if command == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		message := command()
		batch, ok := message.(tea.BatchMsg)
		if !ok {
			return
		}
		for _, child := range batch {
			if _, ok := child().(previewMsg); ok {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
	}
}

func TestPreviewHelpAdvertisesToggle(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("output"), nil
	})
	footer := ansi.Strip(value.help(value.contentWidth()))
	if !strings.Contains(footer, "p preview") {
		t.Fatalf("footer help missing preview toggle: %q", footer)
	}
	value.showHelp = true
	overlay := ansi.Strip(value.View().Content)
	if !strings.Contains(overlay, "toggle preview pane") {
		t.Fatalf("help overlay missing preview binding:\n%s", overlay)
	}
}

func TestPreviewLiteralInSearch(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("output"), nil
	})
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: '/'}))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'p', Text: "p"}))
	if value.query != "p" {
		t.Fatalf("p in search mode = query %q, want literal", value.query)
	}
	if !value.previewOn {
		t.Fatal("p in search mode must not toggle preview")
	}
}

// drainPreviewMsg runs a command (or batch) and returns its previewMsg.
func drainPreviewMsg(command tea.Cmd) previewMsg {
	message := command()
	if batch, ok := message.(tea.BatchMsg); ok {
		for _, child := range batch {
			if preview, ok := child().(previewMsg); ok {
				return preview
			}
		}
		panic("no previewMsg in batch")
	}
	return message.(previewMsg)
}
