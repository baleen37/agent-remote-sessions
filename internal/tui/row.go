package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

const (
	columnGutter  = "  "
	rowPrefixSize = 2
	minInsetWidth = 40
)

type rowLayout struct {
	width                      int
	showProvider, showProject  bool
	showClients                bool
	title, provider, location  int
	project, runtime, activity int
	now                        time.Time
}

func contentFrame(width int) (inset int, usable int) {
	if width >= minInsetWidth {
		return 1, width - 2
	}
	return 0, width
}

func rowPadding(width int) int {
	if width >= 4 {
		return 1
	}
	return 0
}

func allocateWidths(natural []int, budget int) []int {
	widths := make([]int, len(natural))
	pending := make([]int, len(natural))
	for index := range natural {
		pending[index] = index
	}
	for len(pending) > 0 {
		share := budget / len(pending)
		placed := false
		for pendingIndex, fieldIndex := range pending {
			if natural[fieldIndex] > share {
				continue
			}
			widths[fieldIndex] = natural[fieldIndex]
			budget -= natural[fieldIndex]
			pending = append(pending[:pendingIndex], pending[pendingIndex+1:]...)
			placed = true
			break
		}
		if placed {
			continue
		}
		for pendingIndex, fieldIndex := range pending {
			widths[fieldIndex] = budget / (len(pending) - pendingIndex)
			budget -= widths[fieldIndex]
		}
		break
	}
	return widths
}

func column(value string, width int, right bool) string {
	value = ansi.Truncate(value, width, "…")
	padding := strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
	if right {
		return padding + value
	}
	return value + padding
}

func newRowLayout(items []session.Session, width int, now time.Time, localTarget string) rowLayout {
	layout := rowLayout{
		width:        width,
		showProvider: width >= providerColumnWidth,
		showProject:  width >= projectColumnWidth,
		showClients:  width >= clientColumnWidth,
		now:          now,
	}
	for _, item := range items {
		layout.title = max(layout.title, lipgloss.Width(sessionTitle(item)))
		layout.location = max(layout.location, lipgloss.Width(location(item, localTarget)))
		layout.runtime = max(layout.runtime, lipgloss.Width(runtimeLabel(item, layout.showClients)))
		layout.activity = max(layout.activity, lipgloss.Width(activityAge(now, item.UpdatedAt)))
		if layout.showProvider {
			layout.provider = max(layout.provider, lipgloss.Width(string(item.Provider)))
		}
		if layout.showProject {
			layout.project = max(layout.project, lipgloss.Width(session.Project(item.CWD)))
		}
	}

	fieldCount := 5 // marker, title, location, runtime, activity
	fixed := 2*rowPadding(width) + rowPrefixSize + 1 + layout.runtime + layout.activity
	if layout.showProvider {
		fieldCount++
		fixed += layout.provider
	}
	if layout.showProject {
		fieldCount++
	}
	fixed += (fieldCount - 1) * lipgloss.Width(columnGutter)

	flexible := []int{layout.title, layout.location}
	if layout.showProject {
		flexible = append(flexible, layout.project)
	}
	allocated := allocateWidths(flexible, max(0, width-fixed))
	layout.title, layout.location = allocated[0], allocated[1]
	if layout.showProject {
		layout.project = allocated[2]
	}
	return layout
}

func runtimeLabel(item session.Session, clients bool) string {
	if clients && item.Runtime.State == session.RuntimeAttached {
		return fmt.Sprintf("attached(%d)", item.Runtime.AttachedClients)
	}
	return string(item.Runtime.State)
}

func (value model) renderRow(item session.Session, layout rowLayout) string {
	selected := keyOf(item) == value.selectedKey
	cursor := "  "
	if selected {
		cursor = "> "
		if !value.noColor {
			cursor = value.styles.selectedCursor.Render(cursor)
		}
	}
	marker := "∙"
	if item.Runtime.State != session.RuntimeSaved {
		marker = "✻"
	}
	fields := []string{
		value.stateText(marker, item.Runtime.State),
		column(sessionTitle(item), layout.title, false),
	}
	if layout.showProvider {
		fields = append(fields, column(string(item.Provider), layout.provider, false))
	}
	fields = append(fields, column(location(item, value.deps.LocalTarget), layout.location, false))
	if layout.showProject {
		fields = append(fields, column(session.Project(item.CWD), layout.project, false))
	}
	fields = append(fields,
		column(value.stateText(runtimeLabel(item, layout.showClients), item.Runtime.State), layout.runtime, true),
		column(activityAge(layout.now, item.UpdatedAt), layout.activity, true),
	)

	padding := rowPadding(layout.width)
	innerWidth := layout.width - 2*padding
	row := fitLine(cursor+strings.Join(fields, columnGutter), innerWidth)
	row = strings.Repeat(" ", padding) + row
	row += strings.Repeat(" ", max(0, layout.width-padding-lipgloss.Width(row)))
	row += strings.Repeat(" ", padding)
	if selected && !value.noColor {
		row = value.styles.selected.Render(row)
	}
	return row
}
