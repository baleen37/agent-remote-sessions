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
	active, recent := splitSessions(value.visible)
	lines := []string{
		fitLine(value.header(), width),
		"",
		value.stateText("Active", session.RuntimeAttached),
	}
	lines = append(lines, value.renderGroup(active, width)...)
	lines = append(lines, "", value.stateText("Recent", session.RuntimeSaved))
	lines = append(lines, value.renderGroup(recent, width)...)
	lines = append(lines, "")
	if selected, ok := value.selectedSession(); ok {
		lines = append(lines, detailLines(selected, width)...)
	}
	lines = append(lines, value.diagnostics(width)...)
	if value.query != "" || value.searching {
		prefix := "search: "
		if value.searching {
			prefix = "/"
		}
		lines = append(lines, fitLine(prefix+value.query, width))
	}
	lines = append(lines, "", fitLine(help(width), width))
	return tea.View{Content: strings.Join(lines, "\n"), AltScreen: true}
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
	header := fmt.Sprintf("ars  %d active · %d recent · %d hosts", active, len(value.result.Sessions)-active, len(value.result.Hosts))
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
	if width >= providerColumnWidth {
		fields = append(fields, string(item.Provider))
	}
	fields = append(fields, location(item, value.deps.LocalTarget))
	if width >= projectColumnWidth {
		fields = append(fields, session.Project(item.CWD))
	}
	fields = append(fields, runtime, activityAge(value.deps.Now(), item.UpdatedAt))

	fields[1] = truncateTitle(fields, width-lipgloss.Width(prefix))
	fields[0] = value.stateText(fields[0], item.Runtime.State)
	fields[len(fields)-2] = value.stateText(fields[len(fields)-2], item.Runtime.State)
	row := prefix + strings.Join(fields, "  ")
	row = fitLine(row, width)
	if selected && !value.noColor {
		row = lipgloss.NewStyle().Background(lipgloss.Color("6")).Render(row)
	}
	return row
}

func truncateTitle(fields []string, width int) string {
	fixedWidth := 2 * (len(fields) - 1)
	for index, field := range fields {
		if index != 1 {
			fixedWidth += lipgloss.Width(field)
		}
	}
	available := width - fixedWidth
	if available < 1 {
		available = 1
	}
	return ansi.Truncate(fields[1], available, "…")
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

func (value model) diagnostics(width int) []string {
	lines := make([]string, 0, len(value.result.Errors)+len(value.result.Warnings)+1)
	for _, diagnostic := range value.result.Errors {
		lines = append(lines, value.errorText(diagnosticLine(diagnostic), width))
	}
	for _, diagnostic := range value.result.Warnings {
		lines = append(lines, fitLine(diagnosticLine(diagnostic), width))
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

func diagnosticLine(value output.HostError) string {
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
