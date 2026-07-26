package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// emptyModel returns a ready model with zero sessions, isolating the
// truly-empty-inventory branch of sessionLines from the search/filter/
// loading/stale branches that sit next to it.
func emptyModel(width, height int, noColor bool) model {
	value := readyModel()
	value.width, value.height, value.noColor = width, height, noColor
	value.result.Sessions = nil
	value.refreshVisible()
	return value
}

// TestEmptyStateFullTierAtLargeScreen covers the full tier: a wide, tall
// terminal shows the static status-legend box logo alongside the existing
// copy, split across two hint lines.
func TestEmptyStateFullTierAtLargeScreen(t *testing.T) {
	value := emptyModel(120, 24, true)
	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "┌──┬──┬──┐") {
		t.Fatalf("full tier missing box logo top: %q", content)
	}
	if !strings.Contains(content, "│● │◐ │○ │") {
		t.Fatalf("full tier missing box logo glyphs: %q", content)
	}
	if !strings.Contains(content, "└──┴──┴──┘") {
		t.Fatalf("full tier missing box logo bottom: %q", content)
	}
	if !strings.Contains(content, "no sessions yet") {
		t.Fatalf("full tier missing title: %q", content)
	}
	if !strings.Contains(content, "start a claude/codex session, or") {
		t.Fatalf("full tier missing first hint line: %q", content)
	}
	if !strings.Contains(content, "add a remote with: ars remote add <host>") {
		t.Fatalf("full tier missing second hint line: %q", content)
	}
}

// TestEmptyStateCompactTierAtMediumHeight covers the compact tier: too
// short for the full box logo but tall enough to keep the pre-existing
// 3-line hint (title, blank, hint) instead of collapsing to one line.
func TestEmptyStateCompactTierAtMediumHeight(t *testing.T) {
	value := emptyModel(120, 10, true)
	content := ansi.Strip(value.View().Content)
	if strings.Contains(content, "┌──┬──┬──┐") {
		t.Fatalf("compact tier should not show the box logo: %q", content)
	}
	if !strings.Contains(content, "no sessions yet") {
		t.Fatalf("compact tier missing title: %q", content)
	}
	if !strings.Contains(content, "start a claude/codex session, or add a remote with: ars remote add <host>") {
		t.Fatalf("compact tier missing single-line hint: %q", content)
	}
}

// TestEmptyStateMinimalTierAtShortHeight covers the minimal tier: too
// short even for the compact 3-line hint, collapsing to a single line.
func TestEmptyStateMinimalTierAtShortHeight(t *testing.T) {
	value := emptyModel(120, 6, true)
	content := ansi.Strip(value.View().Content)
	if strings.Contains(content, "┌──┬──┬──┐") {
		t.Fatalf("minimal tier should not show the box logo: %q", content)
	}
	if !strings.Contains(content, "no sessions yet · ars remote add <host>") {
		t.Fatalf("minimal tier missing single-line hint: %q", content)
	}
}

// TestEmptyStateNarrowWidthStaysCompact covers the full tier's width gate:
// even at a tall height, a narrow terminal doesn't fit the box logo and
// must fall back to the compact hint instead of clipping the logo.
func TestEmptyStateNarrowWidthStaysCompact(t *testing.T) {
	value := emptyModel(40, 24, true)
	content := ansi.Strip(value.View().Content)
	if strings.Contains(content, "┌──┬──┬──┐") {
		t.Fatalf("narrow width should not show the box logo: %q", content)
	}
	if !strings.Contains(content, "no sessions yet") {
		t.Fatalf("narrow width missing title: %q", content)
	}
}

// TestEmptyStateNoColorLogoHasNoANSI enforces the NO_COLOR contract: the
// box logo's glyphs must render as bare characters, with no escape codes,
// when noColor is set.
func TestEmptyStateNoColorLogoHasNoANSI(t *testing.T) {
	value := emptyModel(120, 24, true)
	raw := value.View().Content
	if strings.Contains(raw, "\x1b[") {
		t.Fatalf("noColor empty state emitted ANSI escapes: %q", raw)
	}
}

// TestEmptyStateHeightContract checks the height budget at each tier,
// including the full tier's boundary (its minimum height, and one less,
// where it must demote to compact rather than let scrolledBody clip the
// box logo into a "N more" indicator). It doesn't probe extreme short
// heights below the fixed chrome's own floor (header/pill/help bar) —
// that's a pre-existing gap tracked separately, not part of this tier
// logic.
func TestEmptyStateHeightContract(t *testing.T) {
	cases := []struct {
		name   string
		height int
	}{
		{"full-tier-boundary", emptyStateFullMinHeight},
		{"full-tier-boundary-minus-one", emptyStateFullMinHeight - 1},
		{"compact-tier-boundary", emptyStateCompactMinHeight},
		{"compact-tier-boundary-minus-one (minimal tier)", emptyStateCompactMinHeight - 1},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			value := emptyModel(120, testCase.height, true)
			content := ansi.Strip(value.View().Content)
			lines := strings.Split(content, "\n")
			if len(lines) > testCase.height {
				t.Fatalf("height=%d rendered %d lines, want <= height:\n%s", testCase.height, len(lines), content)
			}
		})
	}
}

// TestEmptyStateOtherBranchesUnaffected guards the brief's scope boundary:
// the search/filter/loading/stale empty branches next to the truly-empty
// one must keep behaving exactly as before, regardless of height tier.
func TestEmptyStateOtherBranchesUnaffected(t *testing.T) {
	t.Run("search-no-matches", func(t *testing.T) {
		value := readyModel()
		value.width, value.height, value.noColor = 120, 24, true
		value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: '/'}))
		value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "zzz"}))
		content := ansi.Strip(value.View().Content)
		if !strings.Contains(content, `no matches for "zzz"`) {
			t.Fatalf("search-empty view regressed: %q", content)
		}
		if strings.Contains(content, "no sessions yet") {
			t.Fatalf("search-empty view leaked the new-user hint: %q", content)
		}
	})

	t.Run("loading", func(t *testing.T) {
		value := readyModel()
		value.width, value.height, value.noColor = 120, 24, true
		value.result = Result{}
		value.refreshVisible()
		value.collecting = true
		content := ansi.Strip(value.View().Content)
		if !strings.Contains(content, "loading sessions…") {
			t.Fatalf("loading view regressed: %q", content)
		}
		if strings.Contains(content, "no sessions yet") {
			t.Fatalf("loading view leaked the new-user hint: %q", content)
		}
	})

	t.Run("all-stale", func(t *testing.T) {
		value := allStaleModel()
		value.height = 24
		content := ansi.Strip(value.View().Content)
		if !strings.Contains(content, "all 2 sessions are older than 7d · a to show") {
			t.Fatalf("all-stale view regressed: %q", content)
		}
		if strings.Contains(content, "no sessions yet") {
			t.Fatalf("all-stale view leaked the new-user hint: %q", content)
		}
	})
}
