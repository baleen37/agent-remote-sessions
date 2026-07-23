package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// indicatorLine returns the first body line carrying a scroll indicator for the
// given arrow, skipping the "↑↓/jk move" footer hint which is not an indicator.
func indicatorLine(content, arrow string) string {
	for _, line := range strings.Split(content, "\n") {
		plain := ansi.Strip(line)
		if strings.Contains(plain, arrow+" ") && strings.Contains(plain, "more") &&
			!strings.Contains(plain, "jk move") {
			return plain
		}
	}
	return ""
}

func manySessionsModel(count int) model {
	value := readyModel()
	value.result.Sessions = manySessions(count)
	value = openAllGroups(value)
	return value
}

func TestScrollIndicatorShowsHiddenBelowFromTop(t *testing.T) {
	value := manySessionsModel(20)
	value.width, value.height, value.noColor = 80, 10, true
	value.selectRow(0)

	content := ansi.Strip(value.View().Content)
	if indicatorLine(content, "↓") == "" {
		t.Fatalf("view missing bottom scroll indicator:\n%s", content)
	}
	if indicatorLine(content, "↑") != "" {
		t.Fatalf("top of list must not show an up indicator:\n%s", content)
	}
	if lines := strings.Count(content, "\n") + 1; lines > value.height {
		t.Fatalf("view height = %d, want <= %d:\n%s", lines, value.height, content)
	}
}

func TestScrollIndicatorShowsHiddenAboveAndBelowInMiddle(t *testing.T) {
	value := manySessionsModel(20)
	value.width, value.height, value.noColor = 80, 10, true
	value.selectRow(len(value.rows) / 2)

	content := ansi.Strip(value.View().Content)
	if indicatorLine(content, "↑") == "" {
		t.Fatalf("view missing top scroll indicator:\n%s", content)
	}
	if indicatorLine(content, "↓") == "" {
		t.Fatalf("view missing bottom scroll indicator:\n%s", content)
	}
}

func TestScrollIndicatorShowsHiddenAboveAtEnd(t *testing.T) {
	value := manySessionsModel(20)
	value.width, value.height, value.noColor = 80, 10, true
	value.selectRow(len(value.rows) - 1)

	content := ansi.Strip(value.View().Content)
	if indicatorLine(content, "↑") == "" {
		t.Fatalf("view missing top scroll indicator at end:\n%s", content)
	}
	if indicatorLine(content, "↓") != "" {
		t.Fatalf("end of list must not show a down indicator:\n%s", content)
	}
}

func TestScrollIndicatorCountsHiddenLines(t *testing.T) {
	value := manySessionsModel(20)
	value.width, value.height, value.noColor = 80, 10, true
	value.selectRow(0)

	body, _ := value.sessionLines(value.width)
	total := len(body)

	content := ansi.Strip(value.View().Content)
	indicator := strings.TrimSpace(indicatorLine(content, "↓"))
	if indicator == "" {
		t.Fatalf("no bottom indicator line found:\n%s", content)
	}
	var hidden int
	if _, err := fmt.Sscanf(indicator, "↓ %d more", &hidden); err != nil {
		t.Fatalf("indicator %q not in \"↓ N more\" form: %v", indicator, err)
	}
	// At the top the up-indicator is absent, so every hidden row is below: the
	// shown session/header rows plus the hidden count must equal the whole list.
	shown := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "session ") || strings.Contains(line, "api (20)") {
			shown++
		}
	}
	if shown+hidden != total {
		t.Fatalf("shown(%d) + hidden(%d) != total(%d)\n%s", shown, hidden, total, content)
	}
}

func TestScrollIndicatorAbsentWhenListFits(t *testing.T) {
	value := readyModel()
	value = openAllGroups(value)
	value.width, value.height, value.noColor = 80, 24, true

	content := ansi.Strip(value.View().Content)
	if indicatorLine(content, "↑") != "" || indicatorLine(content, "↓") != "" {
		t.Fatalf("short list must not show scroll indicators:\n%s", content)
	}
}

func TestScrollIndicatorIsMutedAndDoesNotShiftSelection(t *testing.T) {
	value := manySessionsModel(20)
	value.width, value.height, value.noColor = 80, 10, false
	value.selectRow(len(value.rows) / 2)
	before := value.selectedRef

	raw := value.View().Content
	// Rendering the view must never mutate selection.
	if value.selectedRef != before {
		t.Fatalf("View() moved selection")
	}

	var upLine string
	for _, line := range strings.Split(raw, "\n") {
		plain := ansi.Strip(line)
		if strings.Contains(plain, "↑ ") && strings.Contains(plain, "more") && !strings.Contains(plain, "jk move") {
			upLine = line
			break
		}
	}
	if upLine == "" {
		t.Fatalf("no up indicator line:\n%s", raw)
	}
	// The muted style is Faint(true); the whole indicator is one faint span, so
	// its content starts with the faint SGR and it is not a selectable row.
	if inner := strings.TrimPrefix(upLine, " "); !strings.HasPrefix(inner, "\x1b[2m") {
		t.Fatalf("indicator line is not muted-styled: %q", upLine)
	}
	if plain := ansi.Strip(upLine); strings.HasPrefix(strings.TrimLeft(plain, " "), "> ") {
		t.Fatalf("indicator line looks selectable: %q", plain)
	}
}

func TestScrollIndicatorWorksWithPreviewPane(t *testing.T) {
	value := manySessionsModel(20)
	value.deps.Preview = func(_ context.Context, _ session.Session) ([]byte, error) { return []byte("pane"), nil }
	value.width, value.height, value.noColor = 140, 10, true
	value.previewOn = true
	value.selectRow(len(value.rows) / 2)

	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "↑") || !strings.Contains(content, "↓") {
		t.Fatalf("preview-mode view missing scroll indicators:\n%s", content)
	}
	for _, line := range strings.Split(value.View().Content, "\n") {
		if ansi.StringWidth(line) > value.width {
			t.Fatalf("line exceeds width %d: %q", value.width, line)
		}
	}
}

func TestScrollIndicatorKeepsBodyWithinHeightAtTransition(t *testing.T) {
	// selectRow(3) is a transition point where the down-anchored window flips
	// from needing only a bottom indicator to needing a top one too; the layout
	// must still fit the viewport exactly.
	value := manySessionsModel(20)
	value.width, value.height, value.noColor = 80, 10, true
	value.selectRow(3)

	rendered := strings.Count(value.View().Content, "\n") + 1
	if rendered > value.height {
		t.Fatalf("rendered lines = %d, want <= %d:\n%s", rendered, value.height, ansi.Strip(value.View().Content))
	}
}

func TestScrollIndicatorBodyNeverExceedsHeightAcrossSelections(t *testing.T) {
	for _, height := range []int{6, 8, 10, 12} {
		for selected := range 20 {
			value := manySessionsModel(20)
			value.width, value.height, value.noColor = 80, height, true
			value.selectRow(selected)

			rendered := strings.Count(value.View().Content, "\n") + 1
			if rendered > value.height {
				t.Fatalf("height=%d selected=%d: rendered lines = %d, want <= %d:\n%s",
					height, selected, rendered, value.height, ansi.Strip(value.View().Content))
			}
		}
	}
}

func TestScrollIndicatorWorksWhileFiltering(t *testing.T) {
	value := manySessionsModel(20)
	value.width, value.height, value.noColor = 80, 10, true
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: '/'}))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "session"}))
	value.selectRow(0)

	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "↓") {
		t.Fatalf("filtered view missing scroll indicator:\n%s", content)
	}
}
