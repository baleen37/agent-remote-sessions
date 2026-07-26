package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/output"
	"github.com/baleen37/agent-remote-sessions/internal/session"
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

// TestEmptyStateHeightContractWithDiagnostics extends the height contract
// to diagnostic-line counts of 0/1/2 at and around the full-tier boundary,
// as requested in review: whatever tier is chosen, total rendered lines
// must never exceed height once diagnostics are factored in.
func TestEmptyStateHeightContractWithDiagnostics(t *testing.T) {
	heights := []int{emptyStateFullMinHeight - 1, emptyStateFullMinHeight, emptyStateFullMinHeight + 1, emptyStateFullMinHeight + 2}
	for _, height := range heights {
		for _, diagCount := range []int{0, 1, 2} {
			name := fmt.Sprintf("height=%d/diagnostics=%d", height, diagCount)
			t.Run(name, func(t *testing.T) {
				value := emptyModel(120, height, true)
				for index := 0; index < diagCount; index++ {
					value.result.Errors = append(value.result.Errors, output.HostError{
						Host:    "remote",
						Message: "error",
						Code:    "E",
					})
				}
				value.refreshVisible()
				content := ansi.Strip(value.View().Content)
				lines := strings.Split(content, "\n")
				if len(lines) > height {
					t.Fatalf("height=%d diagnostics=%d rendered %d lines, want <= height:\n%s", height, diagCount, len(lines), content)
				}
			})
		}
	}
}

// TestEmptyStateDemotesWhenDiagnosticsCrowdTheBudget guards against a gap
// found in review: diagnostics (host errors, warnings, the status line) are
// independent of the session inventory — a host can fail to connect while
// reporting zero sessions — so a height that comfortably fits the full
// tier's 8-line box logo on its own can still be too crowded once
// diagnostics take their share of the budget. Before the fix, emptyStateLines
// picked its tier from height alone, so boundedLayout would then shrink
// bodyHeight out from under the already-chosen full tier and scrolledBody
// would clip the box logo into a "N more" indicator — exactly what the
// tiering exists to avoid.
func TestEmptyStateDemotesWhenDiagnosticsCrowdTheBudget(t *testing.T) {
	t.Run("host-error", func(t *testing.T) {
		value := emptyModel(120, emptyStateFullMinHeight, true)
		value.result.Errors = []output.HostError{{Host: "remote1", Message: "connection refused", Code: "ECONNREFUSED"}}
		value.refreshVisible()
		content := ansi.Strip(value.View().Content)
		if strings.Contains(content, "┌──┬──┬──┐") {
			t.Fatalf("full tier should have been demoted by a host error: %q", content)
		}
		if strings.Contains(content, "more") {
			t.Fatalf("empty state body got clipped into a scroll indicator: %q", content)
		}
		if !strings.Contains(content, "connection refused") {
			t.Fatalf("host error itself must still render: %q", content)
		}
		lines := strings.Split(content, "\n")
		if len(lines) > emptyStateFullMinHeight {
			t.Fatalf("rendered %d lines, want <= %d:\n%s", len(lines), emptyStateFullMinHeight, content)
		}
	})

	t.Run("warning", func(t *testing.T) {
		value := emptyModel(120, emptyStateFullMinHeight, true)
		value.result.Warnings = []output.HostError{{Host: "remote2", Message: "slow to respond", Code: "SLOW"}}
		value.refreshVisible()
		content := ansi.Strip(value.View().Content)
		if strings.Contains(content, "┌──┬──┬──┐") {
			t.Fatalf("full tier should have been demoted by a warning: %q", content)
		}
		if strings.Contains(content, "more") {
			t.Fatalf("empty state body got clipped into a scroll indicator: %q", content)
		}
	})

	t.Run("status-line", func(t *testing.T) {
		value := emptyModel(120, emptyStateFullMinHeight, true)
		value.status = "kill failed: boom"
		content := ansi.Strip(value.View().Content)
		if strings.Contains(content, "┌──┬──┬──┐") {
			t.Fatalf("full tier should have been demoted by the status line: %q", content)
		}
		if strings.Contains(content, "more") {
			t.Fatalf("empty state body got clipped into a scroll indicator: %q", content)
		}
	})

	t.Run("enough-headroom-keeps-full-tier", func(t *testing.T) {
		// One extra line of height over the boundary is enough to absorb
		// one diagnostic line without demoting: the control for the three
		// cases above, proving the demotion is diagnostics-driven and not
		// just "any diagnostic always demotes".
		value := emptyModel(120, emptyStateFullMinHeight+1, true)
		value.result.Errors = []output.HostError{{Host: "remote1", Message: "connection refused", Code: "ECONNREFUSED"}}
		value.refreshVisible()
		content := ansi.Strip(value.View().Content)
		if !strings.Contains(content, "┌──┬──┬──┐") {
			t.Fatalf("full tier should survive one diagnostic line with one line of headroom: %q", content)
		}
		lines := strings.Split(content, "\n")
		if len(lines) > emptyStateFullMinHeight+1 {
			t.Fatalf("rendered %d lines, want <= %d:\n%s", len(lines), emptyStateFullMinHeight+1, content)
		}
	})
}

