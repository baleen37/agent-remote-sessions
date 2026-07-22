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

func TestSelectedRowKeepsBackgroundAcrossNestedANSIStyles(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, false
	_, usable := contentFrame(value.width)
	layout := newRowLayout(value.visible, usable, value.deps.Now(), value.deps.LocalTarget)
	line := value.renderRow(value.visible[0], layout)

	if missing := cellsWithoutBackground(line); len(missing) > 0 {
		t.Fatalf("selected background missing from cells %v: %q", missing, line)
	}
	if cursorStyle := value.styles.selectedCursor.Render("> "); !strings.Contains(line, cursorStyle[:strings.Index(cursorStyle, "> ")]) {
		t.Fatalf("selected cursor foreground missing: %q", line)
	}
	if stateStyle := value.stateText("attached(1)", session.RuntimeAttached); !strings.Contains(line, stateStyle[:strings.Index(stateStyle, "attached(1)")]) {
		t.Fatalf("runtime foreground missing: %q", line)
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
	for width := 1; width < 40; width++ {
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

func cellsWithoutBackground(line string) []int {
	background := false
	cell := 0
	var missing []int
	parser := ansi.NewParser()
	parser.SetHandler(ansi.Handler{
		Print: func(character rune) {
			width := ansi.StringWidth(string(character))
			if !background {
				for offset := range width {
					missing = append(missing, cell+offset)
				}
			}
			cell += width
		},
		HandleCsi: func(command ansi.Cmd, params ansi.Params) {
			if command.Final() != 'm' {
				return
			}
			if len(params) == 0 {
				background = false
				return
			}
			params.ForEach(0, func(_ int, parameter int, _ bool) {
				switch {
				case parameter == 0 || parameter == 49:
					background = false
				case parameter >= 40 && parameter <= 48:
					background = true
				case parameter >= 100 && parameter <= 107:
					background = true
				}
			})
		},
	})
	parser.Parse([]byte(line))
	return missing
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
