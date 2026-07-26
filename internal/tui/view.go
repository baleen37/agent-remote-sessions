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
	// frameTop is the header, pill bar, and the blank line under them.
	frameTop = 3
	// frameBottom is the blank line above the footer and the footer itself.
	frameBottom = 2
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (value model) View() tea.View {
	terminalWidth := value.contentWidth()
	inset, width := contentFrame(terminalWidth)
	if value.showHelp {
		return value.helpOverlay(inset, width)
	}
	if value.previewFullscreen {
		return value.fullscreenPreview(inset, width)
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

	lines := []string{value.header(width), value.pillBar(width), ""}
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

// boundedLayout bounds the detail and diagnostics lines to the terminal
// height and returns them alongside the height left for the session list.
// The fixed frame is header + pill bar + blank line above the body
// (frameTop) and blank line + footer below it (frameBottom); movePage
// derives its page step from the same computation so paging matches one
// visible screen.
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
	detailHeight := value.height - (frameTop + 1 + 1 + statusFloor + searchLines + frameBottom)
	if len(details) > detailHeight {
		details = boundedDetailLines(selected, width, detailHeight, value.deps.Now())
	}
	diagnosticHeight := value.height - (frameTop + 1 + len(details) + 1 + searchLines + frameBottom)
	diagnosticHeight = max(diagnosticHeight, statusFloor)
	if len(diagnostics) > diagnosticHeight {
		diagnostics = diagnostics[len(diagnostics)-diagnosticHeight:]
	}
	bodyHeight := max(1, value.height-(frameTop+1+len(details)+len(diagnostics)+searchLines+frameBottom))
	return details, diagnostics, bodyHeight
}

func (value model) sessionLines(width int) ([]string, int) {
	if len(value.rows) == 0 {
		if value.query != "" {
			return []string{fitLine(fmt.Sprintf("  no matches for %q · esc to clear", value.query), width)}, 0
		}
		if value.filterActive() {
			return []string{fitLine("  "+value.emptyFilterMessage(), width)}, 0
		}
		if value.collecting && len(value.result.Sessions) == 0 {
			return []string{"  loading sessions…"}, 0
		}
		if value.staleHidden > 0 {
			message := fmt.Sprintf("  all %d sessions are older than 7d · a to show", value.staleHidden)
			return []string{fitLine(message, width)}, 0
		}
		hint := value.mutedText("  start a claude/codex session, or add a remote with: ars remote add <host>", width)
		return []string{"  no sessions yet", "", hint}, 0
	}
	layout := newRowLayout(value.result.Sessions, width, value.deps.Now(), value.deps.LocalTarget, value.pins)
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

// header assembles the agent-deck style header: a compact status logo, the
// title, a status-count summary replacing the old active/recent tally, the
// existing suffixes (showing all / older hidden / peers / refreshing), and a
// right-aligned version dropped when it would not fit into width. The
// active-filter indicator lives in the pill bar below instead.
func (value model) header(width int) string {
	left := value.headerContent()
	version := value.versionText()
	if version == "" {
		return fitLine(left, width)
	}
	budget := width - lipgloss.Width(version) - 2
	if lipgloss.Width(left) > budget {
		return fitLine(left, width)
	}
	pad := width - lipgloss.Width(left) - lipgloss.Width(version)
	return fitLine(left+strings.Repeat(" ", max(0, pad))+version, width)
}

// headerContent builds the left-hand header content: logo, title, and
// status stats, without the right-aligned version.
func (value model) headerContent() string {
	attached, running, waiting, idle := value.stateCounts()
	peers := 0
	for _, host := range value.result.Hosts {
		if host.Target != value.deps.LocalTarget {
			peers++
		}
	}
	stats := "  " + value.statusCounts(attached, running, waiting, idle)
	if value.showAll {
		stats += " · showing all"
	} else if value.staleHidden > 0 {
		stats += fmt.Sprintf(" · %d older hidden", value.staleHidden)
	}
	switch peers {
	case 0:
	case 1:
		stats += " · 1 peer"
	default:
		stats += fmt.Sprintf(" · %d peers", peers)
	}
	if value.collecting {
		stats += " · " + spinnerFrames[value.spinner%len(spinnerFrames)] + " refreshing"
	}
	live := attached + running + waiting
	return value.statusLogo(live) + " " + value.titleText() + stats
}

// stateCounts tallies sessions by presentation state (attached/running/
// waiting/idle) before any state filter is applied, so the header and pill
// bar can both show what each filter would reveal rather than the currently
// filtered subset. Staleness (showAll / older-hidden) still narrows the
// count, matching what the visible list would contain with filters cleared.
func (value model) stateCounts() (attached, running, waiting, idle int) {
	counted := value.result.Sessions
	if value.query == "" {
		counted, _ = filterByStale(counted, value.deps.Now(), value.showAll, value.pins)
	}
	for _, item := range counted {
		switch {
		case item.Runtime.State == session.RuntimeAttached:
			attached++
		case item.Runtime.State == session.RuntimeRunning && value.activity[keyOf(item)].state == activityWaiting:
			waiting++
		case item.Runtime.State == session.RuntimeRunning:
			running++
		default:
			idle++
		}
	}
	return attached, running, waiting, idle
}

// statusCounts renders the attached/running/waiting/idle tally, symbol
// colored and label muted, omitting zero counts.
func (value model) statusCounts(attached, running, waiting, idle int) string {
	var parts []string
	if attached > 0 {
		parts = append(parts, value.countPart("●", value.styles.attached, attached, "attached"))
	}
	if running > 0 {
		parts = append(parts, value.countPart("◐", value.styles.running, running, "running"))
	}
	if waiting > 0 {
		parts = append(parts, value.countPart("?", value.styles.failure, waiting, "waiting"))
	}
	if idle > 0 {
		parts = append(parts, value.countPart("○", value.styles.saved, idle, "idle"))
	}
	return strings.Join(parts, " · ")
}

func (value model) countPart(symbol string, symbolStyle lipgloss.Style, count int, label string) string {
	text := fmt.Sprintf("%d %s", count, label)
	if value.noColor {
		return symbol + " " + text
	}
	return symbolStyle.Render(symbol) + " " + value.styles.muted.Render(text)
}

// pillBar renders the always-present filter pill row beneath the header: an
// "All" pill plus one pill per runtime state, each showing the count that
// state would have with filters cleared (state filters change what's
// visible, not what's counted, so toggling a pill doesn't move the numbers
// on the others). Pills stay in the line even at a zero count so toggling
// filters never shifts the layout.
func (value model) pillBar(width int) string {
	attached, running, waiting, idle := value.stateCounts()
	pills := []string{
		value.renderPill("All", value.styles.muted, 0, !value.filterActive(), true),
		value.renderPill("●", value.styles.attached, attached, value.stateFilter[session.RuntimeAttached], false),
		value.renderPill("◐", value.styles.running, running, value.stateFilter[session.RuntimeRunning], false),
		value.renderPill("○", value.styles.saved, idle, value.stateFilter[session.RuntimeSaved], false),
		value.renderPill("?", value.styles.failure, waiting, value.waitingFilter, false),
	}
	return fitLine(strings.Join(pills, "   "), width)
}

// renderPill renders a single pill. isAll pills have no count (always
// "All"); the rest read "symbol N". Active pills get the state's foreground
// bolded and reversed to read as a filled chip; inactive pills keep just the
// foreground. A zero count still renders, faint, so the pill never
// disappears and jumps the layout. Under noColor, only active pills gain a
// bracket since there's no other way to mark them.
func (value model) renderPill(symbol string, style lipgloss.Style, count int, active, isAll bool) string {
	text := symbol
	if !isAll {
		text = fmt.Sprintf("%s %d", symbol, count)
	}
	if value.noColor {
		if active {
			return "[" + text + "]"
		}
		return text
	}
	if !isAll && count == 0 {
		return value.styles.muted.Render(text)
	}
	if active {
		return style.Bold(true).Reverse(true).Render(text)
	}
	return style.Render(text)
}

// statusLogo renders the fixed 3-cell "⟨●●○⟩" summary, filling from the left
// with live-session dots and the rest with saved dots.
func (value model) statusLogo(live int) string {
	const cells = 3
	filled := min(live, cells)
	dots := strings.Repeat("●", filled) + strings.Repeat("○", cells-filled)
	if value.noColor {
		return "⟨" + dots + "⟩"
	}
	rendered := value.styles.attached.Render(strings.Repeat("●", filled)) + value.styles.saved.Render(strings.Repeat("○", cells-filled))
	return value.styles.muted.Render("⟨") + rendered + value.styles.muted.Render("⟩")
}

func (value model) titleText() string {
	if value.noColor {
		return "ars"
	}
	return value.styles.title.Render("ars")
}

func (value model) versionText() string {
	if value.deps.Version == "" {
		return ""
	}
	text := value.deps.Version
	if !strings.HasPrefix(text, "v") {
		text = "v" + text
	}
	if value.noColor {
		return text
	}
	return value.styles.muted.Render(text)
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
	message := value.Message + diagnosticHint(value.Code)
	if value.Host == localTarget {
		return message
	}
	return value.Host + ": " + message
}

// diagnosticHint turns a provider discovery code into a hint the reader can act
// on. Codes without a hint keep the raw suffix so unexpected ones stay visible.
func diagnosticHint(code string) string {
	switch code {
	case "":
		return ""
	case "resource_limit":
		return " · session limit reached, oldest hidden"
	case "incompatible":
		return " · unrecognized session data skipped"
	case "corrupt":
		return " · unreadable session data skipped"
	case "unavailable":
		return " · some session files could not be read"
	default:
		return fmt.Sprintf(" (%s)", code)
	}
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
		items = append(items, "h/l fold", "g/G top/end", "1-9 group", "!@#$ filter", "a older", "x kill", "m msg", "P pin")
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
	if value.previewVisible() {
		items = append(items, "f full")
	}
	items = append(items, action, "r refresh", "q quit", "? help")
	return joinFooterItems(items, separator, width)
}

// joinFooterItems joins footer hints with separator, dropping the lowest
// priority droppable hints (in this order) until the line fits width.
// Higher priority items (navigation, search, quit, help, etc.) are never
// dropped, so on very narrow terminals the line may still overflow.
func joinFooterItems(items []string, separator string, width int) string {
	droppable := []string{"g/G top/end", "P pin", "m msg", "x kill", "!@#$ filter", "a older", "1-9 group", "h/l fold", "f full"}
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
