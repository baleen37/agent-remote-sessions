package tui

import (
	"context"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// historyPreviewModel returns a model wired with both Preview (tail, for the
// side panel) and PreviewHistory (scrollback, for fullscreen).
func historyPreviewModel(history func(context.Context, session.Session) ([]byte, error)) model {
	result := Result{Sessions: twoSessions()}
	deps := Dependencies{
		Collect:        staticCollect(result),
		Attach:         func(context.Context, session.Session) (ExecCommand, error) { return &fakeExecCommand{}, nil },
		Preview:        func(context.Context, session.Session) ([]byte, error) { return []byte("tail output"), nil },
		PreviewHistory: history,
		LocalTarget:    "localhost",
		NoColor:        true,
	}
	value := newModel(context.Background(), deps)
	message, _, _ := initialCommands(value.Init())
	value, _ = updateModel(value, message)
	value.width = 120
	value.height = 30
	return value
}

func numberedLines(n int) []string {
	lines := make([]string, n)
	for index := range lines {
		lines[index] = "line-" + strconv.Itoa(index)
	}
	return lines
}

// openFullscreenWithHistory opens the fullscreen preview and drains the
// history capture it triggers (pressKey applies the key's resulting
// command), returning a model with previewFullContent populated with the
// given lines.
func openFullscreenWithHistory(t *testing.T, lines []string) model {
	t.Helper()
	content := strings.Join(lines, "\n")
	value := historyPreviewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte(content), nil
	})
	value = pressKey(value, 'f', "f")
	if !value.previewFullscreen {
		t.Fatal("f did not open the fullscreen preview")
	}
	if value.previewFullContent == nil {
		t.Fatalf("opening fullscreen did not populate previewFullContent (pending=%t err=%q)",
			value.previewFullPending, value.previewFullErr)
	}
	return value
}

func drainFullPreviewMsg(command tea.Cmd) fullPreviewMsg {
	message := command()
	if batch, ok := message.(tea.BatchMsg); ok {
		for _, child := range batch {
			if preview, ok := child().(fullPreviewMsg); ok {
				return preview
			}
		}
		panic("no fullPreviewMsg in batch")
	}
	return message.(fullPreviewMsg)
}

func TestFullscreenOpenTriggersHistoryCapture(t *testing.T) {
	var gotHistory bool
	value := historyPreviewModel(func(context.Context, session.Session) ([]byte, error) {
		gotHistory = true
		return []byte(strings.Join(numberedLines(10), "\n")), nil
	})
	value = pressKey(value, 'f', "f")
	if !value.previewFullscreen {
		t.Fatal("f did not open the fullscreen preview")
	}
	if !gotHistory {
		t.Fatal("fullscreen open did not call PreviewHistory")
	}
	if len(value.previewFullContent) != 10 {
		t.Fatalf("previewFullContent has %d lines, want 10", len(value.previewFullContent))
	}
}

func TestFullscreenDefaultsToBottom(t *testing.T) {
	value := openFullscreenWithHistory(t, numberedLines(600))
	if value.previewScrollOffset != 0 {
		t.Fatalf("previewScrollOffset = %d, want 0 (tail) on open", value.previewScrollOffset)
	}
	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "line-599") {
		t.Fatalf("fullscreen did not default to the tail:\n%s", content)
	}
}

func TestFullscreenScrollUpDownByLine(t *testing.T) {
	value := openFullscreenWithHistory(t, numberedLines(600))
	value = pressKey(value, 'k', "k")
	if value.previewScrollOffset != 1 {
		t.Fatalf("k scrolled offset to %d, want 1", value.previewScrollOffset)
	}
	value = pressKey(value, 'k', "k")
	if value.previewScrollOffset != 2 {
		t.Fatalf("second k scrolled offset to %d, want 2", value.previewScrollOffset)
	}
	value = pressKey(value, 'j', "j")
	if value.previewScrollOffset != 1 {
		t.Fatalf("j scrolled offset to %d, want 1", value.previewScrollOffset)
	}
}

func TestFullscreenScrollClampsAtBottom(t *testing.T) {
	value := openFullscreenWithHistory(t, numberedLines(600))
	value = pressKey(value, 'j', "j")
	if value.previewScrollOffset != 0 {
		t.Fatalf("j at the bottom moved offset to %d, want clamped at 0", value.previewScrollOffset)
	}
}

func TestFullscreenScrollClampsAtTop(t *testing.T) {
	value := openFullscreenWithHistory(t, numberedLines(10))
	for range 20 {
		value = pressKey(value, 'k', "k")
	}
	maxOffset := len(value.previewFullContent) - 1
	if value.previewScrollOffset != maxOffset {
		t.Fatalf("previewScrollOffset = %d, want clamped at %d (top)", value.previewScrollOffset, maxOffset)
	}
	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "line-0") {
		t.Fatalf("scrolled-to-top view missing the oldest line:\n%s", content)
	}
}

func TestFullscreenPageScrollWithPgUpPgDn(t *testing.T) {
	value := openFullscreenWithHistory(t, numberedLines(600))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}))
	afterPgUp := value.previewScrollOffset
	if afterPgUp <= 1 {
		t.Fatalf("PgUp scrolled by only %d, want a page", afterPgUp)
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown}))
	if value.previewScrollOffset >= afterPgUp {
		t.Fatalf("PgDn did not scroll back down: before %d, after %d", afterPgUp, value.previewScrollOffset)
	}
}

func TestFullscreenPageScrollWithCtrlUCtrlD(t *testing.T) {
	value := openFullscreenWithHistory(t, numberedLines(600))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	afterCtrlU := value.previewScrollOffset
	if afterCtrlU <= 1 {
		t.Fatalf("Ctrl+U scrolled by only %d, want a page", afterCtrlU)
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'd', Mod: tea.ModCtrl}))
	if value.previewScrollOffset >= afterCtrlU {
		t.Fatalf("Ctrl+D did not scroll back down: before %d, after %d", afterCtrlU, value.previewScrollOffset)
	}
}

