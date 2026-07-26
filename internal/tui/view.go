package tui

import (
	"fmt"
	"strconv"
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
	layout := previewLayoutOf(width)
	previewShown := value.previewVisible()
	listWidth := width
	previewCols := 0
	if previewShown && layout == previewDual {
		listWidth, previewCols = value.splitWidths(width)
	}
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
	// The truly-empty-inventory hint (emptyStateLines, reached via
	// sessionLines below) has to pick its tier before boundedLayout runs,
	// so it needs to know upfront how many lines diagnostics/search will
	// actually keep — not just how many they start with. hasSelection is
	// always false for an empty inventory (nothing to select), so details
	// plays no part in this reservation; reservedDiagnosticLines mirrors
	// boundedLayout's own diagnosticHeight formula with details fixed at 0.
	reservedDiagnosticLines := len(diagnostics)
	if value.height > 0 {
		statusFloor := 0
		if value.status != "" {
			statusFloor = 1
		}
		budget := value.height - (frameTop + 1 + 1 + len(search) + frameBottom)
		budget = max(budget, statusFloor)
		reservedDiagnosticLines = min(reservedDiagnosticLines, budget)
	}
	body, selectedLine := value.sessionLines(listWidth, reservedDiagnosticLines+len(search))

	if value.height > 0 {
		var bodyHeight int
		details, diagnostics, bodyHeight = value.boundedLayout(details, selected, diagnostics, len(search), width)
		switch {
		case previewShown && layout == previewStacked:
			body = value.assembleStackedBody(body, selectedLine, listWidth, bodyHeight)
		case previewShown && layout == previewDual:
			body = value.assembleDualBody(body, selectedLine, listWidth, previewCols, bodyHeight)
		default:
			body = value.scrolledBody(body, selectedLine, bodyHeight, listWidth)
		}
	}
	for index, detail := range details {
		details[index] = value.mutedText(detail, width)
	}

	lines := []string{value.header(width), value.pillBar(width), ""}
	lines = append(lines, body...)
	lines = append(lines, "")
	lines = append(lines, details...)
	lines = append(lines, diagnostics...)
	lines = append(lines, search...)
	lines = append(lines, "", fitLine(value.help(width), width))
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

// assembleDualBody renders the side-by-side (dual) layout: the list windowed
// to its column (bodyHeight minus the "SESSIONS" title when there's room),
// the title prepended afterward per the windowing-then-title ordering task 4
// established, then joined with the preview panel via joinPreview.
func (value model) assembleDualBody(body []string, selectedLine, listWidth, previewCols, bodyHeight int) []string {
	listHeight := bodyHeight
	if bodyHeight > panelTitleHeight {
		listHeight = bodyHeight - panelTitleHeight
	}
	body = value.scrolledBody(body, selectedLine, listHeight, listWidth)
	if bodyHeight > panelTitleHeight {
		body = append(value.splitPanelTitle("SESSIONS", 100-value.previewPct, listWidth), body...)
	}
	return value.joinPreview(body, listWidth, previewCols, bodyHeight)
}

// assembleStackedBody renders the narrow list-above-preview (stacked)
// layout: stackedHeights partitions bodyHeight between the two panels using
// previewPct as a vertical ratio; when the partition doesn't fit (ok false),
// the preview is silently demoted and the list alone fills bodyHeight,
// exactly like the hidden layout, with no error message. Each panel's title
// is prepended only after that panel's own content is windowed/rendered, so
// scroll-indicator arithmetic never mistakes a title row for a session row.
func (value model) assembleStackedBody(body []string, selectedLine, width, bodyHeight int) []string {
	listRows, previewRows, ok := value.stackedHeights(bodyHeight)
	if !ok {
		return value.scrolledBody(body, selectedLine, bodyHeight, width)
	}
	list := value.scrolledBody(body, selectedLine, listRows, width)
	list = append(value.splitPanelTitle("SESSIONS", 100-value.previewPct, width), list...)
	preview := value.previewPanel(width, previewRows+panelTitleHeight)
	return append(list, preview...)
}

func (value model) sessionLines(width, reservedLines int) ([]string, int) {
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
		return value.emptyStateLines(width, value.height, reservedLines), 0
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

// emptyStateFullHeight is the truly-empty body's line count in the full
// tier (3-line logo + blank + title + blank + 2-line hint). The full tier
// only applies when the fixed frame (frameTop + blank + frameBottom, see
// boundedLayout) plus this body fits without scrolledBody clipping it:
// frameTop(3) + emptyStateFullHeight(8) + frameBottom(2) + 1 (blank line
// between body and footer) = 14.
const emptyStateFullHeight = 8

// emptyStateFullMinHeight is the smallest height that fits the full tier's
// body without scrolledBody truncating it into a "↓ N more" indicator.
const emptyStateFullMinHeight = frameTop + emptyStateFullHeight + frameBottom + 1

// emptyStateFullMinWidth is the narrowest width the full tier's box logo
// and copy are designed for.
const emptyStateFullMinWidth = 60

// emptyStateCompactMinHeight is the smallest height that keeps the compact
// (pre-existing 3-line) empty state instead of collapsing to one line.
const emptyStateCompactMinHeight = 8

// emptyStateLines renders the truly-empty-inventory hint, scaled to the
// available screen size in three tiers: full (box logo + spaced-out copy),
// compact (the original 3-line hint), and minimal (a single line) for very
// short screens. height <= 0 means the caller isn't bounding the view (see
// View's "if value.height > 0" guard), so it keeps the compact form rather
// than guessing a tier.
//
// reservedLines is however many lines diagnostics and the search/compose
// line will actually keep once boundedLayout runs (computed the same way
// boundedLayout computes its own diagnosticHeight, since a truly-empty
// inventory always has zero details). Diagnostics are independent of the
// session list — a host can fail to connect while reporting zero sessions
// — so a tall-enough screen can still be too crowded for the full tier;
// without this, scrolledBody would clip the box logo into a "N more"
// indicator, exactly what the tiering is meant to avoid.
func (value model) emptyStateLines(width, height, reservedLines int) []string {
	available := height - reservedLines
	if height > 0 && available < emptyStateCompactMinHeight {
		return []string{fitLine("  no sessions yet · ars remote add <host>", width)}
	}
	if height <= 0 || available < emptyStateFullMinHeight || width < emptyStateFullMinWidth {
		hint := value.mutedText("  start a claude/codex session, or add a remote with: ars remote add <host>", width)
		return []string{"  no sessions yet", "", hint}
	}
	title := "  no sessions yet"
	if !value.noColor {
		title = "  " + value.styles.title.Render("no sessions yet")
	}
	return []string{
		"  " + value.emptyStateLogoLine("┌──┬──┬──┐"),
		"  " + value.emptyStateLogoLine("│● │◐ │○ │"),
		"  " + value.emptyStateLogoLine("└──┴──┴──┘"),
		"",
		title,
		"",
		value.mutedText("  start a claude/codex session, or", width),
		value.mutedText("  add a remote with: ars remote add <host>", width),
	}
}

// emptyStateLogoLine renders one row of the static status legend box. Under
// color, the glyph cells (●/◐/○) pick up the attached/running/saved
// foregrounds while the box-drawing characters stay muted; the legend is a
// fixed reference to what the symbols mean, not a live tally, so it always
// shows one of each glyph regardless of actual session counts.
func (value model) emptyStateLogoLine(line string) string {
	if value.noColor {
		return line
	}
	var out strings.Builder
	for _, glyph := range line {
		switch glyph {
		case '●':
			out.WriteString(value.styles.attached.Render(string(glyph)))
		case '◐':
			out.WriteString(value.styles.running.Render(string(glyph)))
		case '○':
			out.WriteString(value.styles.saved.Render(string(glyph)))
		default:
			out.WriteString(value.styles.muted.Render(string(glyph)))
		}
	}
	return out.String()
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
		text := value.status
		status := value.mutedText(text, width)
		if isErrorStatus(text) {
			if value.statusRemaining > 0 {
				text += " · " + strconv.Itoa(value.statusRemaining) + "s"
			}
			status = value.errorText(text, width)
		}
		lines = append(lines, status)
	}
	return lines
}

// isErrorStatus reports whether status is one of the failure statuses
// diagnostics() styles as an error, by the same prefixes that arm the
// auto-dismiss countdown. Extracted so both call sites can't drift apart.
func isErrorStatus(status string) bool {
	return strings.HasPrefix(status, "attach failed:") || strings.HasPrefix(status, "kill failed:") || strings.HasPrefix(status, "send failed:")
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

// providerText brands the provider column: claude gets its own coral, codex
// reuses the selectedCursor teal, and any other provider stays muted.
func (value model) providerText(provider session.Provider) string {
	text := string(provider)
	if value.noColor {
		return text
	}
	switch provider {
	case session.Claude:
		return value.styles.providerClaude.Render(text)
	case session.Codex:
		return value.styles.selectedCursor.Render(text)
	default:
		return value.styles.muted.Render(text)
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
		return value.mutedFooterText(strings.Join([]string{"type to filter", "enter apply", "esc cancel"}, separator))
	}
	if value.composing {
		return value.mutedFooterText(strings.Join([]string{"type message", "enter send", "esc cancel"}, separator))
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
	if value.contentWidth() >= stackedMinWidth {
		label := "p preview"
		if !value.previewOn {
			label = "p preview off"
		}
		items = append(items, label)
	}
	if value.previewVisible() {
		items = append(items, "f full", "</> resize")
	}
	items = append(items, action, "r refresh", "q quit", "? help")
	items = fitFooterItems(items, separator, width)
	if value.noColor {
		return strings.Join(items, separator)
	}
	styled := make([]string, len(items))
	for index, item := range items {
		styled[index] = value.styleFooterItem(item)
	}
	return strings.Join(styled, separator)
}

// mutedFooterText renders a footer line that carries no key chips (the
// searching/composing hints) as a self-contained muted string, matching what
// styleFooterItem produces for chip-bearing hints so help() always returns a
// fully styled line and callers never need to wrap it again.
func (value model) mutedFooterText(text string) string {
	if value.noColor {
		return text
	}
	return value.styles.muted.Render(text)
}

// styleFooterItem splits a hint at its first space into a key and a
// description, rendering the key as a background chip and the description
// muted. Hints without a space (shouldn't occur given the fixed hint list)
// are rendered muted in full.
func (value model) styleFooterItem(item string) string {
	key, description, found := strings.Cut(item, " ")
	if !found {
		return value.styles.muted.Render(item)
	}
	return value.styles.keyChip.Render(key) + " " + value.styles.muted.Render(description)
}

// fitFooterItems drops the lowest priority droppable hints (in this order)
// until the joined line fits width, returning the surviving items. Higher
// priority items (navigation, search, quit, help, etc.) are never dropped,
// so on very narrow terminals the line may still overflow. Drop decisions
// are made against the plain (unstyled) join so styling never changes which
// items survive.
func fitFooterItems(items []string, separator string, width int) []string {
	droppable := []string{"g/G top/end", "P pin", "m msg", "x kill", "!@#$ filter", "a older", "1-9 group", "h/l fold", "</> resize", "f full"}
	line := strings.Join(items, separator)
	for _, drop := range droppable {
		if lipgloss.Width(line) <= width {
			break
		}
		items = removeItem(items, drop)
		line = strings.Join(items, separator)
	}
	return items
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
