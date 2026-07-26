package tui

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// pressKey applies a key press and, unlike a bare updateModel call, also
// drains any capture (preview or fullscreen) the key triggered — such as the
// history capture that opening fullscreen schedules — while skipping
// tea.Tick children, which would block for the tick interval. This mirrors
// what the real bubbletea runtime does when it executes the returned command.
func pressKey(value model, code rune, text string) model {
	updated, command := updateModel(value, tea.KeyPressMsg(tea.Key{Code: code, Text: text}))
	return applyCaptureCmd(updated, command)
}

// applyCaptureCmd runs command (which may be nested tea.Batch commands, as
// updateModel's KeyPressMsg case produces when it batches syncPreview with
// syncFullPreview) with each leaf command in its own goroutine — a bare
// tick command, returned alongside a capture while one is already in
// flight, would otherwise block the caller for the tick interval — and
// folds any resulting previewMsg/fullPreviewMsg back into the model, so
// tests observe the same state a live run would reach after its capture
// lands.
func applyCaptureCmd(value model, command tea.Cmd) model {
	for _, message := range runCmdLeaves(command) {
		switch message.(type) {
		case previewMsg, fullPreviewMsg:
			value = foldCaptureMsg(value, message)
		}
	}
	return value
}

// runCmdLeaves runs command and, if it yields a tea.BatchMsg, recurses into
// each child so nested batches (batches whose commands are themselves
// batches) are fully unpacked. Each leaf command runs in its own goroutine
// with a timeout, so a tea.Tick child cannot block the caller.
func runCmdLeaves(command tea.Cmd) []tea.Msg {
	if command == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- command() }()
	var message tea.Msg
	select {
	case message = <-done:
	case <-time.After(200 * time.Millisecond):
		return nil
	}
	batch, ok := message.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{message}
	}
	var messages []tea.Msg
	for _, child := range batch {
		messages = append(messages, runCmdLeaves(child)...)
	}
	return messages
}

func foldCaptureMsg(value model, message tea.Msg) model {
	switch message := message.(type) {
	case previewMsg:
		updated, _ := value.updatePreview(message)
		return updated
	case fullPreviewMsg:
		updated, _ := value.updateFullPreview(message)
		return updated
	default:
		return value
	}
}

// loadedFullscreenModel returns a model with the preview visible, a live
// capture already applied, and the fullscreen view open.
func loadedFullscreenModel(t *testing.T, content string) model {
	t.Helper()
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte(content), nil
	})
	command := value.syncPreview()
	if command == nil {
		t.Fatal("syncPreview did not schedule a capture for the live session")
	}
	value, _ = updateModel(value, drainPreviewMsg(command))
	value = pressKey(value, 'f', "f")
	if !value.previewFullscreen {
		t.Fatal("f did not open the fullscreen preview")
	}
	return value
}

func TestFullscreenTogglesWithFAndEscape(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	if !value.previewVisible() {
		t.Fatal("preview should start visible")
	}
	value = pressKey(value, 'f', "f")
	if !value.previewFullscreen {
		t.Fatal("f did not open the fullscreen preview")
	}
	value = pressKey(value, 'f', "f")
	if value.previewFullscreen {
		t.Fatal("f did not close the fullscreen preview")
	}
	value = pressKey(value, 'f', "f")
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if value.previewFullscreen {
		t.Fatal("esc did not close the fullscreen preview")
	}
}

func TestFullscreenPreservesSelectionAndQuery(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: '/'}))
	for _, character := range "connection" {
		value = pressKey(value, character, string(character))
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	selected, query := value.selected, value.query
	if query != "connection" {
		t.Fatalf("search query = %q, want %q", query, "connection")
	}

	value = pressKey(value, 'f', "f")
	value = pressKey(value, 'f', "f")
	if value.previewFullscreen {
		t.Fatal("fullscreen still open after the second f")
	}
	if value.selected != selected || value.query != query {
		t.Fatalf("state changed across fullscreen: selected %d→%d, query %q→%q",
			selected, value.selected, query, value.query)
	}
	// The split view is back: the list rows render alongside the preview.
	if content := ansi.Strip(value.View().Content); !strings.Contains(content, "/work/ars") {
		t.Fatalf("split view did not return after closing fullscreen:\n%s", content)
	}
}

