package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

const (
	previewMinWidth = 100
	previewInterval = 2 * time.Second
	previewGutter   = 2
	// fullscreenPageLinesFraction sizes a PgUp/PgDn/Ctrl+U/Ctrl+D scroll step
	// as a fraction of the terminal height, so a page always leaves visible
	// overlap with the previous one.
	fullscreenPageLinesFraction = 2
)

type previewMsg struct {
	key     sessionKey
	content []byte
	err     error
}

type previewTickMsg struct {
	key sessionKey
}

// fullPreviewMsg carries the result of a fullscreen scrollback capture.
type fullPreviewMsg struct {
	key     sessionKey
	content []byte
	err     error
}

type fullPreviewTickMsg struct {
	key sessionKey
}

// previewVisible reports whether the preview panel should render: enabled by
// the user, wide enough, and wired with a Preview dependency.
func (value model) previewVisible() bool {
	return value.previewOn && value.deps.Preview != nil && value.contentWidth() >= previewMinWidth
}

// closePreview clears the capture state the panel renders from, so a reopened
// pane loads fresh instead of showing a stale capture.
func (value model) closePreview() model {
	value.previewKey = sessionKey{}
	value.previewContent = nil
	value.previewErr = ""
	value.previewPending = false
	return value
}

// previewWidth splits the content width, giving the list priority and the
// preview roughly 40%.
func previewWidth(total int) (list, preview int) {
	preview = total * 2 / 5
	list = total - preview - previewGutter
	return list, preview
}

// syncPreview issues a capture for the current selection when the preview is
// visible and the selection changed since the last load. Saved sessions need
// no capture; their panel is rendered locally. It also (re)starts the tick.
func (value *model) syncPreview() tea.Cmd {
	if !value.previewVisible() {
		return nil
	}
	selected, ok := value.selectedSession()
	if !ok {
		value.previewKey = sessionKey{}
		value.previewContent = nil
		value.previewErr = ""
		value.previewPending = false
		return nil
	}
	key := keyOf(selected)
	if key == value.previewKey {
		return nil
	}
	value.previewKey = key
	value.previewContent = nil
	value.previewErr = ""
	if selected.Runtime.State == session.RuntimeSaved {
		value.previewPending = false
		return nil
	}
	value.previewPending = true
	return tea.Batch(capturePreview(value.ctx, value.deps.Preview, selected), previewTick(key))
}

func capturePreview(ctx context.Context, capture func(context.Context, session.Session) ([]byte, error), item session.Session) tea.Cmd {
	key := keyOf(item)
	return func() tea.Msg {
		content, err := capture(ctx, item)
		return previewMsg{key: key, content: content, err: err}
	}
}

func previewTick(key sessionKey) tea.Cmd {
	return tea.Tick(previewInterval, func(time.Time) tea.Msg {
		return previewTickMsg{key: key}
	})
}

func (value model) updatePreview(message previewMsg) (model, tea.Cmd) {
	if message.key != value.previewKey {
		return value, nil
	}
	value.previewPending = false
	if message.err != nil {
		value.previewErr = message.err.Error()
		value.previewContent = nil
		return value, nil
	}
	value.previewErr = ""
	value.previewContent = splitPreview(message.content)
	return value, nil
}

func (value model) updatePreviewTick(message previewTickMsg) (model, tea.Cmd) {
	if message.key != value.previewKey || !value.previewVisible() {
		return value, nil
	}
	selected, ok := value.selectedSession()
	if !ok || keyOf(selected) != message.key || selected.Runtime.State == session.RuntimeSaved {
		return value, nil
	}
	// A capture is already in flight; keep the tick alive but do not stack a
	// second capture, so a slow SSH probe cannot pile up processes.
	if value.previewPending {
		return value, previewTick(message.key)
	}
	value.previewPending = true
	return value, tea.Batch(capturePreview(value.ctx, value.deps.Preview, selected), previewTick(message.key))
}

// enterFullscreen opens the fullscreen preview and resets its scrollback
// state, so a reopened pane loads fresh at the tail rather than the offset
// left over from a previous visit.
func (value model) enterFullscreen() model {
	value.previewFullscreen = true
	value.previewScrollOffset = 0
	value.previewFullContent = nil
	value.previewFullErr = ""
	value.previewFullPending = false
	return value
}

// fullscreenHistoryCapture picks the scrollback capture for fullscreen,
// falling back to the tail-only Preview capture when PreviewHistory is not
// wired, so fullscreen still renders something rather than nothing.
func (value model) fullscreenHistoryCapture() func(context.Context, session.Session) ([]byte, error) {
	if value.deps.PreviewHistory != nil {
		return value.deps.PreviewHistory
	}
	return value.deps.Preview
}

