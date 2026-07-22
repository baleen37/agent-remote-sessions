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
)

func (value model) View() tea.View {
	terminalWidth := value.contentWidth()
	inset, width := contentFrame(terminalWidth)
	body, selectedLine := value.sessionLines(width)
	var details []string
	selected, hasSelection := value.selectedSession()
	if hasSelection {
		details = detailLines(selected, width)
	}
	diagnostics := value.diagnostics(width)
	var search []string
	if value.query != "" || value.searching {
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

	if value.height > 0 {
		var bodyHeight int
		details, bodyHeight = value.boundedLayout(details, selected, len(search), width)
		fixedHeight := 2 + 1 + len(details) + len(search) + 2
		body = visibleLines(body, selectedLine, bodyHeight)
		diagnosticHeight := value.height - (fixedHeight + len(body))
		if diagnosticHeight < len(diagnostics) {
			if diagnosticHeight < 0 {
				diagnosticHeight = 0
			}
			diagnostics = diagnostics[:diagnosticHeight]
		}
	}
	for index, detail := range details {
		details[index] = value.mutedText(detail, width)
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

// boundedLayout bounds the detail lines to the terminal height and returns
// them with the height left for the session list. movePage derives its page
// step from the same computation so paging matches one visible screen.
func (value model) boundedLayout(details []string, selected session.Session, searchLines, width int) ([]string, int) {
	if value.height <= 0 {
		return details, len(value.rows)
	}
	detailHeight := value.height - (2 + 1 + 1 + searchLines + 2)
	if len(details) > detailHeight {
		details = boundedDetailLines(selected, width, detailHeight)
	}
	return details, max(1, value.height-(2+1+len(details)+searchLines+2))
}

func (value model) sessionLines(width int) ([]string, int) {
	if len(value.rows) == 0 {
		if value.query != "" {
			return []string{fitLine(fmt.Sprintf("  no matches for %q · esc to clear", value.query), width)}, 0
		}
		return []string{"  no sessions"}, 0
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
		text += " " + value.stateText("✻", row.state)
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

func visibleLines(lines []string, selected, height int) []string {
	if height >= len(lines) {
		return lines
	}
	start := selected - height + 1
	if start < 0 {
		start = 0
	}
	if start+height > len(lines) {
		start = len(lines) - height
	}
	return lines[start : start+height]
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
		stats += " · refreshing"
	}
	if value.noColor {
		return "ars" + stats
	}
	return value.styles.title.Render("ars") + value.styles.muted.Render(stats)
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

func detailLines(item session.Session, width int) []string {
	fields := []string{item.CWD, item.NativeID, item.UpdatedAt.Format(time.RFC3339Nano)}
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

func boundedDetailLines(item session.Session, width, height int) []string {
	if height <= 0 {
		return nil
	}
	fields := []string{item.CWD, item.NativeID, item.UpdatedAt.Format(time.RFC3339Nano)}
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
		lines = append(lines, value.errorText(diagnosticLine(diagnostic, value.deps.LocalTarget), width))
	}
	for _, diagnostic := range value.result.Warnings {
		lines = append(lines, value.mutedText(diagnosticLine(diagnostic, value.deps.LocalTarget), width))
	}
	if value.status != "" {
		status := value.mutedText(value.status, width)
		if strings.HasPrefix(value.status, "attach failed:") {
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
		items = append(items, "g/G top/end")
	}
	items = append(items, "/ search")
	if value.query != "" {
		items = append(items, "esc clear")
	}
	items = append(items, action, "r refresh", "q quit")
	return strings.Join(items, separator)
}