func TestFullscreenSwallowsNavigationKeys(t *testing.T) {
	value := loadedFullscreenModel(t, "live output\n")
	selected := value.selected
	for _, key := range []tea.Key{
		{Code: 'j', Text: "j"},
		{Code: 'k', Text: "k"},
		{Code: tea.KeyDown},
		{Code: tea.KeyUp},
		{Code: 'G', Text: "G"},
	} {
		value, _ = updateModel(value, tea.KeyPressMsg(key))
	}
	if value.selected != selected {
		t.Fatalf("navigation moved the selection while fullscreen: %d→%d", selected, value.selected)
	}
	if !value.previewFullscreen {
		t.Fatal("navigation keys closed the fullscreen preview")
	}
}

// TestFullscreenOpensHelpOverlay guards the dead end the fullscreen key
// swallowing first created: the overlay features the f binding, so it has to be
// reachable from the view that binding describes.
func TestFullscreenOpensHelpOverlay(t *testing.T) {
	value := loadedFullscreenModel(t, "live output\n")
	value = pressKey(value, '?', "?")
	if !value.showHelp {
		t.Fatal("? while fullscreen did not open the help overlay")
	}
	if value.previewFullscreen {
		t.Fatal("? must leave fullscreen so closing help returns to the split view")
	}
	if content := ansi.Strip(value.View().Content); !strings.Contains(content, "ars keys") {
		t.Fatalf("help overlay did not render from fullscreen:\n%s", content)
	}

	// Closing the overlay lands in the split view, not back in fullscreen.
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if value.showHelp || value.previewFullscreen {
		t.Fatalf("closing help left showHelp=%t previewFullscreen=%t, want both false",
			value.showHelp, value.previewFullscreen)
	}
	if value.helpFromFullscreen {
		t.Fatal("closing help did not clear helpFromFullscreen, would mislabel a later plain-list help open")
	}
	if content := ansi.Strip(value.View().Content); !strings.Contains(content, "q quit") {
		t.Fatalf("split view footer missing after closing help:\n%s", content)
	}
}

// TestFullscreenSwallowsActionKeys covers the destructive and mode-switching
// keys: acting on a selection the user cannot see would be a surprise, so
// fullscreen must ignore them rather than pass them to the hidden list.
func TestFullscreenSwallowsActionKeys(t *testing.T) {
	for _, testCase := range []struct {
		name string
		key  tea.Key
	}{
		{"enter", tea.Key{Code: tea.KeyEnter}},
		{"x kill", tea.Key{Code: 'x', Text: "x"}},
		{"m compose", tea.Key{Code: 'm', Text: "m"}},
		{"r refresh", tea.Key{Code: 'r', Text: "r"}},
		{"ctrl+q detach", tea.Key{Code: 'q', Mod: tea.ModCtrl}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			value := loadedFullscreenModel(t, "live output\n")
			updated, _ := updateModel(value, tea.KeyPressMsg(testCase.key))
			if !updated.previewFullscreen {
				t.Fatalf("%s closed the fullscreen preview", testCase.name)
			}
			if updated.killPending {
				t.Fatalf("%s armed a kill from the fullscreen view", testCase.name)
			}
			if updated.composing {
				t.Fatalf("%s started compose mode from the fullscreen view", testCase.name)
			}
			if updated.selected != value.selected {
				t.Fatalf("%s moved the selection: %d→%d", testCase.name, value.selected, updated.selected)
			}
		})
	}
}

func TestFullscreenNoOpWhenPreviewNotVisible(t *testing.T) {
	off := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	off.previewOn = false
	if pressKey(off, 'f', "f").previewFullscreen {
		t.Fatal("f opened fullscreen with the preview toggled off")
	}

	narrow := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	narrow.width = previewMinWidth - 1
	if pressKey(narrow, 'f', "f").previewFullscreen {
		t.Fatal("f opened fullscreen below the preview minimum width")
	}

	nilDep := previewModel(nil)
	if pressKey(nilDep, 'f', "f").previewFullscreen {
		t.Fatal("f opened fullscreen without a Preview dependency")
	}
}

