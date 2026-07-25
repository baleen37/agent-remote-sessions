package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// overlayLines renders the help overlay and returns its plain, un-indented
// lines so tests can assert on ordering.
func overlayLines(t *testing.T, value model) []string {
	t.Helper()
	value.showHelp = true
	lines := strings.Split(ansi.Strip(value.View().Content), "\n")
	for index, line := range lines {
		lines[index] = strings.TrimRight(strings.TrimLeft(line, " "), " ")
	}
	return lines
}

func TestHelpOverlayFeaturesGroupHeaderBindings(t *testing.T) {
	value := groupKillModel(t)
	value.width, value.height, value.noColor = 120, 40, true
	value.previewOn = false
	lines := overlayLines(t, value)

	label := lineContaining(t, lines, "on a group header")
	all := lineContaining(t, lines, "all keys")
	if label >= all {
		t.Fatalf("context label at %d is not above the all-keys label at %d:\n%s", label, all, strings.Join(lines, "\n"))
	}
	for _, want := range []string{"toggle group", "fold", "jump to group", "kill session / group"} {
		if index := lineContaining(t, lines, want); index < label || index > all {
			t.Fatalf("featured binding %q at line %d is outside the context section (%d..%d):\n%s",
				want, index, label, all, strings.Join(lines, "\n"))
		}
	}
	if kill, search := lineContaining(t, lines, "kill session / group"), lineContaining(t, lines, "search"); kill > search {
		t.Fatalf("x kill at %d must precede / search at %d on a group header:\n%s", kill, search, strings.Join(lines, "\n"))
	}
}

func TestHelpOverlayFeaturesSessionRowAndPreviewBindings(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("output"), nil
	})
	value.height, value.noColor = 40, true
	if _, ok := value.selectedSession(); !ok {
		t.Fatalf("expected a session row selected, got %+v", value.rows[value.selected])
	}
	if !value.previewVisible() {
		t.Fatal("expected the preview pane visible")
	}
	lines := overlayLines(t, value)

	label := lineContaining(t, lines, "on a session")
	all := lineContaining(t, lines, "all keys")
	for _, want := range []string{"attach session", "send a line without attaching", "kill session / group", "pin / unpin", "preview pane"} {
		if index := lineContaining(t, lines, want); index < label || index > all {
			t.Fatalf("featured binding %q at line %d is outside the context section (%d..%d):\n%s",
				want, index, label, all, strings.Join(lines, "\n"))
		}
	}
	// The preview line is additive and comes after the row-kind bindings.
	if preview, pin := lineContaining(t, lines, "preview pane"), lineContaining(t, lines, "pin / unpin"); preview < pin {
		t.Fatalf("preview line at %d must follow the row-kind bindings (pin at %d):\n%s", preview, pin, strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[lineContaining(t, lines, "preview pane")], "close") {
		t.Fatalf("featured preview line must offer closing the pane:\n%s", strings.Join(lines, "\n"))
	}
}

func TestHelpOverlayFeaturesUndoFirstWhenKillPending(t *testing.T) {
	value := groupKillModel(t)
	value.width, value.height, value.noColor = 120, 40, true
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	if !value.killPending {
		t.Fatal("x on a group header did not arm a pending kill")
	}
	lines := overlayLines(t, value)

	label := lineContaining(t, lines, "kill pending")
	undo := lineContaining(t, lines, "undo the pending kill")
	if undo != label+1 {
		t.Fatalf("u undo at %d is not the first featured row after the label at %d:\n%s",
			undo, label, strings.Join(lines, "\n"))
	}
}

