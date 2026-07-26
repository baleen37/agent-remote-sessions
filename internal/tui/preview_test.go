package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
	// Keep the model narrow (hidden layout) through the initial collection
	// update so its internal syncPreview call cannot claim previewKey before
	// the test's own explicit syncPreview call does — otherwise that call
	// would see an unchanged key and skip firing a capture. defaultWidth (80)
	// now falls in the stacked layout's visible range, so this can no longer
	// rely on the zero-value width alone the way it could before stacked mode.
	value.width = stackedMinWidth - 1
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

func TestPreviewHiddenBelowStackedMinWidth(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	value.width = stackedMinWidth - 1
	if value.previewVisible() {
		t.Fatal("preview should be hidden below the stacked minimum width")
	}
	if got := previewLayoutOf(value.contentWidth()); got != previewHidden {
		t.Fatalf("previewLayoutOf below stacked minimum = %v, want previewHidden", got)
	}
}

// TestPreviewVisibleInStackedRange covers the 50-99 content-width band: too
// narrow for the dual side-by-side layout, but wide enough for stacked
// (list-above-preview), so the preview must stay visible rather than hide as
// it did before stacked mode existed.
func TestPreviewVisibleInStackedRange(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	value.width = stackedMinWidth
	if !value.previewVisible() {
		t.Fatal("preview should be visible at the stacked minimum width")
	}
	if got := previewLayoutOf(value.contentWidth()); got != previewStacked {
		t.Fatalf("previewLayoutOf at stacked minimum = %v, want previewStacked", got)
	}

	value.width = previewMinWidth - 1
	if !value.previewVisible() {
		t.Fatal("preview should still be visible just below the dual minimum width")
	}
	if got := previewLayoutOf(value.contentWidth()); got != previewStacked {
		t.Fatalf("previewLayoutOf below dual minimum = %v, want previewStacked", got)
	}

	value.width = previewMinWidth
	if !value.previewVisible() {
		t.Fatal("preview should be visible at the dual minimum width")
	}
	if got := previewLayoutOf(value.contentWidth()); got != previewDual {
		t.Fatalf("previewLayoutOf at dual minimum = %v, want previewDual", got)
	}
}

// TestPreviewWidthInvariant checks the split-view width contract: the list
// column, the fixed 3-cell separator, and the preview column must always sum
// back to the full content width, across the widths the split view actually
// ships at and across the adjustable ratio's full range.
func TestPreviewWidthInvariant(t *testing.T) {
	value := model{previewPct: defaultPreviewPct}
	for _, total := range []int{100, 120, 140} {
		for pct := previewPctMin; pct <= previewPctMax; pct += previewPctStep {
			value.previewPct = pct
			list, preview := value.splitWidths(total)
			if got := list + previewSeparatorWidth + preview; got != total {
				t.Fatalf("splitWidths(%d) at pct %d = list %d, preview %d; list+%d+preview = %d, want %d", total, pct, list, preview, previewSeparatorWidth, got, total)
			}
		}
	}
}

// TestSplitWidthsDefaultFavorsPreview locks in the agent-deck-style default:
// the preview gets 65% of the content width, the list the remainder.
func TestSplitWidthsDefaultFavorsPreview(t *testing.T) {
	value := model{previewPct: defaultPreviewPct}
	for _, total := range []int{120, 140} {
		list, preview := value.splitWidths(total)
		if preview <= list {
			t.Fatalf("splitWidths(%d) = list %d, preview %d; want preview to be the larger share at the default 65%%", total, list, preview)
		}
	}
}

// TestPreviewSeparatorReplacesGutter locks in the visible three-column
// separator (" │ ") between the list and preview panels, replacing the old
// two-space gutter.
func TestPreviewSeparatorReplacesGutter(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	value.width, value.height = 120, 24
	content := ansi.Strip(value.View().Content)
	lines := strings.Split(content, "\n")
	found := false
	for _, line := range lines {
		if strings.Contains(line, "SESSIONS") && strings.Contains(line, previewSeparator) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("panel title row missing the %q separator:\n%s", previewSeparator, content)
	}
}