func TestFullscreenNoOpOnGroupHeader(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	value = pressKey(value, 'k', "k") // move up from the session row onto its group header
	row, ok := value.selectedRow()
	if !ok || row.kind != rowHeader {
		t.Fatalf("expected a group header selected: %+v ok=%t", row, ok)
	}
	value = pressKey(value, 'f', "f")
	if value.previewFullscreen {
		t.Fatal("f opened fullscreen on a group header")
	}
}

func TestFullscreenNoOpOnMoreRow(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	sessions := manySessions(3)
	sessions[1].Runtime.State = session.RuntimeSaved
	sessions[2].Runtime.State = session.RuntimeSaved
	value.result.Sessions = sessions
	value.refreshVisible()
	index := -1
	for position, row := range value.rows {
		if row.kind == rowMore {
			index = position
			break
		}
	}
	if index < 0 {
		t.Fatalf("test setup produced no more row: %+v", value.rows)
	}
	value.selectRow(index)
	value = pressKey(value, 'f', "f")
	if value.previewFullscreen {
		t.Fatal("f opened fullscreen on a more row")
	}
}

func TestFullscreenKeyLiteralInSearchAndCompose(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	searching, _ := updateModel(value, tea.KeyPressMsg(tea.Key{Code: '/'}))
	searching = pressKey(searching, 'f', "f")
	if searching.query != "f" {
		t.Fatalf("f in search mode = query %q, want literal", searching.query)
	}
	if searching.previewFullscreen {
		t.Fatal("f in search mode must not open fullscreen")
	}

	composing := pressKey(value, 'm', "m")
	if !composing.composing {
		t.Fatal("m did not start compose mode")
	}
	composing = pressKey(composing, 'f', "f")
	if composing.compose != "f" {
		t.Fatalf("f in compose mode = compose %q, want literal", composing.compose)
	}
	if composing.previewFullscreen {
		t.Fatal("f in compose mode must not open fullscreen")
	}
}

func TestFullscreenRendersTitleContentAndCloseHint(t *testing.T) {
	value := loadedFullscreenModel(t, "first line\nsecond line\n")
	content := ansi.Strip(value.View().Content)
	for _, want := range []string{"connection check", "first line", "second line", "f / esc to close"} {
		if !strings.Contains(content, want) {
			t.Fatalf("fullscreen preview missing %q:\n%s", want, content)
		}
	}
	// The list is replaced, not merely widened: its detail and footer rows are gone.
	if strings.Contains(content, "q quit") {
		t.Fatalf("fullscreen preview still renders the split-view footer:\n%s", content)
	}
}

// TestFullscreenUsesFullContentWidth is the point of the feature: a line too
// wide for the split preview column must survive intact fullscreen.
func TestFullscreenUsesFullContentWidth(t *testing.T) {
	_, width := contentFrame(120)
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return nil, nil
	})
	_, previewCols := value.splitWidths(width)
	line := strings.Repeat("x", previewCols+20)
	if len(line) > width {
		t.Fatalf("test line of %d columns does not fit the %d-column frame", len(line), width)
	}

	value = previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte(line), nil
	})
	value, _ = updateModel(value, drainPreviewMsg(value.syncPreview()))

	// Split view: the column is too narrow, so the line is truncated.
	if split := ansi.Strip(value.View().Content); strings.Contains(split, line) {
		t.Fatalf("split preview unexpectedly fit the %d-column line in a %d-column pane", len(line), previewCols)
	}
	// Fullscreen: the same line renders in full.
	value = pressKey(value, 'f', "f")
	if full := ansi.Strip(value.View().Content); !strings.Contains(full, line) {
		t.Fatalf("fullscreen truncated a %d-column line that fits the %d-column frame:\n%s",
			len(line), width, full)
	}
}

