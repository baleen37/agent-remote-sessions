package tui

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

func typeText(value model, text string) model {
	for _, character := range text {
		value = pressKey(value, character, string(character))
	}
	return value
}

func TestFullscreenSlashOpensBufferSearchInput(t *testing.T) {
	value := loadedFullscreenModel(t, "connection lost\nretrying\nconnection ok\n")
	value = pressKey(value, '/', "/")
	if !value.previewSearching {
		t.Fatal("/ in fullscreen did not open buffer search input")
	}
	value = typeText(value, "conn")
	if value.previewSearchQuery != "conn" {
		t.Fatalf("search query = %q, want %q", value.previewSearchQuery, "conn")
	}
	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "/conn") {
		t.Fatalf("fullscreen did not render the search input line:\n%s", content)
	}
}

func TestFullscreenSearchEscCancelsInput(t *testing.T) {
	value := loadedFullscreenModel(t, "connection lost\nretrying\nconnection ok\n")
	value = pressKey(value, '/', "/")
	value = typeText(value, "conn")
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if value.previewSearching {
		t.Fatal("esc did not cancel the search input")
	}
	if value.previewSearchQuery != "" {
		t.Fatalf("esc-cancelled search left query %q, want empty", value.previewSearchQuery)
	}
	if !value.previewFullscreen {
		t.Fatal("esc that cancels search input must not also close fullscreen")
	}
}

func TestFullscreenSearchConfirmActivatesAndJumps(t *testing.T) {
	lines := make([]string, 50)
	for index := range lines {
		lines[index] = "line-" + strconv.Itoa(index)
	}
	lines[10] = "needle here"
	value := loadedFullscreenModel(t, strings.Join(lines, "\n"))
	value = pressKey(value, '/', "/")
	value = typeText(value, "needle")
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if value.previewSearching {
		t.Fatal("enter did not close the search input")
	}
	if !value.previewSearchActive {
		t.Fatal("enter did not activate the buffer search")
	}
	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "needle here") {
		t.Fatalf("confirming search did not scroll to the match:\n%s", content)
	}
}

func TestFullscreenSearchZeroMatchesShowsFeedbackAndNoScroll(t *testing.T) {
	lines := make([]string, 50)
	for index := range lines {
		lines[index] = "line-" + strconv.Itoa(index)
	}
	value := loadedFullscreenModel(t, strings.Join(lines, "\n"))
	before := value.previewScrollOffset
	value = pressKey(value, '/', "/")
	value = typeText(value, "nonexistent")
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if value.previewSearchActive {
		t.Fatal("confirming a query with zero matches must not activate an active search state")
	}
	if value.previewScrollOffset != before {
		t.Fatalf("zero-match search moved the viewport: %d -> %d", before, value.previewScrollOffset)
	}
	content := ansi.Strip(value.View().Content)
	if !strings.Contains(content, "no matches") {
		t.Fatalf("zero-match search missing feedback:\n%s", content)
	}
}

func TestFullscreenSearchCaseInsensitivePartialMatch(t *testing.T) {
	value := loadedFullscreenModel(t, "Hello World\nsomething else\n")
	value = pressKey(value, '/', "/")
	value = typeText(value, "WORLD")
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if !value.previewSearchActive {
		t.Fatal("case-insensitive partial match did not activate search")
	}
	if len(value.previewSearchMatches) != 1 {
		t.Fatalf("expected 1 match, got %d: %v", len(value.previewSearchMatches), value.previewSearchMatches)
	}
}