func TestFullscreenTailFollowsWhenAtBottom(t *testing.T) {
	lines := numberedLines(50)
	content := strings.Join(lines, "\n")
	value := historyPreviewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte(content), nil
	})
	value = pressKey(value, 'f', "f")
	if value.previewFullContent == nil {
		t.Fatalf("opening fullscreen did not populate previewFullContent (pending=%t err=%q)",
			value.previewFullPending, value.previewFullErr)
	}

	selected, _ := value.selectedSession()
	content = strings.Join(numberedLines(60), "\n")
	value, command := value.updateFullPreviewTick(fullPreviewTickMsg{key: keyOf(selected)})
	if command == nil {
		t.Fatal("fullscreen tick stopped rescheduling")
	}
	value, _ = updateModel(value, drainFullPreviewMsg(command))
	if value.previewScrollOffset != 0 {
		t.Fatalf("tail-follow lost bottom position: offset = %d", value.previewScrollOffset)
	}
	rendered := ansi.Strip(value.View().Content)
	if !strings.Contains(rendered, "line-59") {
		t.Fatalf("tail-follow did not show the new tail:\n%s", rendered)
	}
}

func TestFullscreenPinnedOffsetSurvivesRecapture(t *testing.T) {
	lines := numberedLines(50)
	content := strings.Join(lines, "\n")
	value := historyPreviewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte(content), nil
	})
	value = pressKey(value, 'f', "f")
	if value.previewFullContent == nil {
		t.Fatalf("opening fullscreen did not populate previewFullContent (pending=%t err=%q)",
			value.previewFullPending, value.previewFullErr)
	}
	value = pressKey(value, 'k', "k")
	value = pressKey(value, 'k', "k")
	if value.previewScrollOffset != 2 {
		t.Fatalf("previewScrollOffset = %d, want 2 before recapture", value.previewScrollOffset)
	}

	selected, _ := value.selectedSession()
	content = strings.Join(numberedLines(55), "\n")
	value, command := value.updateFullPreviewTick(fullPreviewTickMsg{key: keyOf(selected)})
	value, _ = updateModel(value, drainFullPreviewMsg(command))
	if value.previewScrollOffset != 2 {
		t.Fatalf("recapture while scrolled up changed offset to %d, want pinned at 2", value.previewScrollOffset)
	}
}

func TestFullscreenScrollOffsetResetsOnReopen(t *testing.T) {
	value := openFullscreenWithHistory(t, numberedLines(600))
	value = pressKey(value, 'k', "k")
	value = pressKey(value, 'k', "k")
	value = pressKey(value, 'f', "f") // close
	value = pressKey(value, 'f', "f") // reopen
	if value.previewScrollOffset != 0 {
		t.Fatalf("reopening fullscreen kept offset %d, want reset to 0", value.previewScrollOffset)
	}
}

func TestFullscreenShowsScrollIndicatorWhenScrolledUp(t *testing.T) {
	value := openFullscreenWithHistory(t, numberedLines(600))
	value = pressKey(value, 'k', "k")
	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "more") {
		t.Fatalf("fullscreen scrolled up did not show a position indicator:\n%s", content)
	}
}

func TestFullscreenNoScrollIndicatorAtBottom(t *testing.T) {
	value := openFullscreenWithHistory(t, numberedLines(10))
	content := ansi.Strip(value.View().Content)
	if strings.Contains(content, "more") {
		t.Fatalf("fullscreen at the tail should not show a scroll indicator:\n%s", content)
	}
}

func TestFullscreenHelpOverlayListsScrollKeys(t *testing.T) {
	value := loadedFullscreenModel(t, "live output\n")
	value = pressKey(value, '?', "?")
	lines := overlayLines(t, value)
	label := lineContaining(t, lines, "fullscreen preview:")
	all := lineContaining(t, lines, "all keys")
	content := strings.Join(lines, "\n")
	for _, want := range []string{"j / k", "PgUp / PgDn", "Ctrl+U / Ctrl+D"} {
		if !strings.Contains(content, want) {
			t.Fatalf("fullscreen help overlay missing scroll key %q:\n%s", want, content)
		}
		if index := lineContaining(t, lines, want); index < label || index > all {
			t.Fatalf("scroll key %q at line %d is outside the fullscreen context section (%d..%d):\n%s",
				want, index, label, all, content)
		}
	}
}

// TestFullscreenQuestionMarkStillOpensHelp guards the prior regression where
// fullscreen swallowed ? entirely; it must keep working once j/k become
// scroll keys instead of a no-op pass-through.
func TestFullscreenQuestionMarkStillOpensHelp(t *testing.T) {
	value := loadedFullscreenModel(t, "live output\n")
	value = pressKey(value, '?', "?")
	if !value.showHelp {
		t.Fatal("? while fullscreen did not open the help overlay")
	}
	if value.previewFullscreen {
		t.Fatal("? must leave fullscreen so closing help returns to the split view")
	}
}

func TestFullscreenNoPreviewHistoryFallsBackToTail(t *testing.T) {
	// PreviewHistory unset (nil): fullscreen must still render usefully by
	// falling back to the tail-only Preview capture rather than showing
	// nothing.
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	command := value.syncPreview()
	value, _ = updateModel(value, drainPreviewMsg(command))
	value = pressKey(value, 'f', "f")
	if !value.previewFullscreen {
		t.Fatal("f did not open fullscreen")
	}
	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "live output") {
		t.Fatalf("fullscreen without PreviewHistory did not fall back to the tail capture:\n%s", content)
	}
}