func TestFullscreenSavedSessionShowsPlaceholder(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	// Move to the saved-only "api" group, open it and select its session.
	value = pressKey(value, 'j', "j")
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	value = pressKey(value, 'j', "j")
	selected, ok := value.selectedSession()
	if !ok || selected.Runtime.State != session.RuntimeSaved {
		t.Fatalf("did not select a saved session: %+v ok=%t", selected, ok)
	}
	value = pressKey(value, 'f', "f")
	if !value.previewFullscreen {
		t.Fatal("f did not open fullscreen on a saved session")
	}
	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "no live pane") {
		t.Fatalf("fullscreen saved session missing the placeholder:\n%s", content)
	}
}

func TestFullscreenTruncatesLongLines(t *testing.T) {
	value := loadedFullscreenModel(t, strings.Repeat("w", 400))
	if line := widestLine(ansi.Strip(value.View().Content)); line > value.contentWidth() {
		t.Fatalf("fullscreen line is %d columns, wider than the %d-column terminal",
			line, value.contentWidth())
	}
}

func TestFullscreenShowsTailWhenContentOverflows(t *testing.T) {
	lines := make([]string, 200)
	for index := range lines {
		lines[index] = "pane-line-" + strconv.Itoa(index)
	}
	value := loadedFullscreenModel(t, strings.Join(lines, "\n"))
	content := ansi.Strip(value.View().Content)
	if rendered := strings.Count(content, "\n") + 1; rendered > value.height {
		t.Fatalf("fullscreen rendered %d lines, over the %d-line terminal", rendered, value.height)
	}
	last := lines[len(lines)-1]
	if !strings.Contains(content, last) {
		t.Fatalf("fullscreen dropped the most recent line %q:\n%s", last, content)
	}
	// Matched with the trailing newline so "pane-line-0" cannot match inside
	// "pane-line-0..." from a later line.
	if strings.Contains(content, lines[0]+"\n") {
		t.Fatalf("fullscreen kept the oldest line %q instead of the tail:\n%s", lines[0], content)
	}
}

// TestFullscreenStaysLiveOnPreviewTick covers a PreviewHistory-less setup,
// where fullscreen falls back to the tail-only Preview capture: its own
// fullscreen tick (fullPreviewTickMsg), not the side panel's previewTickMsg,
// is what keeps it live, since the two panels now capture independently.
func TestFullscreenStaysLiveOnPreviewTick(t *testing.T) {
	capture := "before tick"
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte(capture), nil
	})
	command := value.syncPreview()
	value, _ = updateModel(value, drainPreviewMsg(command))
	value = pressKey(value, 'f', "f")
	if !strings.Contains(ansi.Strip(value.View().Content), "before tick") {
		t.Fatal("fullscreen did not render the initial capture")
	}

	selected, _ := value.selectedSession()
	capture = "after tick"
	value, command = value.updateFullPreviewTick(fullPreviewTickMsg{key: keyOf(selected)})
	if command == nil {
		t.Fatal("the fullscreen tick stopped rescheduling while fullscreen")
	}
	value, _ = updateModel(value, drainFullPreviewMsg(command))
	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "after tick") {
		t.Fatalf("fullscreen did not refresh from the tick capture:\n%s", content)
	}
}

func TestFullscreenExitsWhenResizedBelowMinWidth(t *testing.T) {
	value := loadedFullscreenModel(t, "live output\n")
	value, _ = updateModel(value, tea.WindowSizeMsg{Width: previewMinWidth - 1, Height: 30})
	if value.previewFullscreen {
		t.Fatal("fullscreen survived a resize below the preview minimum width")
	}
	if content := ansi.Strip(value.View().Content); !strings.Contains(content, "q quit") {
		t.Fatalf("split view footer missing after the forced exit:\n%s", content)
	}
}