func TestFullscreenSearchNextPreviousWrapsAround(t *testing.T) {
	lines := make([]string, 50)
	for index := range lines {
		lines[index] = "line-" + strconv.Itoa(index)
	}
	lines[5] = "needle A"
	lines[20] = "needle B"
	lines[40] = "needle C"
	value := loadedFullscreenModel(t, strings.Join(lines, "\n"))
	value = pressKey(value, '/', "/")
	value = typeText(value, "needle")
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if !value.previewSearchActive {
		t.Fatal("search did not activate")
	}
	firstIndex := value.previewSearchIndex

	value = pressKey(value, 'n', "n")
	secondIndex := value.previewSearchIndex
	if secondIndex == firstIndex {
		t.Fatalf("n did not move to the next match: stayed at index %d", firstIndex)
	}

	value = pressKey(value, 'n', "n")
	thirdIndex := value.previewSearchIndex

	// One more n from the last match should wrap back to the first.
	value = pressKey(value, 'n', "n")
	if value.previewSearchIndex != firstIndex {
		t.Fatalf("n did not wrap around after the last match: got index %d, want %d (indices seen: %d, %d, %d)",
			value.previewSearchIndex, firstIndex, firstIndex, secondIndex, thirdIndex)
	}

	// N from the first match should wrap back to the last.
	value = pressKey(value, 'N', "N")
	if value.previewSearchIndex != thirdIndex {
		t.Fatalf("N did not wrap back to the previous (last) match: got %d, want %d", value.previewSearchIndex, thirdIndex)
	}
}

func TestFullscreenSearchHighlightsMatchAndShowsCount(t *testing.T) {
	lines := make([]string, 30)
	for index := range lines {
		lines[index] = "line-" + strconv.Itoa(index)
	}
	lines[10] = "needle here"
	value := loadedFullscreenModel(t, strings.Join(lines, "\n"))
	value.noColor = false
	value = pressKey(value, '/', "/")
	value = typeText(value, "needle")
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	raw := value.View().Content
	if !strings.Contains(raw, "\x1b[") {
		t.Fatalf("search match highlight missing escape sequences in colored mode:\n%q", raw)
	}
	plain := ansi.Strip(raw)
	if !strings.Contains(plain, "1/1") {
		t.Fatalf("fullscreen search missing the match count indicator:\n%s", plain)
	}
}

