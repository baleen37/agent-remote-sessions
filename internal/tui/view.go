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
	projectColumnWidth  = 90
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
		search = append(search, fitLine(prefix+value.query, width))
	}

	if value.height > 0 {
		detailHeight := value.height - (2 + 1 + 1 + len(search) + 2)
		if len(details) > detailHeight {
			details = boundedDetailLines(selected, width, detailHeight)
		}
		fixedHeight := 2 + 1 + len(details) + len(search) + 2
		bodyHeight := value.height - fixedHeight
		if bodyHeight < 1 {
			bodyHeight = 1
		}
		body = visibleLines(body, selectedLine, bodyHeight)
		diagnosticHeight := value.height - (fixedHeight + len(body))
		if diagnosticHeight < len(diagnostics) {
			if diagnosticHeight < 0 {
				diagnosticHeight = 0
			}
			diagnostics = diagnostics[:diagnosticHeight]
		}
	}

	for index, line := range details {
		details[index] = value.mutedText(line, width)
	}
	lines := []string{fitLine(value.header(), width), ""}
	lines = append(lines, body...)
	lines = append(lines, "")
	lines = append(lines, details...)
	lines = append(lines, diagnostics...)
	lines = append(lines, search...)
	lines = append(lines, "", value.mutedText(help(width), width))
	if inset > 0 {
		margin := strings.Repeat(" ", inset)
		for index, line := range lines {
			if line != "" {
				lines[index] = margin + line
			}
		}
	}
	return tea.View{Content: strings.Join(lines, "\n"), AltScreen: true}
}

func (value model) sessionLines(width int) ([]string, int) {
	active, recent := splitSessions(value.visible)
	layout := newRowLayout(value.visible, width, value.deps.Now(), value.deps.LocalTarget)
	lines := []string{value.stateText("Active", session.RuntimeAttached)}
	lines = append(lines, value.renderGroup(active, layout)...)
	lines = append(lines, "", value.stateText("Recent", session.RuntimeSaved))
	lines = append(lines, value.renderGroup(recent, layout)...)

	selectedLine := 0
	for index, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(ansi.Strip(line), " "), "> ") {
			selectedLine = index
			break
		}
	}
	return lines, selectedLine
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
	hosts := 0
	for _, host := range value.result.Hosts {
		if host.Target != value.deps.LocalTarget {
			hosts++
		}
	}
	stats := fmt.Sprintf("%d active · %d recent · %d hosts", active, len(value.result.Sessions)-active, hosts)
	if value.collecting {
		stats += " · refreshing"
	}
	if value.noColor {
		return "ars  " + stats
	}
	return value.styles.title.Render("ars") + "  " + value.styles.muted.Render(stats)
}

func splitSessions(items []session.Session) (active, recent []session.Session) {
	for _, item := range items {
		if item.Runtime.State == session.RuntimeSaved {
			recent = append(recent, item)
		} else {
			active = append(active, item)
		}
	}
	return active, recent
}

func (value model) renderGroup(items []session.Session, layout rowLayout) []string {
	if len(items) == 0 {
		return []string{"  none"}
	}
	rows := make([]string, 0, len(items))
	for _, item := range items {
		rows = append(rows, value.renderRow(item, layout))
	}
	return rows
}

func sessionTitle(item session.Session) string {
	if item.Title != "" {
		return item.Title
	}
	return session.Project(item.CWD) + " · " + item.NativeID[:8]
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
	if len(value.visible) == 0 || value.selected < 0 || value.selected >= len(value.visible) {
		return session.Session{}, false
	}
	return value.visible[value.selected], true
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

func help(width int) string {
	if width < 75 {
		return "↑↓/jk move  / search  enter attach  r refresh  q quit"
	}
	return "↑↓/jk move   / search   enter attach   r refresh   q quit"
}
