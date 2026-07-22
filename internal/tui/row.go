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
	columnGutter   = "  "
	rowPrefixSize  = 2
	treeGuideWidth = 3
	minInsetWidth  = 40
)

type rowLayout struct {
	width                     int
	showProvider, showClients bool
	title, provider, location int
	runtime, activity         int
}

func contentFrame(width int) (int, int) {
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
		showClients:  width >= clientColumnWidth,
	}
	for _, item := range items {
		layout.title = max(layout.title, lipgloss.Width(sessionTitle(item)))
		layout.location = max(layout.location, lipgloss.Width(location(item, localTarget)))
		layout.runtime = max(layout.runtime, lipgloss.Width(runtimeLabel(item, layout.showClients)))
		layout.activity = max(layout.activity, lipgloss.Width(activityAge(now, item.UpdatedAt)))
		if layout.showProvider {
			layout.provider = max(layout.provider, lipgloss.Width(string(item.Provider)))
		}
	}

	fieldCount := 5 // marker, title, location, runtime, activity
	fixed := 2*rowPadding(width) + rowPrefixSize + treeGuideWidth + 1 + layout.runtime + layout.activity
	if layout.showProvider {
		fieldCount++
		fixed += layout.provider
	}
	fixed += (fieldCount - 1) * lipgloss.Width(columnGutter)

	flexible := []int{layout.title, layout.location}
	allocated := allocateWidths(flexible, max(0, width-fixed))
	layout.title, layout.location = allocated[0], allocated[1]
	return layout
}

func runtimeLabel(item session.Session, clients bool) string {
	if clients && item.Runtime.State == session.RuntimeAttached {
		return fmt.Sprintf("attached(%d)", item.Runtime.AttachedClients)
	}
	return string(item.Runtime.State)
}

func (value model) renderRow(row listRow, selected bool, layout rowLayout) string {
	item := row.session
	cursor := "  "
	if selected {
		cursor = "> "
		if !value.noColor {
			cursor = value.styles.selectedCursor.Render(cursor)
		}
	}
	guide := "├─ "
	if row.last {
		guide = "└─ "
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
	fields = append(fields,
		column(value.stateText(runtimeLabel(item, layout.showClients), item.Runtime.State), layout.runtime, true),
		column(activityAge(value.deps.Now(), item.UpdatedAt), layout.activity, true),
	)
	if _, isStale := value.stale[item.Host]; isStale {
		fields = append(fields, value.stateText("cached", session.RuntimeSaved))
	}

	padding := rowPadding(layout.width)
	innerWidth := layout.width - 2*padding
	line := fitLine(cursor+guide+strings.Join(fields, columnGutter), innerWidth)
	line = strings.Repeat(" ", padding) + line
	line += strings.Repeat(" ", max(0, layout.width-padding-lipgloss.Width(line)))
	line += strings.Repeat(" ", padding)
	if selected && !value.noColor {
		line = value.styles.selected.Render(line)
	}
	return line
}