// syncFullPreview issues a scrollback capture for the current selection when
// fullscreen is open and no capture is already loaded or in flight. It also
// (re)starts the fullscreen tick.
func (value *model) syncFullPreview() tea.Cmd {
	if !value.previewFullscreen {
		return nil
	}
	selected, ok := value.selectedSession()
	if !ok {
		return nil
	}
	if selected.Runtime.State == session.RuntimeSaved {
		return nil
	}
	capture := value.fullscreenHistoryCapture()
	if capture == nil {
		return nil
	}
	if value.previewFullPending || value.previewFullContent != nil || value.previewFullErr != "" {
		return nil
	}
	key := keyOf(selected)
	value.previewFullPending = true
	return tea.Batch(captureFullPreview(value.ctx, capture, selected), fullPreviewTick(key))
}

func captureFullPreview(ctx context.Context, capture func(context.Context, session.Session) ([]byte, error), item session.Session) tea.Cmd {
	key := keyOf(item)
	return func() tea.Msg {
		content, err := capture(ctx, item)
		return fullPreviewMsg{key: key, content: content, err: err}
	}
}

func fullPreviewTick(key sessionKey) tea.Cmd {
	return tea.Tick(previewInterval, func(time.Time) tea.Msg {
		return fullPreviewTickMsg{key: key}
	})
}

func (value model) updateFullPreview(message fullPreviewMsg) (model, tea.Cmd) {
	selected, ok := value.selectedSession()
	if !ok || keyOf(selected) != message.key {
		return value, nil
	}
	value.previewFullPending = false
	if message.err != nil {
		value.previewFullErr = message.err.Error()
		value.previewFullContent = nil
		return value, nil
	}
	value.previewFullErr = ""
	lines := splitPreview(message.content)
	if lines == nil {
		lines = []string{}
	}
	value.previewFullContent = lines
	value.previewScrollOffset = clampScrollOffset(value.previewScrollOffset, len(lines))
	return value, nil
}

func (value model) updateFullPreviewTick(message fullPreviewTickMsg) (model, tea.Cmd) {
	if !value.previewFullscreen {
		return value, nil
	}
	selected, ok := value.selectedSession()
	if !ok || keyOf(selected) != message.key || selected.Runtime.State == session.RuntimeSaved {
		return value, nil
	}
	capture := value.fullscreenHistoryCapture()
	if capture == nil {
		return value, nil
	}
	// A capture is already in flight; keep the tick alive but do not stack a
	// second capture, mirroring the side-panel tick's backpressure.
	if value.previewFullPending {
		return value, fullPreviewTick(message.key)
	}
	value.previewFullPending = true
	return value, tea.Batch(captureFullPreview(value.ctx, capture, selected), fullPreviewTick(message.key))
}

// scrollFullscreen moves the scrollback viewport by delta lines, where a
// positive delta scrolls up (toward older output) and clamps at both ends:
// 0 is the tail (bottom) and len(previewFullContent)-1 is the oldest visible
// line reachable while still showing at least one line.
func (value *model) scrollFullscreen(delta int) {
	value.previewScrollOffset = clampScrollOffset(value.previewScrollOffset+delta, len(value.previewFullContent))
}

func clampScrollOffset(offset, lineCount int) int {
	maxOffset := lineCount - 1
	if maxOffset < 0 {
		maxOffset = 0
	}
	if offset < 0 {
		return 0
	}
	if offset > maxOffset {
		return maxOffset
	}
	return offset
}

// fullscreenPageLines sizes a page scroll step from the terminal height.
func fullscreenPageLines(height int) int {
	lines := height / fullscreenPageLinesFraction
	if lines < 1 {
		return 1
	}
	return lines
}