// TestEmptyStateStaysDemotedThroughStatusAutoDismiss guards a cross-task
// regression found in Task 12 review: emptyStateLines picks its tier fresh
// on every render from the diagnostics reserved that frame, so the error
// status's 5s auto-dismiss timer (Task 12) clearing value.status on its own
// used to shrink that reservation and silently promote the empty state from
// compact back to the full tier's box logo mid-countdown — a screen
// structure change with no user input at all. emptyDiagnosticFloor
// (model.go) exists to prevent exactly this: the floor only rises or gets
// reset by a real user action (keypress/resize), never falls on its own.
func TestEmptyStateStaysDemotedThroughStatusAutoDismiss(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 80, emptyStateFullMinHeight, true
	value.deps.Kill = func(context.Context, session.Session) error {
		return errors.New("boom")
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	seq := value.killSeq
	value, command := updateModel(value, killFireMsg{seq: seq})
	done := command().(killDoneMsg)
	value, _ = updateModel(value, done)
	if !strings.HasPrefix(value.status, "kill failed:") {
		t.Fatalf("status = %q, want a kill failed status", value.status)
	}

	// Empty the inventory directly (as emptyModel does) so the truly-empty
	// branch of sessionLines is exercised alongside the error status.
	value.result.Sessions = nil
	value.refreshVisible()

	demoted := ansi.Strip(value.View().Content)
	if strings.Contains(demoted, "┌──┬──┬──┐") {
		t.Fatalf("full tier should have been demoted by the error status: %q", demoted)
	}

	for range statusDismissSeconds {
		value, _ = updateModel(value, statusTickMsg{seq: value.statusSeq})
	}
	if value.status != "" {
		t.Fatalf("status = %q after %d ticks, want cleared", value.status, statusDismissSeconds)
	}

	afterDismiss := ansi.Strip(value.View().Content)
	if strings.Contains(afterDismiss, "┌──┬──┬──┐") {
		t.Fatalf("empty state promoted to the full tier on its own when the status auto-dismissed (no user input): %q", afterDismiss)
	}
	if !strings.Contains(afterDismiss, "no sessions yet") {
		t.Fatalf("empty state hint missing after auto-dismiss: %q", afterDismiss)
	}

	// A real user action (any keypress) resets the floor, so the tier is
	// free to reflect the now-empty diagnostics again.
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	afterInteraction := ansi.Strip(value.View().Content)
	if !strings.Contains(afterInteraction, "┌──┬──┬──┐") {
		t.Fatalf("empty state should promote back to the full tier once the user actually interacts: %q", afterInteraction)
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