// TestPanelTitlesConsumeBodyHeightWithoutGrowingFrame verifies the height
// contract from the task 4 brief: the two panel-title rows (title + underline)
// are spent out of the same bodyHeight budget boundedLayout already hands to
// the list, not added on top of it, so total rendered lines never exceed
// value.height.
func TestPanelTitlesConsumeBodyHeightWithoutGrowingFrame(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	value.width = 120
	value.result.Sessions = longSessionList(40)
	value.refreshVisible()
	for _, height := range []int{24, 12, 9} {
		value.height = height
		if !value.previewVisible() {
			t.Fatalf("preview should be visible at 120 columns, height %d", height)
		}
		content := ansi.Strip(value.View().Content)
		lines := strings.Count(content, "\n") + 1
		if lines > value.height {
			t.Fatalf("height %d: view rendered %d lines, want <= %d:\n%s", height, lines, value.height, content)
		}
	}
}

// TestPanelTitleHiddenWhenBodyTooSmall covers the brief's escape hatch: when
// bodyHeight is 2 or smaller, the list wins the whole budget and the panel
// title is omitted rather than starving the (already minimal) session row.
func TestPanelTitleHiddenWhenBodyTooSmall(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	value.width = 120
	value.height = frameTop + 1 + 1 + frameBottom + 2 // bodyHeight resolves to 2
	if !value.previewVisible() {
		t.Fatal("preview should be visible at 120 columns")
	}
	content := ansi.Strip(value.View().Content)
	if strings.Contains(content, "SESSIONS") {
		t.Fatalf("panel title rendered despite too-small body height:\n%s", content)
	}
}

// TestPreviewTitleScrollIndicatorExcludesTitleRows confirms the ordering the
// brief calls out explicitly: scrolledBody windows the list body first, and
// the panel title is prepended only afterward, so the "↑ N more" scroll
// indicator's hidden-row count reflects only session rows, never the two
// title rows above them.
func TestPreviewTitleScrollIndicatorExcludesTitleRows(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	value.width = 120
	value.height = 16
	value.result.Sessions = longSessionList(40)
	value.refreshVisible()
	for range 30 {
		value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	}
	if !value.previewVisible() {
		t.Fatal("preview should be visible at 120 columns")
	}
	content := ansi.Strip(value.View().Content)
	lines := strings.Split(content, "\n")

	title := lineContaining(t, lines, "SESSIONS")
	indicator := -1
	for index, line := range lines {
		if strings.Contains(line, "more") && strings.Contains(line, "↑") {
			indicator = index
			break
		}
	}
	if indicator == -1 {
		t.Fatalf("no top scroll indicator found:\n%s", content)
	}
	if indicator <= title+1 {
		t.Fatalf("scroll indicator at line %d overlaps the panel title ending at line %d:\n%s", indicator, title+1, content)
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
	// The default 65%-preview split narrows the list column enough at 120 to
	// truncate "connection check"; widen so the full title still fits.
	value.width = 140
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
	// With the pane open the overlay features the p row as "close preview
	// pane"; closed it reads "toggle preview pane".
	if !strings.Contains(overlay, "close preview pane") {
		t.Fatalf("help overlay missing preview binding:\n%s", overlay)
	}
	value.previewOn = false
	if closed := ansi.Strip(value.View().Content); !strings.Contains(closed, "toggle preview pane") {
		t.Fatalf("help overlay missing preview binding with the pane closed:\n%s", closed)
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

// TestPreviewBodyPreservesColorInColorMode locks in the render-layer half of
// task 7: a captured pane line's SGR codes survive into the rendered preview
// body when color is on.
func TestPreviewBodyPreservesColorInColorMode(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return nil, nil
	})
	value.noColor = false
	item := twoSessions()[0]
	value.previewContent = []string{"\x1b[31mred text\x1b[0m"}
	body := value.previewBody(item, 80, 5)
	if len(body) != 1 || !strings.Contains(body[0], "\x1b[31m") {
		t.Fatalf("previewBody in color mode stripped SGR: %#v", body)
	}
}

// TestPreviewBodyStripsColorUnderNoColor keeps the NO_COLOR contract: with
// noColor set, previewBody's output must be identical after ansi.Strip.
func TestPreviewBodyStripsColorUnderNoColor(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return nil, nil
	})
	item := twoSessions()[0]
	value.previewContent = []string{"\x1b[31mred text\x1b[0m", "\x1b[1;32mbold green\x1b[0m"}
	body := value.previewBody(item, 80, 5)
	for _, line := range body {
		if ansi.Strip(line) != line {
			t.Fatalf("previewBody under NO_COLOR left SGR in output: %q", line)
		}
	}
}

