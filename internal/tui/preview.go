package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

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
	value.previewFullKey = sessionKey{}
	value.previewFullContent = nil
	value.previewFullErr = ""
	value.previewFullPending = false
	value = value.clearFullscreenSearch()
	value.previewSearching = false
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
// fullscreen is open and no capture is already loaded or in flight for that
// same selection. A selection change is detected the same way the side
// panel's syncPreview does it — comparing against the key of the last capture
// — so switching sessions while fullscreen stays open recaptures instead of
// freezing the previous session's scrollback on screen. It also (re)starts
// the fullscreen tick.
func (value *model) syncFullPreview() tea.Cmd {
	if !value.previewFullscreen {
		return nil
	}
	selected, ok := value.selectedSession()
	if !ok {
		return nil
	}
	key := keyOf(selected)
	if key != value.previewFullKey {
		value.previewFullKey = key
		value.previewScrollOffset = 0
		value.previewFullContent = nil
		value.previewFullErr = ""
		value.previewFullPending = false
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
	value.recomputeFullscreenSearch()
	return value, nil
}

// recomputeFullscreenSearch keeps an active buffer search alive across a
// recapture: the query survives, but the match set is recomputed against the
// new content and re-anchored from the current viewport position, since the
// old line indices no longer necessarily point at the same text.
func (value *model) recomputeFullscreenSearch() {
	if !value.previewSearchActive {
		return
	}
	matches := findMatches(value.previewFullContent, value.previewSearchQuery)
	if len(matches) == 0 {
		value.previewSearchActive = false
		value.previewSearchMatches = nil
		value.previewSearchNoMatches = true
		return
	}
	value.previewSearchMatches = matches
	value.previewSearchIndex = nearestMatchIndex(matches, value.currentViewportLine())
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

// updateFullscreenSearchInput handles a key press while the fullscreen buffer
// search input is open: enter confirms (finding matches and jumping to the
// nearest one), esc cancels back to whatever search state existed before
// (matching the esc hierarchy — this only ever cancels the input, never the
// fullscreen view itself), and any other printable key is appended literally,
// exactly like the list search and compose inputs.
func (value model) updateFullscreenSearchInput(key tea.Key) model {
	switch key.Code {
	case tea.KeyEnter:
		return value.confirmFullscreenSearch()
	case tea.KeyEscape:
		value.previewSearching = false
		value.previewSearchQuery = ""
		return value
	case tea.KeyBackspace:
		_, size := utf8.DecodeLastRuneInString(value.previewSearchQuery)
		if size > 0 {
			value.previewSearchQuery = value.previewSearchQuery[:len(value.previewSearchQuery)-size]
		}
		return value
	}
	if key.Code == 'u' && key.Mod&tea.ModCtrl != 0 {
		value.previewSearchQuery = ""
		return value
	}
	if printable(key.Text) {
		value.previewSearchQuery += key.Text
	}
	return value
}

// confirmFullscreenSearch closes the input and, for a non-empty query,
// searches the whole buffer case-insensitively. A match jumps the viewport to
// the nearest match at or after the current position (wrapping if none is);
// zero matches leaves the viewport untouched and previewSearchActive false,
// with previewSearchNoMatches set so the view can render "no matches"
// feedback instead of silently doing nothing.
func (value model) confirmFullscreenSearch() model {
	value.previewSearching = false
	query := value.previewSearchQuery
	value.previewSearchNoMatches = false
	if query == "" {
		value.previewSearchActive = false
		value.previewSearchMatches = nil
		return value
	}
	matches := findMatches(value.previewFullContent, query)
	if len(matches) == 0 {
		value.previewSearchActive = false
		value.previewSearchMatches = nil
		value.previewSearchNoMatches = true
		return value
	}
	value.previewSearchActive = true
	value.previewSearchMatches = matches
	value.previewSearchIndex = nearestMatchIndex(matches, value.currentViewportLine())
	value.jumpToCurrentMatch()
	return value
}

// clearFullscreenSearch drops the active search — its matches, highlight and
// count — without touching the scrollback viewport, so esc from an active
// search returns the plain scrollback view at the position search left it.
func (value model) clearFullscreenSearch() model {
	value.previewSearchActive = false
	value.previewSearchNoMatches = false
	value.previewSearchQuery = ""
	value.previewSearchMatches = nil
	value.previewSearchIndex = 0
	return value
}

// advanceFullscreenSearch moves the current match by delta (1 for n, -1 for
// N), wrapping around both ends, and scrolls the viewport to it.
func (value *model) advanceFullscreenSearch(delta int) {
	if len(value.previewSearchMatches) == 0 {
		return
	}
	count := len(value.previewSearchMatches)
	value.previewSearchIndex = ((value.previewSearchIndex+delta)%count + count) % count
	value.jumpToCurrentMatch()
}

// jumpToCurrentMatch scrolls the viewport so the current match line is
// visible, anchoring it at the top of the viewport like the rest of
// fullscreen's offset scheme (offset 0 = tail).
func (value *model) jumpToCurrentMatch() {
	if value.previewSearchIndex < 0 || value.previewSearchIndex >= len(value.previewSearchMatches) {
		return
	}
	line := value.previewSearchMatches[value.previewSearchIndex]
	value.previewScrollOffset = clampScrollOffset(len(value.previewFullContent)-1-line, len(value.previewFullContent))
}

// currentViewportLine reports the buffer line the viewport is anchored at —
// the same line clampScrollOffset's inverse of previewScrollOffset resolves
// to — so a fresh confirm searches forward from what the user is looking at
// rather than always from the top or bottom of the whole buffer.
func (value model) currentViewportLine() int {
	total := len(value.previewFullContent)
	if total == 0 {
		return 0
	}
	line := total - 1 - value.previewScrollOffset
	if line < 0 {
		return 0
	}
	if line >= total {
		return total - 1
	}
	return line
}

// findMatches returns the indices of every line in lines containing query as
// a case-insensitive substring.
func findMatches(lines []string, query string) []int {
	if query == "" {
		return nil
	}
	needle := strings.ToLower(query)
	var matches []int
	for index, line := range lines {
		if strings.Contains(strings.ToLower(line), needle) {
			matches = append(matches, index)
		}
	}
	return matches
}

// nearestMatchIndex returns the index into matches of the first match at or
// after anchorLine, wrapping to the first match overall if none qualifies.
func nearestMatchIndex(matches []int, anchorLine int) int {
	for index, line := range matches {
		if line >= anchorLine {
			return index
		}
	}
	return 0
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
		searchLine := value.fullscreenSearchLine(width)
		// The fixed frame the body has to share the screen with: the ars header
		// and its blank, the title and its blank, then the blank and the close
		// hint at the foot, plus one row when the search input or an active
		// search's match count is showing.
		frameLines := 6
		if searchLine != "" {
			frameLines++
		}
		lines = append(lines, value.fullscreenBody(selected, width, value.height-frameLines)...)
		if searchLine != "" {
			lines = append(lines, searchLine)
		}
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

// fullscreenSearchLine renders the bottom-of-frame search line: the live
// "/query" input while typing, the "i/n" match position once a search is
// active, or "no matches" feedback after a query with zero hits — empty when
// none of those apply, so callers can tell whether the line claims a row in
// the height budget.
func (value model) fullscreenSearchLine(width int) string {
	switch {
	case value.previewSearching:
		prefix := "/"
		if !value.noColor {
			prefix = value.styles.selectedCursor.Render(prefix)
		}
		return fitLine(prefix+value.previewSearchQuery, width)
	case value.previewSearchActive:
		count := fmt.Sprintf("%d/%d match", value.previewSearchIndex+1, len(value.previewSearchMatches))
		if len(value.previewSearchMatches) != 1 {
			count += "es"
		}
		return value.mutedText(count, width)
	case value.previewSearchNoMatches:
		return value.mutedText(fmt.Sprintf("no matches for %q", value.previewSearchQuery), width)
	default:
		return ""
	}
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
	matchSet := value.searchMatchSet()
	fitted := make([]string, 0, height)
	if topInd {
		fitted = append(fitted, value.scrollIndicator("↑", start, width))
	}
	for offset, line := range lines[start : start+rows] {
		lineIndex := start + offset
		plain := ansi.Strip(line)
		if matchSet[lineIndex] {
			fitted = append(fitted, value.highlightMatches(fitLine(plain, width), value.previewSearchQuery))
		} else {
			fitted = append(fitted, fitLine(plain, width))
		}
	}
	if botInd {
		fitted = append(fitted, value.scrollIndicator("↓", len(lines)-(start+rows), width))
	}
	return fitted
}

// searchMatchSet returns the set of buffer line indices an active search
// matched, or nil when no search is active, so fullscreenBody can decide
// per-line whether to highlight without a linear scan of the match slice for
// every visible row.
func (value model) searchMatchSet() map[int]bool {
	if !value.previewSearchActive || len(value.previewSearchMatches) == 0 {
		return nil
	}
	set := make(map[int]bool, len(value.previewSearchMatches))
	for _, line := range value.previewSearchMatches {
		set[line] = true
	}
	return set
}

// highlightMatches wraps every case-insensitive occurrence of query in line
// with the same emphasis style row selection uses, falling back to plain text
// under NO_COLOR. The line has already been through fitLine/ansi.Strip, so
// this only ever sees plain text.
func (value model) highlightMatches(line, query string) string {
	if query == "" || value.noColor {
		return line
	}
	needle := strings.ToLower(query)
	var builder strings.Builder
	rest := line
	restLower := strings.ToLower(line)
	for {
		index := strings.Index(restLower, needle)
		if index < 0 {
			builder.WriteString(rest)
			break
		}
		builder.WriteString(rest[:index])
		builder.WriteString(value.styles.matched.Render(rest[index : index+len(needle)]))
		rest = rest[index+len(needle):]
		restLower = restLower[index+len(needle):]
	}
	return builder.String()
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