// TestFullscreenExitsWhenResizeLeavesHelpFromFullscreenStale covers M-3: a
// resize that forces fullscreen closed must also clear helpFromFullscreen,
// or a help overlay opened afterward from the plain list would still be
// mislabeled "fullscreen preview:".
func TestFullscreenExitsWhenResizeLeavesHelpFromFullscreenStale(t *testing.T) {
	value := loadedFullscreenModel(t, "live output\n")
	value, _ = updateModel(value, tea.WindowSizeMsg{Width: previewMinWidth - 1, Height: 30})
	if value.previewFullscreen {
		t.Fatal("fullscreen survived a resize below the preview minimum width")
	}
	if value.helpFromFullscreen {
		t.Fatal("helpFromFullscreen survived a resize-forced fullscreen exit")
	}
}

// TestFullscreenExitsWhenSelectionLeavesSessionRowOnCollect covers I-1: a
// collectUpdateMsg that leaves the selection off a session row (the session
// died, or its host dropped out) must close fullscreen exactly like the
// WindowSizeMsg guard does, rather than rendering the empty frame PR #45
// eliminated for other entry points.
func TestFullscreenExitsWhenSelectionLeavesSessionRowOnCollect(t *testing.T) {
	value := loadedFullscreenModel(t, "live output\n")
	generation := value.generation

	// The collection comes back with the session gone entirely: the group
	// header is what restoreSelection lands on.
	empty := Result{}
	value, _ = updateModel(value, collectUpdateMsg{generation: generation, update: Update{Result: empty, Done: true}})

	if value.previewFullscreen {
		t.Fatal("fullscreen survived a collect that left the selection off a session row")
	}
	if content := ansi.Strip(value.View().Content); !strings.Contains(content, "q quit") {
		t.Fatalf("split view footer missing after the forced exit:\n%s", content)
	}
}

// TestFullscreenStaysOpenWhenCollectKeepsSessionSelected is the control for
// I-1: a collect that still resolves the same session must not be treated as
// a "selection left the session row" exit.
func TestFullscreenStaysOpenWhenCollectKeepsSessionSelected(t *testing.T) {
	value := loadedFullscreenModel(t, "live output\n")
	generation := value.generation

	same := Result{Sessions: twoSessions()}
	value, _ = updateModel(value, collectUpdateMsg{generation: generation, update: Update{Result: same, Done: true}})

	if !value.previewFullscreen {
		t.Fatal("fullscreen closed on a collect that kept the session selected")
	}
}

// TestFullscreenRecapturesWhenSelectionChangesWithContentAlreadyLoaded covers
// I-2: syncFullPreview must recapture when the selected session changes even
// though previewFullContent is already non-nil from the previous session —
// otherwise the old session's scrollback freezes on screen, and the tick
// (key-checked against the new session) never corrects it.
func TestFullscreenRecapturesWhenSelectionChangesWithContentAlreadyLoaded(t *testing.T) {
	sessions := manySessions(2)
	calls := map[string]int{}
	value := previewModel(func(_ context.Context, item session.Session) ([]byte, error) {
		calls[item.NativeID]++
		return []byte("content for " + item.NativeID), nil
	})
	value.result.Sessions = sessions
	value.refreshVisible()
	value.selectRow(firstSessionRow(value.rows))
	first, ok := value.selectedSession()
	if !ok {
		t.Fatal("expected a session row selected")
	}
	value = pressKey(value, 'f', "f")
	if !value.previewFullscreen {
		t.Fatal("f did not open fullscreen")
	}
	if !strings.Contains(ansi.Strip(value.View().Content), "content for "+first.NativeID) {
		t.Fatalf("fullscreen did not capture the first session:\n%s", ansi.Strip(value.View().Content))
	}

	// Move to the other session while fullscreen stays open (session rows are
	// adjacent, so this simulates the selection changing under the hood).
	value.previewFullscreen = true
	for index, row := range value.rows {
		if row.kind == rowSession && row.session.NativeID != first.NativeID {
			value.selectRow(index)
			break
		}
	}
	second, ok := value.selectedSession()
	if !ok || second.NativeID == first.NativeID {
		t.Fatalf("test setup did not select a different session: %+v", second)
	}

	command := value.syncFullPreview()
	if command == nil {
		t.Fatal("syncFullPreview did not recapture after the selection changed")
	}
	value, _ = updateModel(value, drainFullPreviewMsg(command))
	content := ansi.Strip(value.View().Content)
	if strings.Contains(content, "content for "+first.NativeID) {
		t.Fatalf("fullscreen still shows the previous session's frozen scrollback:\n%s", content)
	}
	if !strings.Contains(content, "content for "+second.NativeID) {
		t.Fatalf("fullscreen did not recapture the newly selected session:\n%s", content)
	}
}

