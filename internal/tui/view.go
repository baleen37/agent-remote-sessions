package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/baleen37/agent-remote-sessions/internal/output"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

const (
	defaultWidth        = 80
	providerColumnWidth = 70
	clientColumnWidth   = 55
	spinnerInterval     = 100 * time.Millisecond
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (value model) View() tea.View {
	terminalWidth := value.contentWidth()
	inset, width := contentFrame(terminalWidth)
	if value.showHelp {
		return value.helpOverlay(inset, width)
	}
	previewShown := value.previewVisible()
	listWidth := width
	previewCols := 0
	if previewShown {
		listWidth, previewCols = previewWidth(width)
	}
	body, selectedLine := value.sessionLines(listWidth)
	var details []string
	selected, hasSelection := value.selectedSession()
	if hasSelection {
		details = detailLines(selected, width, value.deps.Now())
	}
	diagnostics := value.diagnostics(width)
	var search []string
	if value.composing {
		prefix := "send to " + sessionTitle(value.composeTarget) + ": "
		if !value.noColor {
			prefix = value.styles.selectedCursor.Render(prefix)
		}
		search = append(search, fitLine(prefix+value.compose, width))
	} else if value.query != "" || value.searching {
		prefix := "search: "
		if value.searching {
			prefix = "/"
			if !value.noColor {
				prefix = value.styles.selectedCursor.Render(prefix)
			}
		}
		count := ""
		if value.query != "" {
			count = fmt.Sprintf("   %d/%d", value.matched, len(value.result.Sessions))
			if !value.noColor {
				count = value.styles.muted.Render(count)
			}
		}
		search = append(search, fitLine(prefix+value.query+count, width))
	}

	panelHeight := len(body)
	if value.height > 0 {
		var bodyHeight int
		details, diagnostics, bodyHeight = value.boundedLayout(details, selected, diagnostics, len(search), width)
		body = value.scrolledBody(body, selectedLine, bodyHeight, listWidth)
		panelHeight = bodyHeight
	}
	for index, detail := range details {
		details[index] = value.mutedText(detail, width)
	}

	if previewShown {
		body = value.joinPreview(body, listWidth, previewCols, panelHeight)
	}

	lines := []string{fitLine(value.header(), width), ""}
	lines = append(lines, body...)
	lines = append(lines, "")
	lines = append(lines, details...)
	lines = append(lines, diagnostics...)
	lines = append(lines, search...)
	lines = append(lines, "", value.mutedText(value.help(width), width))
	margin := strings.Repeat(" ", inset)
	for index, line := range lines {
		if line != "" {
			lines[index] = margin + line
		}
	}
	return tea.View{Content: strings.Join(lines, "\n"), AltScreen: true}
}

func (value model) helpOverlay(inset, width int) tea.View {
	bindings := [][2]string{
		{"↑↓ / jk", "move"},
		{"h / l", "fold / unfold group"},
		{"g / G · Home / End", "jump to top / end"},
		{"1-9", "jump to group"},
		{"PgUp / PgDn · Ctrl+U / Ctrl+D", "page up / down"},
		{"/", "search"},
		{"! / @ / #", "filter attached / running / saved"},
		{"p", "toggle preview pane"},
		{"x", "kill session (3s grace · u undo)"},
		{"m", "send a line without attaching"},
		{"enter", "attach session · toggle group"},
		{"space", "toggle group"},
		{"r", "refresh"},
		{"q", "quit"},
		{"Ctrl+Q", "detach from an attached session"},
	}
	keyWidth := 0
	for _, binding := range bindings {
		keyWidth = max(keyWidth, lipgloss.Width(binding[0]))
	}
	title := "ars keys"
	if !value.noColor {
		title = value.styles.title.Render(title)
	}
	lines := []string{fitLine(title, width), ""}
	for _, binding := range bindings {
		key := binding[0] + strings.Repeat(" ", max(0, keyWidth-lipgloss.Width(binding[0])))
		plain := fitLine(key+"  "+binding[1], width)
		if !value.noColor {
			description := strings.TrimPrefix(plain, key+"  ")
			plain = key + "  " + value.styles.muted.Render(description)
		}
		lines = append(lines, plain)
	}
	lines = append(lines, "", value.mutedText("● attached · ◐ running · ○ saved", width), value.mutedText("? / esc / q to close", width))
	margin := strings.Repeat(" ", inset)
	for index, line := range lines {
		if line != "" {
			lines[index] = margin + line
		}
	}
	return tea.View{Content: strings.Join(lines, "\n"), AltScreen: true}
}

// boundedLayout bounds the detail and diagnostics lines to the terminal
// height and returns them alongside the height left for the session list.
// movePage derives its page step from the same computation so paging matches
// one visible screen.
//
// Diagnostics (errors, then warnings, then the status line last) compete
// with the body for the same budget instead of only getting body leftovers:
// otherwise a full-screen body (a long list, or a short one padded by the
// preview pane) starves diagnostics to zero and the status line kill/send
// rely on for feedback never renders. When starved, diagnostics are
// truncated from the front so the status line — the last element — is the
// last one dropped, and at least one line is reserved for it up front so it
// survives unless height is too small to fit anything past the fixed frame.
func (value model) boundedLayout(details []string, selected session.Session, diagnostics []string, searchLines, width int) ([]string, []string, int) {
	if value.height <= 0 {
		return details, diagnostics, len(value.rows)
	}
	statusFloor := 0
	if value.status != "" {
		statusFloor = 1
	}
	detailHeight := value.height - (2 + 1 + 1 + statusFloor + searchLines + 2)
	if len(details) > detailHeight {
		details = boundedDetailLines(selected, width, detailHeight, value.deps.Now())
	}
	diagnosticHeight := value.height - (2 + 1 + len(details) + 1 + searchLines + 2)
	diagnosticHeight = max(diagnosticHeight, statusFloor)
	if len(diagnostics) > diagnosticHeight {
		diagnostics = diagnostics[len(diagnostics)-diagnosticHeight:]
	}
	bodyHeight := max(1, value.height-(2+1+len(details)+len(diagnostics)+searchLines+2))
	return details, diagnostics, bodyHeight
}

func (value model) sessionLines(width int) ([]string, int) {
	if len(value.rows) == 0 {
		if value.query != "" {
			return []string{fitLine(fmt.Sprintf("  no matches for %q · esc to clear", value.query), width)}, 0
		}
		if value.filterActive() {
			return []string{fitLine("  no sessions match filter · esc to clear", width)}, 0
		}
		hint := value.mutedText("  start a claude/codex session, or add a remote with: ars remote add <host>", width)
		return []string{"  no sessions yet", "", hint}, 0
	}
	layout := newRowLayout(value.result.Sessions, value.stale, width, value.deps.Now(), value.deps.LocalTarget)
	lines := make([]string, 0, len(value.rows))
	for index, row := range value.rows {
		selected := index == value.selected
		switch row.kind {
		case rowHeader:
			lines = append(lines, value.renderHeader(row, selected, width))
		case rowMore:
			lines = append(lines, value.renderMore(row, selected, width))
		default:
			lines = append(lines, value.renderRow(row, selected, layout))
		}
	}
	return lines, value.selected
}

func rowSessions(rows []listRow) []session.Session {
	items := make([]session.Session, 0, len(rows))
	for _, row := range rows {
		if row.kind == rowSession {
			items = append(items, row.session)
		}
	}
	return items
}

func (value model) renderHeader(row listRow, selected bool, width int) string {
	cursor := "  "
	if selected {
		cursor = "> "
		if !value.noColor {
			cursor = value.styles.selectedCursor.Render(cursor)
		}
	}
	symbol := "▾"
	if row.collapsed {
		symbol = "▸"
	}
	text := fmt.Sprintf("%s %s (%d)", symbol, row.project, row.count)
	if row.collapsed && row.state != session.RuntimeSaved {
		text += " " + value.stateText(stateSymbol(row.state), row.state)
	}
	padding := rowPadding(width)
	line := fitLine(cursor+text, width-2*padding)
	line = strings.Repeat(" ", padding) + line
	line += strings.Repeat(" ", max(0, width-padding-lipgloss.Width(line)))
	line += strings.Repeat(" ", padding)
	if selected && !value.noColor {
		line = value.selectedBackground(line)
	}
	return line
}

func (value model) renderMore(row listRow, selected bool, width int) string {
	cursor := "  "
	if selected {
		cursor = "> "
		if !value.noColor {
			cursor = value.styles.selectedCursor.Render(cursor)
		}
	}
	text := fmt.Sprintf("… %d more", row.count)
	if !value.noColor {
		text = value.styles.muted.Render(text)
	}
	padding := rowPadding(width)
	line := fitLine(cursor+"└─ "+text, width-2*padding)
	line = strings.Repeat(" ", padding) + line
	line += strings.Repeat(" ", max(0, width-padding-lipgloss.Width(line)))
	line += strings.Repeat(" ", padding)
	if selected && !value.noColor {
		line = value.selectedBackground(line)
	}
	return line
}

// scrolledBody windows the list body to the viewport and, when rows fall
// outside it, spends the first and/or last viewport line on a muted indicator
// counting the hidden rows. Indicators take the place of a content row rather
// than adding a line, so the body stays exactly height tall and the
// boundedLayout contract holds; the selected row always keeps a content slot.
func (value model) scrolledBody(lines []string, selected, height, width int) []string {
	if height >= len(lines) || height <= 0 {
		return lines
	}
	// Resolve how many viewport lines the indicators claim. The content window
	// is bottom-anchored on the selection over the remaining rows; its bounds
	// then decide whether each indicator is actually needed. This is circular —
	// adding an indicator shrinks the window, which can reveal the need for the
	// other indicator — so iterate until the counts stop changing, which the
	// window over a finite list always reaches. Indicators may claim at most
	// height-1 lines so at least one content row (always including the
	// selection) survives; a 1-line viewport shows only the selected row.
	budget := min(2, height-1)
	topInd, botInd := 0, 0
	var start, rows int
	for range 3 {
		rows = height - topInd - botInd
		start = selected - rows + 1
		start = max(0, min(start, len(lines)-rows))
		newTop, newBot := 0, 0
		if start > 0 && budget >= 1 {
			newTop = 1
		}
		if start+rows < len(lines) && newTop < budget {
			newBot = 1
		}
		if newTop == topInd && newBot == botInd {
			break
		}
		topInd, botInd = newTop, newBot
	}
	window := make([]string, 0, height)
	if topInd == 1 {
		window = append(window, value.scrollIndicator("↑", start, width))
	}
	window = append(window, lines[start:start+rows]...)
	if botInd == 1 {
		window = append(window, value.scrollIndicator("↓", len(lines)-(start+rows), width))
	}
	return window
}

func (value model) scrollIndicator(arrow string, hidden, width int) string {
	text := fmt.Sprintf("%s %d more", arrow, hidden)
	padding := rowPadding(width)
	line := strings.Repeat(" ", padding) + fitLine(text, width-2*padding)
	return value.mutedText(line, width)
}

func (value model) contentWidth() int {
	if value.width > 0 {
		return value.width
	}
	return defaultWidth
}

func (value model) header() string {
	active := 0
	for _, item := range value.result.Sessions {
		if item.Runtime.State != session.RuntimeSaved {
			active++
		}
	}
	peers := 0
	for _, host := range value.result.Hosts {
		if host.Target != value.deps.LocalTarget {
			peers++
		}
	}
	stats := fmt.Sprintf("  %d active · %d recent", active, len(value.result.Sessions)-active)
	switch peers {
	case 0:
	case 1:
		stats += " · 1 peer"
	default:
		stats += fmt.Sprintf(" · %d peers", peers)
	}
	if value.collecting {
		stats += " · " + spinnerFrames[value.spinner%len(spinnerFrames)]
	}
	if symbols := value.filterSymbols(); symbols != "" {
		stats += " · filter " + symbols
	}
	if value.noColor {
		return "ars" + stats
	}
	return value.styles.title.Render("ars") + value.styles.muted.Render(stats)
}

// filterSymbols returns the state symbols for the active filter, in
// attached/running/saved order, or "" when no filter is active.
func (value model) filterSymbols() string {
	symbols := ""
	for _, state := range []session.RuntimeState{session.RuntimeAttached, session.RuntimeRunning, session.RuntimeSaved} {
		if value.stateFilter[state] {
			symbols += stateSymbol(state)
		}
	}
	return symbols
}

func sessionTitle(item session.Session) string {
	if item.Title != "" {
		return item.Title
	}
	return item.NativeID[:8]
}

func activityAge(now, updatedAt time.Time) string {
	age := now.Sub(updatedAt)
	if age < time.Minute {
		return "now"
	}
	if age < time.Hour {
		return fmt.Sprintf("%dm", int(age/time.Minute))
	}
	if age < 24*time.Hour {
		return fmt.Sprintf("%dh", int(age/time.Hour))
	}
	return fmt.Sprintf("%dd", int(age/(24*time.Hour)))
}

func (value model) selectedSession() (session.Session, bool) {
	row, ok := value.selectedRow()
	if !ok || row.kind != rowSession {
		return session.Session{}, false
	}
	return row.session, true
}

func humanizedActivity(now, updatedAt time.Time) string {
	age := activityAge(now, updatedAt)
	if age == "now" {
		return "now"
	}
	return age + " ago"
}

func detailLines(item session.Session, width int, now time.Time) []string {
	fields := []string{item.CWD, item.NativeID, humanizedActivity(now, item.UpdatedAt)}
	lines := make([]string, 0, len(fields))
	line := ""
	for _, field := range fields {
		candidate := field
		if line != "" {
			candidate = line + " · " + field
		}
		if lipgloss.Width(candidate) <= width {
			line = candidate
			continue
		}
		if line != "" {
			lines = append(lines, line)
		}
		wrapped := strings.Split(ansi.Hardwrap(field, width, true), "\n")
		lines = append(lines, wrapped[:len(wrapped)-1]...)
		line = wrapped[len(wrapped)-1]
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

func boundedDetailLines(item session.Session, width, height int, now time.Time) []string {
	if height <= 0 {
		return nil
	}
	fields := []string{item.CWD, item.NativeID, humanizedActivity(now, item.UpdatedAt)}
	if height == 1 {
		fields = fields[1:2]
	} else if height == 2 {
		fields = fields[:2]
	}
	lines := make([]string, 0, len(fields))
	for _, field := range fields {
		lines = append(lines, fitLine(field, width))
	}
	return lines
}

func (value model) diagnostics(width int) []string {
	lines := make([]string, 0, len(value.result.Errors)+len(value.result.Warnings)+1)
	for _, diagnostic := range value.result.Errors {
		lines = append(lines, value.errorText("✕ "+diagnosticLine(diagnostic, value.deps.LocalTarget), width))
	}
	for _, diagnostic := range value.result.Warnings {
		lines = append(lines, value.mutedText(diagnosticLine(diagnostic, value.deps.LocalTarget), width))
	}
	if value.status != "" {
		status := value.mutedText(value.status, width)
		if strings.HasPrefix(value.status, "attach failed:") || strings.HasPrefix(value.status, "kill failed:") || strings.HasPrefix(value.status, "send failed:") {
			status = value.errorText(value.status, width)
		}
		lines = append(lines, status)
	}
	return lines
}

func diagnosticLine(value output.HostError, localTarget string) string {
	if value.Host == localTarget {
		return fmt.Sprintf("%s (%s)", value.Message, value.Code)
	}
	return fmt.Sprintf("%s: %s (%s)", value.Host, value.Message, value.Code)
}

func stateSymbol(state session.RuntimeState) string {
	switch state {
	case session.RuntimeAttached:
		return "●"
	case session.RuntimeRunning:
		return "◐"
	default:
		return "○"
	}
}

func (value model) stateText(text string, state session.RuntimeState) string {
	if value.noColor {
		return text
	}
	switch state {
	case session.RuntimeAttached:
		return value.styles.attached.Render(text)
	case session.RuntimeRunning:
		return value.styles.running.Render(text)
	default:
		return value.styles.saved.Render(text)
	}
}

func (value model) mutedText(text string, width int) string {
	text = fitLine(text, width)
	if value.noColor {
		return text
	}
	return value.styles.muted.Render(text)
}

func (value model) errorText(text string, width int) string {
	text = fitLine(text, width)
	if value.noColor {
		return text
	}
	return value.styles.failure.Render(text)
}

func fitLine(line string, width int) string {
	if lipgloss.Width(line) <= width {
		return line
	}
	return ansi.Truncate(line, width, "…")
}

func (value model) help(width int) string {
	separator := "   "
	if width < 75 {
		separator = "  "
	}
	if value.searching {
		return strings.Join([]string{"type to filter", "enter apply", "esc cancel"}, separator)
	}
	if value.composing {
		return strings.Join([]string{"type message", "enter send", "esc cancel"}, separator)
	}
	action := "enter attach"
	if row, ok := value.selectedRow(); ok {
		switch row.kind {
		case rowHeader:
			action = "enter toggle"
		case rowMore:
			action = "enter expand"
		}
	}
	items := []string{"↑↓/jk move"}
	if width >= 75 {
		items = append(items, "h/l fold", "g/G top/end", "1-9 group", "!@# filter", "x kill", "m msg")
	}
	items = append(items, "/ search")
	if value.query != "" || value.filterActive() {
		items = append(items, "esc clear")
	}
	if value.contentWidth() >= previewMinWidth {
		label := "p preview"
		if !value.previewOn {
			label = "p preview off"
		}
		items = append(items, label)
	}
	items = append(items, action, "r refresh", "q quit", "? help")
	return joinFooterItems(items, separator, width)
}

// joinFooterItems joins footer hints with separator, dropping the lowest
// priority droppable hints (in this order) until the line fits width.
// Higher priority items (navigation, search, quit, help, etc.) are never
// dropped, so on very narrow terminals the line may still overflow.
func joinFooterItems(items []string, separator string, width int) string {
	droppable := []string{"m msg", "x kill", "!@# filter", "1-9 group", "g/G top/end", "h/l fold"}
	line := strings.Join(items, separator)
	for _, drop := range droppable {
		if lipgloss.Width(line) <= width {
			break
		}
		items = removeItem(items, drop)
		line = strings.Join(items, separator)
	}
	return line
}

func removeItem(items []string, target string) []string {
	filtered := make([]string, 0, len(items))
	for _, item := range items {
		if item != target {
			filtered = append(filtered, item)
		}
	}
	return filtered
}