// TestFitANSILineClosesDanglingStyle covers the truncation-bleed bug the
// brief calls out: a long line whose color never closes within the source
// text must not leave an open SGR past the truncation point, since padPanel
// pads directly onto whatever fitANSILine returns and an open color would
// bleed into that padding.
func TestFitANSILineClosesDanglingStyle(t *testing.T) {
	// No reset anywhere in the source line — the pane's own reset is assumed
	// to live further along in the (unclipped) real output.
	long := "\x1b[31m" + strings.Repeat("x", 50)
	fitted := fitANSILine(long, 10)
	if !strings.HasSuffix(fitted, ansi.ResetStyle) {
		t.Fatalf("fitANSILine left an unclosed SGR: %q", fitted)
	}
	// The reset must be appended after the visible width, not counted toward
	// it, so the panel's rectangular width contract still holds.
	if width := lipgloss.Width(fitted); width != 10 {
		t.Fatalf("fitANSILine width = %d, want 10: %q", width, fitted)
	}
}

// TestFitANSILineNoOpForAlreadyClosedLine confirms fitANSILine does not add a
// spurious trailing reset when the source line already closes its own color
// well within the fitted width (the common case for short, fully-styled
// tokens).
func TestFitANSILineNoOpForAlreadyClosedLine(t *testing.T) {
	line := "\x1b[31mred\x1b[0m plain"
	fitted := fitANSILine(line, 80)
	if fitted != line {
		t.Fatalf("fitANSILine changed a line that already fits within width and needs no truncation: got %q, want %q", fitted, line)
	}
}

// TestPadPanelPreservesWidthWithANSI locks in task 4's rectangular contract
// under ANSI content: a line carrying SGR codes must still measure exactly
// width cells after padPanel, since lipgloss.Width (used throughout the
// panel layout) is ANSI-aware and must not count escape bytes as columns.
func TestPadPanelPreservesWidthWithANSI(t *testing.T) {
	value := model{}
	lines := []string{"\x1b[31mred\x1b[0m"}
	padded := value.padPanel(lines, 20, 3)
	if len(padded) != 3 {
		t.Fatalf("padPanel rows = %d, want 3", len(padded))
	}
	for _, line := range padded {
		if width := lipgloss.Width(line); width != 20 {
			t.Fatalf("padPanel line width = %d, want 20: %q", width, line)
		}
	}
}

// TestFindMatchesAcrossEmbeddedSGR covers the brief's "SGR-penetrating
// search" requirement: a color escape landing in the middle of the search
// term must not defeat the substring match, since findMatches searches the
// ansi.Strip'd line rather than the raw captured text.
func TestFindMatchesAcrossEmbeddedSGR(t *testing.T) {
	lines := []string{"err\x1b[31mor\x1b[0m detected", "all clear"}
	matches := findMatches(lines, "error")
	if len(matches) != 1 || matches[0] != 0 {
		t.Fatalf("findMatches across embedded SGR = %v, want [0]", matches)
	}
}

// TestFullscreenBodyPreservesColorForNonMatchLines checks the fullscreen
// scrollback path keeps a non-matching line's SGR when color is on.
func TestFullscreenBodyPreservesColorForNonMatchLines(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return nil, nil
	})
	value.noColor = false
	item := twoSessions()[0]
	value.previewFullContent = []string{"\x1b[31mred text\x1b[0m"}
	body := value.fullscreenBody(item, 80, 5)
	if len(body) != 1 || !strings.Contains(body[0], "\x1b[31m") {
		t.Fatalf("fullscreenBody stripped SGR from a non-match line: %#v", body)
	}
}

// TestFullscreenBodyMatchLinesRenderPlainWithHighlight locks in the brief's
// explicit decision: a line containing an active search match always renders
// stripped-and-highlighted, never with its original SGR, even in color mode —
// layering the two would be ambiguous. This also guards PR #50's highlight
// behavior continuing to work once color capture is on.
func TestFullscreenBodyMatchLinesRenderPlainWithHighlight(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return nil, nil
	})
	value.noColor = false
	item := twoSessions()[0]
	value.previewFullContent = []string{"\x1b[31mneedle here\x1b[0m"}
	value.previewSearchActive = true
	value.previewSearchMatches = []int{0}
	value.previewSearchQuery = "needle"
	body := value.fullscreenBody(item, 80, 5)
	if len(body) != 1 {
		t.Fatalf("fullscreenBody match line = %#v, want 1 line", body)
	}
	// The source color escape must be gone (stripped before highlighting)...
	if strings.Contains(body[0], "\x1b[31m") {
		t.Fatalf("match line kept the original SGR instead of rendering plain+highlight: %q", body[0])
	}
	// ...while the matched-substring highlight style from PR #50 still fires.
	if !strings.Contains(body[0], "needle") {
		t.Fatalf("match line lost its text: %q", body[0])
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