func TestFullscreenPreviewOffKeyExitsAndClosesPane(t *testing.T) {
	value := loadedFullscreenModel(t, "live output\n")
	value = pressKey(value, 'p', "p")
	if value.previewFullscreen {
		t.Fatal("p while fullscreen did not exit the fullscreen view")
	}
	if value.previewOn {
		t.Fatal("p while fullscreen did not turn the preview off")
	}
}

func TestFullscreenFooterHintFollowsPreviewVisibility(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	// Wide enough that joinFooterItems drops nothing, so the assertions measure
	// the hint's visibility rule rather than the narrow-terminal drop order.
	value.width = 200
	if footer := ansi.Strip(value.help(value.contentWidth())); !strings.Contains(footer, "f full") {
		t.Fatalf("footer missing the fullscreen hint with the preview visible: %q", footer)
	}
	value.previewOn = false
	if footer := ansi.Strip(value.help(value.contentWidth())); strings.Contains(footer, "f full") {
		t.Fatalf("footer shows the fullscreen hint with the preview off: %q", footer)
	}
	value.previewOn = true
	value.width = previewMinWidth - 1
	if footer := ansi.Strip(value.help(value.contentWidth())); strings.Contains(footer, "f full") {
		t.Fatalf("footer shows the fullscreen hint below the minimum width: %q", footer)
	}
}

// TestFullscreenFeaturedHelpExcludesRowKindBindings covers the follow-up: while
// fullscreen is open, the list underneath it is hidden, so featuredHelp must
// not feature row-kind bindings fullscreen swallows (P, x, m, enter) — only
// the scroll binding fullscreen actually handles. helpFullscreen already wins
// helpContextLabel; this brings featuredHelp's band selection into agreement.
func TestFullscreenFeaturedHelpExcludesRowKindBindings(t *testing.T) {
	value := loadedFullscreenModel(t, "live output\n")
	value.width, value.height, value.noColor = 120, 40, true
	lines := overlayLines(t, value)

	label := lineContaining(t, lines, "fullscreen preview")
	all := lineContaining(t, lines, "all keys")
	for _, want := range []string{"pin / unpin", "kill session / group", "send a line without attaching", "attach session"} {
		if index := lineContaining(t, lines, want); index >= label && index <= all {
			t.Fatalf("row-kind binding %q at line %d is wrongly featured under fullscreen (%d..%d):\n%s",
				want, index, label, all, strings.Join(lines, "\n"))
		}
	}
	if index := lineContaining(t, lines, "scroll scrollback"); index < label || index > all {
		t.Fatalf("scroll binding at line %d is outside the fullscreen context section (%d..%d):\n%s",
			index, label, all, strings.Join(lines, "\n"))
	}
}

func TestFullscreenHelpOverlayFeaturesBindingInPreviewContext(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	value.height, value.noColor = 40, true
	if !value.previewVisible() {
		t.Fatal("expected the preview pane visible")
	}
	lines := overlayLines(t, value)
	label := lineContaining(t, lines, "on a session")
	all := lineContaining(t, lines, "all keys")
	index := lineContaining(t, lines, "fullscreen preview")
	if index < label || index > all {
		t.Fatalf("f binding at line %d is outside the context section (%d..%d):\n%s",
			index, label, all, strings.Join(lines, "\n"))
	}
}

// widestLine reports the display width of the widest line in text.
func widestLine(text string) int {
	widest := 0
	for _, line := range strings.Split(text, "\n") {
		widest = max(widest, lipgloss.Width(strings.TrimRight(line, " ")))
	}
	return widest
}
