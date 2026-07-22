package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

func TestWideRowsAlignRuntimeAndAgeColumns(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, true
	short := twoSessions()[0]
	long := short
	long.NativeID = "223e4567-e89b-42d3-a456-426614174000"
	long.Title = "아주 긴 session title"
	long.Host = "개발-server"
	value.result.Sessions = []session.Session{short, long}
	value.refreshVisible()

	rows := sessionRows(value.View().Content)
	if len(rows) != 2 {
		t.Fatalf("session rows = %q", rows)
	}
	for _, column := range []string{"attached", "1d"} {
		if first, second := renderedColumn(rows[0], column), renderedColumn(rows[1], column); first != second {
			t.Fatalf("%s columns = (%d, %d), rows = %q", column, first, second, rows)
		}
	}
}

func TestSelectedRowFillsUsableWidthInsideInset(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, false
	line := styledSelectedRow(value.View().Content)
	inset, usable := contentFrame(value.width)
	padding := rowPadding(usable)
	plain := ansi.Strip(line)
	if !strings.HasPrefix(plain, strings.Repeat(" ", inset+padding)+">") {
		t.Fatalf("selected row missing inset: %q", plain)
	}
	if !strings.HasSuffix(plain, strings.Repeat(" ", padding)) {
		t.Fatalf("selected row missing trailing padding: %q", plain)
	}
	if ansi.StringWidth(line)-inset != usable {
		t.Fatalf("selected width = %d, want usable %d + inset %d",
			ansi.StringWidth(line), usable, inset)
	}
}

func TestRowsUseTwoCellColumnGutter(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, true
	row := activeRow(value.View().Content)
	if !strings.Contains(row, "attached(1)  1d") {
		t.Fatalf("runtime/activity gutter is not two cells: %q", row)
	}
}

func TestVeryNarrowFrameDropsInset(t *testing.T) {
	if inset, usable := contentFrame(39); inset != 0 || usable != 39 {
		t.Fatalf("contentFrame(39) = (%d, %d)", inset, usable)
	}
	if inset, usable := contentFrame(40); inset != 1 || usable != 38 {
		t.Fatalf("contentFrame(40) = (%d, %d)", inset, usable)
	}
}

func TestVeryNarrowViewsStayWithinTerminalWidth(t *testing.T) {
	for _, width := range []int{1, 2, 3, 4, 10, 39} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			value := readyModel()
			value.width, value.height, value.noColor = width, 24, false
			for _, line := range strings.Split(value.View().Content, "\n") {
				if got := ansi.StringWidth(line); got > width {
					t.Fatalf("line width = %d, want <= %d: %q", got, width, line)
				}
			}
		})
	}
}

func sessionRows(content string) []string {
	var rows []string
	for _, line := range strings.Split(ansi.Strip(content), "\n") {
		if strings.Contains(line, "attached") {
			rows = append(rows, line)
		}
	}
	return rows
}

func styledSelectedRow(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimLeft(ansi.Strip(line), " "), "> ") {
			return line
		}
	}
	return ""
}

func renderedColumn(line, value string) int {
	index := strings.Index(line, value)
	if index < 0 {
		return -1
	}
	return ansi.StringWidth(line[:index])
}