func TestFullscreenSearchEscHierarchy(t *testing.T) {
	value := loadedFullscreenModel(t, "connection lost\nretrying\nconnection ok\n")

	// No search: esc closes fullscreen (existing behavior).
	plain := value
	plain, _ = updateModel(plain, tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if plain.previewFullscreen {
		t.Fatal("esc with no search active did not close fullscreen")
	}

	// Search input active: esc cancels input only.
	inputting := pressKey(value, '/', "/")
	inputting = typeText(inputting, "conn")
	inputting, _ = updateModel(inputting, tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if inputting.previewSearching {
		t.Fatal("esc did not cancel search input")
	}
	if !inputting.previewFullscreen {
		t.Fatal("esc cancelling search input closed fullscreen too")
	}

	// Confirmed search: esc clears the active search first.
	confirmed := pressKey(value, '/', "/")
	confirmed = typeText(confirmed, "conn")
	confirmed, _ = updateModel(confirmed, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if !confirmed.previewSearchActive {
		t.Fatal("test setup: search did not activate")
	}
	confirmed, _ = updateModel(confirmed, tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if confirmed.previewSearchActive {
		t.Fatal("esc did not clear the active search")
	}
	if !confirmed.previewFullscreen {
		t.Fatal("esc clearing the active search closed fullscreen too")
	}

	// A second esc, with search now cleared, closes fullscreen.
	confirmed, _ = updateModel(confirmed, tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if confirmed.previewFullscreen {
		t.Fatal("esc after search was cleared did not close fullscreen")
	}
}

func TestFullscreenSearchLiteralKeysDuringInput(t *testing.T) {
	value := loadedFullscreenModel(t, "live output\n")
	value = pressKey(value, '/', "/")
	value = typeText(value, "jkn")
	if value.previewSearchQuery != "jkn" {
		t.Fatalf("j/k/n typed during search input must be literal, got query %q", value.previewSearchQuery)
	}
	if value.previewScrollOffset != 0 {
		t.Fatal("j/k typed during search input must not scroll the viewport")
	}
}

func TestFullscreenSearchFIsLiteralDuringInput(t *testing.T) {
	value := loadedFullscreenModel(t, "live output\n")
	value = pressKey(value, '/', "/")
	value = pressKey(value, 'f', "f")
	if value.previewSearchQuery != "f" {
		t.Fatalf("f typed during search input must be literal, got query %q", value.previewSearchQuery)
	}
	if !value.previewFullscreen {
		t.Fatal("f typed during search input must not close fullscreen")
	}
}

func TestFullscreenSearchQuerySurvivesRecapture(t *testing.T) {
	capture := "needle first\nother\n"
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte(capture), nil
	})
	command := value.syncPreview()
	value, _ = updateModel(value, drainPreviewMsg(command))
	value = pressKey(value, 'f', "f")

	value = pressKey(value, '/', "/")
	value = typeText(value, "needle")
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if !value.previewSearchActive {
		t.Fatal("test setup: search did not activate")
	}

	selected, _ := value.selectedSession()
	capture = "before\nneedle second\nafter\n"
	value, command = value.updateFullPreviewTick(fullPreviewTickMsg{key: keyOf(selected)})
	if command == nil {
		t.Fatal("fullscreen tick stopped rescheduling")
	}
	value, _ = updateModel(value, drainFullPreviewMsg(command))

	if value.previewSearchQuery != "needle" {
		t.Fatalf("recapture lost the search query: %q", value.previewSearchQuery)
	}
	if !value.previewSearchActive {
		t.Fatal("recapture cleared the active search state")
	}
	if len(value.previewSearchMatches) != 1 {
		t.Fatalf("recapture did not recompute matches against the new buffer: %v", value.previewSearchMatches)
	}
}

func TestFullscreenSearchScrollKeysStillWorkWhileActive(t *testing.T) {
	lines := make([]string, 100)
	for index := range lines {
		lines[index] = "line-" + strconv.Itoa(index)
	}
	lines[50] = "needle"
	value := loadedFullscreenModel(t, strings.Join(lines, "\n"))
	value = pressKey(value, '/', "/")
	value = typeText(value, "needle")
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	offsetAfterJump := value.previewScrollOffset

	value = pressKey(value, 'k', "k")
	if value.previewScrollOffset == offsetAfterJump {
		t.Fatal("k did not scroll while a buffer search is active")
	}
}

func TestFullscreenSearchHelpEntriesPresent(t *testing.T) {
	value := loadedFullscreenModel(t, "live output\n")
	value.width, value.height, value.noColor = 120, 40, true
	lines := overlayLines(t, value)

	label := lineContaining(t, lines, "fullscreen preview")
	all := lineContaining(t, lines, "all keys")
	searchIndex := lineContaining(t, lines, "search buffer")
	if searchIndex < label || searchIndex > all {
		t.Fatalf("search buffer binding at line %d is outside the fullscreen context section (%d..%d):\n%s",
			searchIndex, label, all, strings.Join(lines, "\n"))
	}
	nextIndex := lineContaining(t, lines, "next / previous match")
	if nextIndex < label || nextIndex > all {
		t.Fatalf("next/previous match binding at line %d is outside the fullscreen context section (%d..%d):\n%s",
			nextIndex, label, all, strings.Join(lines, "\n"))
	}
}

func TestFullscreenSearchKeysetParityUnaffected(t *testing.T) {
	// A sanity guard specific to this feature: the / key it reuses (shared
	// with list search, disambiguated by altDescription per context) and the
	// new n/N binding must both show up in the plain (non-fullscreen)
	// overlay's keyset too, or TestHelpOverlayKeysetIsStableAcrossContexts
	// would already have caught a mismatch — this pins the human-readable
	// reason. The plain overlay describes / as "search" (the list search),
	// not "search buffer", since that context doesn't apply here.
	value := readyModel()
	value.width, value.height, value.noColor = 120, 40, true
	keys := overlayBindingKeys(t, value)
	if !slices.Contains(keys, "/") {
		t.Fatalf("plain overlay missing the / binding: %v", keys)
	}
	if !slices.Contains(keys, "n / N") {
		t.Fatalf("plain overlay missing the n / N binding: %v", keys)
	}
}
