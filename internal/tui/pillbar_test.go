package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// TestPillBarAlwaysRenders locks the pill bar as a permanent line under the
// header: it must be present even with zero sessions, so toggling a filter
// never makes the layout jump vertically.
func TestPillBarAlwaysRenders(t *testing.T) {
	value := readyModel()
	value.noColor = true
	value.width = 120
	value.result.Sessions = nil
	value.refreshVisible()

	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "All") {
		t.Fatalf("pill bar missing with zero sessions: %q", content)
	}
	for _, want := range []string{"● 0", "◐ 0", "○ 0", "? 0"} {
		if !strings.Contains(content, want) {
			t.Fatalf("pill bar missing zero-count pill %q: %q", want, content)
		}
	}
}

// TestPillBarMarksActiveFilters checks that toggling a state filter switches
// which pill renders as active (noColor: bracketed) without touching the
// others, and that All is active only when no filter is set.
func TestPillBarMarksActiveFilters(t *testing.T) {
	value := readyModel()
	value.noColor = true
	value.width = 120

	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "[All]") {
		t.Fatalf("All pill not active with no filter: %q", content)
	}

	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Text: "!"}))
	content = ansi.Strip(value.View().Content)
	if strings.Contains(content, "[All]") {
		t.Fatalf("All pill still active once a filter is set: %q", content)
	}
	if !strings.Contains(content, "[● 1]") {
		t.Fatalf("attached pill not marked active: %q", content)
	}
	if strings.Contains(content, "[○ 1]") {
		t.Fatalf("idle pill incorrectly marked active: %q", content)
	}
}

// TestPillBarKeepsZeroCountPillsVisible ensures a pill with a zero count
// stays on the line (so the layout doesn't jump) instead of being omitted.
func TestPillBarKeepsZeroCountPillsVisible(t *testing.T) {
	value := readyModel()
	value.noColor = false
	value.width = 120

	content := value.pillBar(value.width)
	plain := ansi.Strip(content)
	if !strings.Contains(plain, "◐ 0") || !strings.Contains(plain, "? 0") {
		t.Fatalf("zero-count pills missing from bar: %q", plain)
	}
	faintZero := value.styles.muted.Render("◐ 0")
	if !strings.Contains(content, faintZero) {
		t.Fatalf("zero-count pill not rendered faint: %q", content)
	}
}

// TestPillBarNoColorBracketsOnlyActivePills locks the noColor presentation:
// only the active pill(s) get brackets, inactive ones render plain.
func TestPillBarNoColorBracketsOnlyActivePills(t *testing.T) {
	value := readyModel()
	value.noColor = true
	value.width = 120
	value.toggleStateFilter(session.RuntimeAttached)
	value.refreshVisible()

	got := value.pillBar(value.width)
	if !strings.Contains(got, "[● 1]") {
		t.Fatalf("active pill missing brackets: %q", got)
	}
	if strings.Contains(got, "[○ 1]") {
		t.Fatalf("inactive pill incorrectly bracketed: %q", got)
	}
	if strings.Contains(got, "[All]") {
		t.Fatalf("All incorrectly bracketed while a filter is active: %q", got)
	}
}

// TestPillBarCountsIgnoreActiveStateFilter is the core counting contract:
// counts reflect what each filter would reveal, computed before the state
// filter narrows the visible rows, so toggling one pill doesn't change the
// numbers shown on the others.
func TestPillBarCountsIgnoreActiveStateFilter(t *testing.T) {
	value := readyModel()
	value.noColor = true
	value.width = 120

	before := value.pillBar(value.width)
	if !strings.Contains(before, "● 1") || !strings.Contains(before, "○ 1") {
		t.Fatalf("pill bar counts before filtering = %q", before)
	}

	value.toggleStateFilter(session.RuntimeRunning)
	value.refreshVisible()

	after := value.pillBar(value.width)
	if !strings.Contains(after, "● 1") || !strings.Contains(after, "○ 1") {
		t.Fatalf("pill bar counts changed after filtering by a different state: %q", after)
	}
}
