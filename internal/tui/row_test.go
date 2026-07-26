package tui

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
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

func TestCJKTitleRowFillsUsableWidthInsideInset(t *testing.T) {
	for _, width := range []int{120, 140} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			value := readyModel()
			value.width, value.height, value.noColor = width, 24, true
			items := twoSessions()
			items[0].Title = "인증 플로우 리팩터링"
			value.result.Sessions = items
			value.refreshVisible()

			inset, usable := contentFrame(value.width)
			for _, line := range strings.Split(value.View().Content, "\n") {
				if !strings.Contains(line, "인증") {
					continue
				}
				if got := ansi.StringWidth(line); got != inset+usable {
					t.Fatalf("row width = %d, want inset %d + usable %d: %q", got, inset, usable, line)
				}
			}
		})
	}
}

func TestCJKProjectHeaderFillsUsableWidthInsideInset(t *testing.T) {
	for _, width := range []int{120, 140} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			value := readyModel()
			value.width, value.height, value.noColor = width, 24, true
			items := twoSessions()
			items[0].CWD = "/work/인증-서비스"
			value.result.Sessions = items
			value.refreshVisible()

			inset, usable := contentFrame(value.width)
			found := false
			for _, line := range strings.Split(value.View().Content, "\n") {
				if !strings.Contains(line, "▾ 인증-서비스") {
					continue
				}
				found = true
				if got := ansi.StringWidth(line); got != inset+usable {
					t.Fatalf("header width = %d, want inset %d + usable %d: %q", got, inset, usable, line)
				}
			}
			if !found {
				t.Fatalf("no header line contains the CJK project name:\n%s", value.View().Content)
			}
		})
	}
}

func TestCJKTitleTruncatesAtCellBoundaryWithoutExceedingContract(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, true
	items := twoSessions()
	items[0].Title = "인증 플로우 리팩터링 및 세션 상태 동기화 작업 진행중"
	value.result.Sessions = items
	value.refreshVisible()

	sawOddTitleWidth := false
	for width := 28; width <= 40; width++ {
		layout := newRowLayout(items, width, value.deps.Now(), value.deps.LocalTarget, value.pins)
		if layout.title%2 == 1 {
			sawOddTitleWidth = true
		}
		line := value.renderRow(listRow{kind: rowSession, session: items[0], last: true}, false, layout)
		if got := ansi.StringWidth(line); got != width {
			t.Fatalf("width=%d: row rendered at %d, want exactly %d: %q", width, got, width, line)
		}
		if plain := ansi.Strip(line); !utf8.ValidString(plain) {
			t.Fatalf("width=%d: truncated row is not valid UTF-8, a 2-cell rune was split: %q", width, plain)
		}
	}
	if !sawOddTitleWidth {
		t.Fatalf("no swept width produced an odd title column; the 2-cell boundary case was not exercised")
	}
}

func TestCJKSelectedRowKeepsBackgroundAcrossUsableWidth(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, false
	items := twoSessions()
	items[0].Title = "인증 플로우 리팩터링"
	value.result.Sessions = items
	value.refreshVisible()

	_, usable := contentFrame(value.width)
	layout := newRowLayout(rowSessions(value.rows), usable, value.deps.Now(), value.deps.LocalTarget, value.pins)
	line := value.renderRow(value.rows[1], true, layout)

	if missing := cellsWithoutBackground(line); len(missing) > 0 {
		t.Fatalf("selected background missing from cells %v: %q", missing, line)
	}
	if got := ansi.StringWidth(line); got != usable {
		t.Fatalf("selected width = %d, want usable %d", got, usable)
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
	layout := newRowLayout(rowSessions(value.rows), usable, value.deps.Now(), value.deps.LocalTarget, value.pins)
	line := value.renderRow(value.rows[1], true, layout)

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

func TestProviderColumnUsesBrandColors(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, false
	value = openAllGroups(value)

	content := value.View().Content
	claudeRow := rowContaining(content, "connection check")
	codexRow := rowContaining(content, "API repair")

	assertSpanForeground(t, claudeRow, "claude", true)
	if styled := value.styles.providerClaude.Render("claude"); !strings.Contains(claudeRow, styled) {
		t.Fatalf("claude provider does not use the coral style: %q", claudeRow)
	}

	assertSpanForeground(t, codexRow, "codex", true)
	if styled := value.styles.selectedCursor.Render("codex"); !strings.Contains(codexRow, styled) {
		t.Fatalf("codex provider does not reuse the selectedCursor teal style: %q", codexRow)
	}
}

func TestUnknownProviderColumnStaysMuted(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, false
	items := twoSessions()
	items[0].Provider = "gemini"
	value.result.Sessions = items
	value.refreshVisible()

	row := rowContaining(value.View().Content, "connection check")
	if styled := value.styles.muted.Render("gemini"); !strings.Contains(row, styled) {
		t.Fatalf("unknown provider does not use the muted style: %q", row)
	}
}

func TestSelectedRowKeepsBackgroundAcrossProviderColor(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, false
	_, usable := contentFrame(value.width)
	layout := newRowLayout(rowSessions(value.rows), usable, value.deps.Now(), value.deps.LocalTarget, value.pins)
	line := value.renderRow(value.rows[1], true, layout)

	if missing := cellsWithoutBackground(line); len(missing) > 0 {
		t.Fatalf("selected background missing from cells %v: %q", missing, line)
	}
	if styled := value.styles.providerClaude.Render("claude"); !strings.Contains(line, styled[:strings.Index(styled, "claude")]) {
		t.Fatalf("provider foreground missing from selected row: %q", line)
	}
}

func TestNoColorProviderColumnHasNoANSI(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, true
	row := activeRow(value.View().Content)
	if row != ansi.Strip(row) {
		t.Fatalf("noColor provider column contains ANSI escapes: %q", row)
	}
	if !strings.Contains(row, "claude") {
		t.Fatalf("noColor row missing plain provider text: %q", row)
	}
}

func TestSelectedHeaderKeepsBackgroundAcrossWidth(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, false
	_, usable := contentFrame(value.width)
	line := value.renderHeader(value.rows[0], true, usable)

	if missing := cellsWithoutBackground(line); len(missing) > 0 {
		t.Fatalf("selected header background missing from cells %v: %q", missing, line)
	}
	if ansi.StringWidth(line) != usable {
		t.Fatalf("selected header width = %d, want %d", ansi.StringWidth(line), usable)
	}
}

func TestIdleRowRendersIdleInsteadOfSavedState(t *testing.T) {
	value := readyModel()
	value = openAllGroups(value)
	value.width, value.height, value.noColor = 120, 24, true

	plainContent := value.View().Content
	idle := rowContaining(plainContent, "API repair")
	if !strings.Contains(idle, "idle") {
		t.Fatalf("idle row missing idle label: %q", idle)
	}
	if strings.Contains(plainContent, "saved") {
		t.Fatalf("view still shows the saved word:\n%s", plainContent)
	}

	// The # filter still admits RuntimeSaved sessions under the idle label.
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Text: "#"}))
	if sessions := rowSessions(value.rows); len(sessions) != 1 || sessions[0].Runtime.State != session.RuntimeSaved {
		t.Fatalf("rows under # = %+v, want the idle session", value.rows)
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
		if strings.Contains(line, "├─") || strings.Contains(line, "└─") {
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

func rowContaining(content, text string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(ansi.Strip(line), text) {
			return line
		}
	}
	return ""
}
