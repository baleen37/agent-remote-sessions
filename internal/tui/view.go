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
	width := value.contentWidth()
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

	lines := []string{fitLine(value.header(), width), ""}
	lines = append(lines, body...)
	lines = append(lines, "")
	lines = append(lines, details...)
	lines = append(lines, diagnostics...)
	lines = append(lines, search...)
	lines = append(lines, "", fitLine(help(width), width))
	return tea.View{Content: strings.Join(lines, "\n"), AltScreen: true}
}

func (value model) sessionLines(width int) ([]string, int) {
	active, recent := splitSessions(value.visible)
	lines := []string{value.stateText("Active", session.RuntimeAttached)}
	lines = append(lines, value.renderGroup(active, width)...)
	lines = append(lines, "", value.stateText("Recent", session.RuntimeSaved))
	lines = append(lines, value.renderGroup(recent, width)...)

	selectedLine := 0
	for index, line := range lines {
		if strings.HasPrefix(ansi.Strip(line), "> ") {
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
	header := fmt.Sprintf("ars  %d active · %d recent · %d hosts", active, len(value.result.Sessions)-active, hosts)
	if value.collecting {
		header += " · refreshing"
	}
	return header
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

func (value model) renderGroup(items []session.Session, width int) []string {
	if len(items) == 0 {
		return []string{"  none"}
	}
	rows := make([]string, 0, len(items))
	for _, item := range items {
		rows = append(rows, value.renderRow(item, width))
	}
	return rows
}

func (value model) renderRow(item session.Session, width int) string {
	selected := keyOf(item) == value.selectedKey
	prefix := "  "
	if selected {
		prefix = "> "
	}

	state := "∙"
	if item.Runtime.State != session.RuntimeSaved {
		state = "✻"
	}
	runtime := string(item.Runtime.State)
	if item.Runtime.State == session.RuntimeAttached && width >= clientColumnWidth {
		runtime = fmt.Sprintf("attached(%d)", item.Runtime.AttachedClients)
	}
	fields := []string{state, sessionTitle(item)}
	flexible := []int{1}
	if width >= providerColumnWidth {
		fields = append(fields, string(item.Provider))
	}
	fields = append(fields, location(item, value.deps.LocalTarget))
	flexible = append(flexible, len(fields)-1)
	if width >= projectColumnWidth {
		fields = append(fields, session.Project(item.CWD))
		flexible = append(flexible, len(fields)-1)
	}
	fields = append(fields, runtime, activityAge(value.deps.Now(), item.UpdatedAt))
	runtimeIndex := len(fields) - 2
	_, isStale := value.stale[item.Host]
	if isStale {
		fields = append(fields, "cached")
	}

	truncateFlexibleFields(fields, flexible, width-lipgloss.Width(prefix))
	fields[0] = value.stateText(fields[0], item.Runtime.State)
	fields[runtimeIndex] = value.stateText(fields[runtimeIndex], item.Runtime.State)
	if isStale {
		fields[len(fields)-1] = value.stateText(fields[len(fields)-1], session.RuntimeSaved)
	}
	row := prefix + strings.Join(fields, "  ")
	row = fitLine(row, width)
	if selected && !value.noColor {
		row = lipgloss.NewStyle().Background(lipgloss.Color("6")).Render(row)
	}
	return row
}

func truncateFlexibleFields(fields []string, flexible []int, width int) {
	fixedWidth := 2 * (len(fields) - 1)
	isFlexible := make([]bool, len(fields))
	for _, index := range flexible {
		isFlexible[index] = true
	}
	for index, field := range fields {
		if !isFlexible[index] {
			fixedWidth += lipgloss.Width(field)
		}
	}
	remaining := width - fixedWidth
	if remaining < 0 {
		remaining = 0
	}
	pending := append([]int(nil), flexible...)
	for len(pending) > 0 {
		share := remaining / len(pending)
		foundShortField := false
		for pendingIndex, fieldIndex := range pending {
			fieldWidth := lipgloss.Width(fields[fieldIndex])
			if fieldWidth > share {
				continue
			}
			remaining -= fieldWidth
			pending = append(pending[:pendingIndex], pending[pendingIndex+1:]...)
			foundShortField = true
			break
		}
		if foundShortField {
			continue
		}
		for pendingIndex, fieldIndex := range pending {
			fieldWidth := remaining / (len(pending) - pendingIndex)
			fields[fieldIndex] = ansi.Truncate(fields[fieldIndex], fieldWidth, "…")
			remaining -= fieldWidth
		}
		return
	}
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
		lines = append(lines, fitLine(diagnosticLine(diagnostic, value.deps.LocalTarget), width))
	}
	if value.status != "" {
		status := fitLine(value.status, width)
		if strings.HasPrefix(value.status, "attach failed:") {
			status = value.errorText(status, width)
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
	style := lipgloss.NewStyle()
	switch state {
	case session.RuntimeAttached:
		style = style.Foreground(lipgloss.Color("2"))
	case session.RuntimeRunning:
		style = style.Foreground(lipgloss.Color("3"))
	default:
		style = style.Faint(true)
	}
	return style.Render(text)
}

func (value model) errorText(text string, width int) string {
	text = fitLine(text, width)
	if value.noColor {
		return text
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render(text)
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