// TestHelpOverlayKeysetIsStableAcrossContexts pins the parity contract: the
// context ordering may move rows but must never add, drop or duplicate one.
func TestHelpOverlayKeysetIsStableAcrossContexts(t *testing.T) {
	headerModel := groupKillModel(t)
	headerModel.width, headerModel.height, headerModel.noColor = 120, 40, true

	pendingModel, _ := updateModel(headerModel, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))

	sessionModel := headerModel
	sessionModel.move(1)
	if _, ok := sessionModel.selectedSession(); !ok {
		t.Fatalf("expected a session row after moving down, got %+v", sessionModel.rows[sessionModel.selected])
	}

	previewOn := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("output"), nil
	})
	previewOn.height, previewOn.noColor = 40, true

	filtered := headerModel
	filtered.stateFilter = map[session.RuntimeState]bool{session.RuntimeAttached: true}
	filtered.refreshVisible()

	var baseline []string
	for _, testCase := range []struct {
		name  string
		value model
	}{
		{"group header", headerModel},
		{"session row", sessionModel},
		{"kill pending", pendingModel},
		{"preview on", previewOn},
		{"filter active", filtered},
	} {
		keys := overlayBindingKeys(t, testCase.value)
		if len(keys) == 0 {
			t.Fatalf("%s: overlay rendered no binding rows", testCase.name)
		}
		for index, key := range keys {
			if slices.Index(keys, key) != index {
				t.Fatalf("%s: binding %q appears twice in the overlay: %v", testCase.name, key, keys)
			}
		}
		sorted := slices.Clone(keys)
		slices.Sort(sorted)
		if baseline == nil {
			baseline = sorted
			continue
		}
		if !slices.Equal(sorted, baseline) {
			t.Fatalf("%s: keyset %v differs from the baseline %v", testCase.name, sorted, baseline)
		}
	}
}

// overlayBindingKeys returns the key column of every binding row in the
// overlay, identified by matching the rendered lines against the binding table.
func overlayBindingKeys(t *testing.T, value model) []string {
	t.Helper()
	lines := overlayLines(t, value)
	var keys []string
	for _, binding := range helpBindings {
		for _, line := range lines {
			if strings.HasPrefix(line, binding.key+"  ") && strings.Contains(line, binding.description(value)) {
				keys = append(keys, binding.key)
				break
			}
		}
	}
	return keys
}

// TestHelpContextLabelAccompaniesEveryFeaturedSet guards the invariant the
// filter-only context first broke: helpOverlay only renders the featured
// section when there is a label for it, so any state that features a row
// without one silently drops that row from the overlay.
func TestHelpContextLabelAccompaniesEveryFeaturedSet(t *testing.T) {
	base := groupKillModel(t)
	base.width, base.height, base.noColor = 120, 40, true

	noRows := base
	noRows.stateFilter = map[session.RuntimeState]bool{session.RuntimeAttached: true}
	noRows.refreshVisible()
	if len(noRows.rows) != 0 {
		t.Fatalf("expected the attached filter to empty the list, got %d rows", len(noRows.rows))
	}

	for _, testCase := range []struct {
		name  string
		value model
	}{
		{"group header", base},
		{"filter active with no rows", noRows},
	} {
		featured, _ := testCase.value.featuredHelp()
		if len(featured) > 0 && testCase.value.helpContextLabel() == "" {
			t.Fatalf("%s: %d featured bindings with no context label to render them under",
				testCase.name, len(featured))
		}
	}
}

func TestHelpOverlayNoColorStaysLegible(t *testing.T) {
	value := groupKillModel(t)
	value.width, value.height, value.noColor = 120, 40, true
	value.showHelp = true
	content := value.View().Content
	if content != ansi.Strip(content) {
		t.Fatalf("NO_COLOR overlay emitted escape sequences:\n%q", content)
	}
	for _, want := range []string{"ars keys", "on a group header", "all keys", "? / esc / q to close"} {
		if !strings.Contains(content, want) {
			t.Fatalf("NO_COLOR overlay missing %q:\n%s", want, content)
		}
	}
}

// TestHelpOverlayStaysWithinHeightBudget guards the ~3-line allowance the
// context section may add: the label, the blank separator and the "all keys:"
// heading. Measured against one binding row per key plus the fixed frame, so
// the check stays honest about layout overhead even as the keyset grows.
func TestHelpOverlayStaysWithinHeightBudget(t *testing.T) {
	value := groupKillModel(t)
	value.width, value.height, value.noColor = 120, 40, true
	// title + blank + one row per binding + blank + legend + close hint.
	flat := 2 + len(helpBindings) + 3
	contextual := len(overlayLines(t, value))
	if contextual > flat+3 {
		t.Fatalf("contextual overlay is %d lines, more than 3 over the %d-line flat listing", contextual, flat)
	}
}