func splitPreview(content []byte) []string {
	text := strings.TrimRight(string(content), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// previewPanel renders the preview column: a session header, a divider, and
// the tail of the captured pane fitted to the panel box.
func (value model) previewPanel(width, height int) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	lines := make([]string, 0, height)
	selected, ok := value.selectedSession()
	if !ok {
		return value.padPanel(lines, width, height)
	}

	header := sessionTitle(selected)
	lines = append(lines, value.previewHeader(header, width))
	if height > 1 {
		lines = append(lines, value.mutedText(strings.Repeat("─", width), width))
	}

	body := value.previewBody(selected, width, height-len(lines))
	lines = append(lines, body...)
	return value.padPanel(lines, width, height)
}

func (value model) previewHeader(text string, width int) string {
	if value.noColor {
		return fitLine(text, width)
	}
	return value.styles.title.Render(fitLine(text, width))
}

func (value model) previewBody(selected session.Session, width, height int) []string {
	if height <= 0 {
		return nil
	}
	if selected.Runtime.State == session.RuntimeSaved {
		return []string{value.mutedText("no live pane", width)}
	}
	if value.previewErr != "" {
		return []string{value.mutedText("preview unavailable", width)}
	}
	if len(value.previewContent) == 0 {
		return []string{value.mutedText("loading preview…", width)}
	}
	lines := value.previewContent
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	fitted := make([]string, len(lines))
	for index, line := range lines {
		fitted[index] = fitLine(ansi.Strip(line), width)
	}
	return fitted
}

// joinPreview places the preview panel to the right of the list body, one row
// per line, separated by a fixed gutter. The joined block is as tall as the
// panel so the preview can use the full body height even when the list is
// short; missing list rows become blank padding.
func (value model) joinPreview(body []string, listWidth, previewCols, height int) []string {
	if height < len(body) {
		height = len(body)
	}
	panel := value.previewPanel(previewCols, height)
	gutter := strings.Repeat(" ", previewGutter)
	joined := make([]string, height)
	for index := range height {
		line := ""
		if index < len(body) {
			line = body[index]
		}
		if pad := listWidth - lipgloss.Width(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		right := ""
		if index < len(panel) {
			right = panel[index]
		}
		joined[index] = line + gutter + right
	}
	return joined
}

// fullscreenPreview renders the preview as an alternate full-screen view,
// replacing the list: the session title, the captured pane's scrollback
// scrolled to previewScrollOffset over the remaining height, and a close
// hint. Like helpOverlay it owns the whole frame, so the list, details,
// diagnostics and footer are absent.
func (value model) fullscreenPreview(inset, width int) tea.View {
	lines := []string{fitLine(value.header(), width), ""}
	selected, ok := value.selectedSession()
	if ok {
		lines = append(lines, value.previewHeader(sessionTitle(selected), width), "")
		// The fixed frame the body has to share the screen with: the ars header
		// and its blank, the title and its blank, then the blank and the close
		// hint at the foot.
		const frameLines = 6
		lines = append(lines, value.fullscreenBody(selected, width, value.height-frameLines)...)
	}
	lines = append(lines, "", value.mutedText("f / esc to close", width))
	margin := strings.Repeat(" ", inset)
	for index, line := range lines {
		if line != "" {
			lines[index] = margin + line
		}
	}
	return tea.View{Content: strings.Join(lines, "\n"), AltScreen: true}
}

// fullscreenBody renders the fullscreen scrollback body: a saved session's
// placeholder, an error notice, a loading notice while the first capture is
// in flight, or the captured lines windowed at previewScrollOffset with
// scrollIndicator rows — the same "↑/↓ N more" convention the list uses —
// for whichever side(s) of the viewport have hidden lines.
func (value model) fullscreenBody(selected session.Session, width, height int) []string {
	if height <= 0 {
		return nil
	}
	if selected.Runtime.State == session.RuntimeSaved {
		return []string{value.mutedText("no live pane", width)}
	}
	if value.previewFullErr != "" {
		return []string{value.mutedText("preview unavailable", width)}
	}
	if value.previewFullContent == nil {
		return []string{value.mutedText("loading preview…", width)}
	}
	lines := value.previewFullContent
	start, rows, topInd, botInd := fullscreenWindow(len(lines), value.previewScrollOffset, height)
	fitted := make([]string, 0, height)
	if topInd {
		fitted = append(fitted, value.scrollIndicator("↑", start, width))
	}
	for _, line := range lines[start : start+rows] {
		fitted = append(fitted, fitLine(ansi.Strip(line), width))
	}
	if botInd {
		fitted = append(fitted, value.scrollIndicator("↓", len(lines)-(start+rows), width))
	}
	return fitted
}

// fullscreenWindow resolves the visible line range [start, start+rows) for a
// scrollback of lineCount lines viewed through a height-tall viewport
// anchored offset lines up from the tail (offset 0 = bottom), together with
// whether the top and/or bottom indicator is needed. It mirrors
// scrolledBody's iteration: claiming an indicator shrinks the content rows
// available, which can in turn reveal (or remove the need for) the other
// indicator, so the counts are resolved by iterating to a fixed point.
// Indicators may claim at most height-1 rows so at least one content line
// survives.
func fullscreenWindow(lineCount, offset, height int) (start, rows int, topInd, botInd bool) {
	if height <= 0 || lineCount <= 0 {
		return 0, 0, false, false
	}
	if height >= lineCount {
		return 0, lineCount, false, false
	}
	budget := min(2, height-1)
	end := lineCount - offset
	if end > lineCount {
		end = lineCount
	}
	claimedTop, claimedBot := 0, 0
	for range 3 {
		rows = height - claimedTop - claimedBot
		start = end - rows
		if start < 0 {
			start = 0
		}
		newTop, newBot := 0, 0
		if start > 0 && budget >= 1 {
			newTop = 1
		}
		if start+rows < lineCount && newTop < budget {
			newBot = 1
		}
		if newTop == claimedTop && newBot == claimedBot {
			break
		}
		claimedTop, claimedBot = newTop, newBot
	}
	rows = height - claimedTop - claimedBot
	start = end - rows
	if start < 0 {
		start = 0
	}
	if start+rows > lineCount {
		rows = lineCount - start
	}
	return start, rows, claimedTop == 1, claimedBot == 1
}

func (value model) padPanel(lines []string, width, height int) []string {
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	for index, line := range lines {
		if pad := width - lipgloss.Width(line); pad > 0 {
			lines[index] = line + strings.Repeat(" ", pad)
		}
	}
	return lines[:height]
}
