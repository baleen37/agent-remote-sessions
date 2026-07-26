package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// TestStackedHeightsPartitionsBodyHeight checks stackedHeights' contract
// directly: the list and preview rows it returns, plus both panels' title
// rows, must sum back to exactly bodyHeight, and each panel must clear its
// floor (stackedListMinRows, stackedPreviewMinRows).
func TestStackedHeightsPartitionsBodyHeight(t *testing.T) {
	value := model{previewPct: defaultPreviewPct}
	minTotal := 2*panelTitleHeight + stackedListMinRows + stackedPreviewMinRows
	for _, bodyHeight := range []int{minTotal, minTotal + 3, 20, 30, 50} {
		listRows, previewRows, ok := value.stackedHeights(bodyHeight)
		if !ok {
			t.Fatalf("stackedHeights(%d) reported not ok, want a fit", bodyHeight)
		}
		if listRows < stackedListMinRows {
			t.Fatalf("stackedHeights(%d) listRows = %d, below the %d-row floor", bodyHeight, listRows, stackedListMinRows)
		}
		if previewRows < stackedPreviewMinRows {
			t.Fatalf("stackedHeights(%d) previewRows = %d, below the %d-row floor", bodyHeight, previewRows, stackedPreviewMinRows)
		}
		if total := listRows + previewRows + 2*panelTitleHeight; total != bodyHeight {
			t.Fatalf("stackedHeights(%d) listRows=%d previewRows=%d; total with titles = %d, want %d", bodyHeight, listRows, previewRows, total, bodyHeight)
		}
	}
}

// TestStackedHeightsDemotesBelowFloor checks the silent-demotion contract:
// once bodyHeight can't fit both panels at their floors, stackedHeights
// reports not ok rather than returning an undersized partition, so callers
// fall back to rendering the list alone with no error message.
func TestStackedHeightsDemotesBelowFloor(t *testing.T) {
	value := model{previewPct: defaultPreviewPct}
	minTotal := 2*panelTitleHeight + stackedListMinRows + stackedPreviewMinRows
	if _, _, ok := value.stackedHeights(minTotal); !ok {
		t.Fatalf("stackedHeights(%d) at the exact floor reported not ok, want a fit", minTotal)
	}
	if _, _, ok := value.stackedHeights(minTotal - 1); ok {
		t.Fatalf("stackedHeights(%d), one row under the floor, reported ok, want demotion", minTotal-1)
	}
	if _, _, ok := value.stackedHeights(0); ok {
		t.Fatal("stackedHeights(0) reported ok, want demotion")
	}
}

// TestStackedLayoutDemotesPreviewWhenBodyTooShort drives the demotion
// through View(): a terminal short enough that stacked can't fit both
// panels must render the list alone, with no crash and no preview panel
// text, rather than an error or a truncated preview.
func TestStackedLayoutDemotesPreviewWhenBodyTooShort(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	value.width = 80
	value.height = frameTop + frameBottom + 2*panelTitleHeight + stackedListMinRows + stackedPreviewMinRows - 1
	if got := previewLayoutOf(value.contentWidth()); got != previewStacked {
		t.Fatalf("previewLayoutOf(%d) = %v, want previewStacked", value.contentWidth(), got)
	}
	content := ansi.Strip(value.View().Content)
	if strings.Contains(content, "PREVIEW") {
		t.Fatalf("preview panel rendered despite bodyHeight below the stacked floor:\n%s", content)
	}
	if !strings.Contains(content, "connection check") {
		t.Fatalf("list content missing when the preview was demoted:\n%s", content)
	}
}

// TestStackedLayoutHeightContract locks in the brief's height contract at
// the representative stacked width (80): the total rendered line count must
// never exceed the model's height budget.
func TestStackedLayoutHeightContract(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output\nsecond line\nthird line"), nil
	})
	value.width = 80
	for _, height := range []int{12, 16, 20, 24, 30, 40} {
		value.height = height
		if got := previewLayoutOf(value.contentWidth()); got != previewStacked {
			t.Fatalf("height %d: previewLayoutOf(%d) = %v, want previewStacked", height, value.contentWidth(), got)
		}
		lines := strings.Split(value.View().Content, "\n")
		if len(lines) > height {
			t.Fatalf("height %d: rendered %d lines, want <= %d:\n%s", height, len(lines), height, value.View().Content)
		}
	}
}

// TestStackedLayoutListMinimumRows checks the brief's list floor: even when
// previewPct is pushed to its maximum (favoring the preview), the list panel
// must still get at least stackedListMinRows body rows.
func TestStackedLayoutListMinimumRows(t *testing.T) {
	value := model{previewPct: previewPctMax}
	bodyHeight := 2*panelTitleHeight + stackedListMinRows + stackedPreviewMinRows + 3
	listRows, _, ok := value.stackedHeights(bodyHeight)
	if !ok {
		t.Fatalf("stackedHeights(%d) at previewPctMax reported not ok, want a fit", bodyHeight)
	}
	if listRows < stackedListMinRows {
		t.Fatalf("stackedHeights(%d) at previewPctMax listRows = %d, below the %d-row floor", bodyHeight, listRows, stackedListMinRows)
	}
}

// TestPageStepUsesStackedListShare checks that pageStep, used by movePage
// for j/k paging, partitions bodyHeight through the same stackedHeights call
// View() uses for rendering — so a page-down press moves the selection by
// the list panel's row share, not the full (list+preview) bodyHeight, which
// would overshoot past what's actually visible in the list column.
func TestPageStepUsesStackedListShare(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	value.width = 80
	value.height = 30
	if got := previewLayoutOf(value.contentWidth()); got != previewStacked {
		t.Fatalf("previewLayoutOf(%d) = %v, want previewStacked", value.contentWidth(), got)
	}

	_, width := contentFrame(value.contentWidth())
	details := detailLines(mustSelectedSession(t, value), width, value.deps.Now())
	diagnostics := value.diagnostics(width)
	_, _, bodyHeight := value.boundedLayout(details, mustSelectedSession(t, value), diagnostics, 0, width)
	listRows, _, ok := value.stackedHeights(bodyHeight)
	if !ok {
		t.Fatal("stackedHeights reported not ok for the 80x30 stacked fixture")
	}

	if listRows == bodyHeight {
		t.Fatal("fixture's list share equals its full bodyHeight; this test needs them to differ to be meaningful")
	}
	if step := value.pageStep(); step != max(1, listRows) {
		t.Fatalf("pageStep() = %d, want the stacked list share %d (not the full bodyHeight %d)", step, listRows, bodyHeight)
	}
}

func mustSelectedSession(t *testing.T, value model) session.Session {
	t.Helper()
	selected, ok := value.selectedSession()
	if !ok {
		t.Fatal("no selected session in fixture")
	}
	return selected
}
