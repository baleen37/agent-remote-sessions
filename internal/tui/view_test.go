package tui

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/output"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// TestPreviewPaneShortListStaysWithinTerminalHeight reproduces a bug seen on a
// real 140x35 terminal with the preview pane on, a short session list, host
// warnings, and compose active: joinPreview pads the body up to the
// bounded-layout budget, but the diagnostics budget was computed against the
// pre-pad body length, so the total line count exceeded value.height and the
// compose line and footer were pushed off screen.
func TestPreviewPaneShortListStaysWithinTerminalHeight(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	value.width = 140
	value.height = 35
	if !value.previewVisible() {
		t.Fatal("preview should be visible at 140 columns")
	}
	value.result.Warnings = []output.HostError{
		hostError("one", "warn", "first warning"),
		hostError("two", "warn", "second warning"),
		hostError("three", "warn", "third warning"),
	}
	value.refreshVisible()
	value.composing = true
	value.composeTarget = value.result.Sessions[0]
	value.compose = "hello"

	view := value.View()
	content := ansi.Strip(view.Content)
	if lines := strings.Count(content, "\n") + 1; lines > value.height {
		t.Fatalf("view height = %d, want <= %d:\n%s", lines, value.height, content)
	}
	if !strings.Contains(content, "send to "+sessionTitle(value.composeTarget)+": hello") {
		t.Fatalf("compose line missing from view:\n%s", content)
	}
	if !strings.Contains(content, "enter send") {
		t.Fatalf("footer missing from view:\n%s", content)
	}
}

// longSessionList returns enough attached (non-collapsing), distinctly
// grouped sessions to fill any reasonable terminal height, so bodyHeight
// consumes the entire body budget and diagnostics have no leftover space
// unless the layout explicitly reserves it for them.
func longSessionList(count int) []session.Session {
	sessions := make([]session.Session, 0, count)
	for index := range count {
		item := twoSessions()[1]
		item.NativeID = fmt.Sprintf("0195f5dc-9e3f-7c26-8000-%012d", index)
		item.Title = fmt.Sprintf("session %02d", index)
		item.CWD = fmt.Sprintf("/work/project-%02d", index)
		item.Runtime = session.Runtime{State: session.RuntimeAttached, AttachedClients: 1}
		sessions = append(sessions, item)
	}
	return sessions
}

// TestStatusLineSurvivesPreviewPaneWithLongList reproduces the starvation the
// preview-pane height fix exposed: joinPreview makes panelHeight consume the
// entire body budget, so diagnosticHeight (leftover after body) is always 0
// and the status line (which kill/send rely on for feedback) never renders
// while the preview pane is on, even though the terminal has 35 rows.
func TestStatusLineSurvivesPreviewPaneWithLongList(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	value.width = 140
	value.height = 35
	value.result.Sessions = longSessionList(40)
	value.refreshVisible()
	if !value.previewVisible() {
		t.Fatal("preview should be visible at 140 columns")
	}
	value.result.Warnings = []output.HostError{
		hostError("one", "warn", "first warning"),
		hostError("two", "warn", "second warning"),
	}
	value.status = "killing session 00 in 3s · u undo"
	value.refreshVisible()

	content := ansi.Strip(value.View().Content)
	if lines := strings.Count(content, "\n") + 1; lines > value.height {
		t.Fatalf("view height = %d, want <= %d:\n%s", lines, value.height, content)
	}
	if !strings.Contains(content, value.status) {
		t.Fatalf("status line missing from view:\n%s", content)
	}
	if _, ok := value.selectedRow(); !ok {
		t.Fatal("no selected row")
	}
}

// TestStatusLineSurvivesLongListWithoutPreview is the same starvation but
// without the preview pane: a long list alone already consumes the whole
// body budget pre-existing this fix, so the status line was never visible.
func TestStatusLineSurvivesLongListWithoutPreview(t *testing.T) {
	value := readyModel()
	value.width = 120
	value.height = 24
	value.result.Sessions = longSessionList(40)
	value.refreshVisible()
	value.status = "no live session to kill"

	content := ansi.Strip(value.View().Content)
	if lines := strings.Count(content, "\n") + 1; lines > value.height {
		t.Fatalf("view height = %d, want <= %d:\n%s", lines, value.height, content)
	}
	if !strings.Contains(content, value.status) {
		t.Fatalf("status line missing from view:\n%s", content)
	}
}

// TestStatusLineSurvivesExtremeStarvation forces a tiny terminal with several
// warnings and a status: the status line must still win the last kept
// diagnostics slot, the body must still render at least the selected row,
// and the frame must stay within height.
func TestStatusLineSurvivesExtremeStarvation(t *testing.T) {
	value := readyModel()
	value.width = 120
	value.height = 9
	value.result.Sessions = longSessionList(40)
	value.refreshVisible()
	value.result.Warnings = []output.HostError{
		hostError("one", "warn", "first warning"),
		hostError("two", "warn", "second warning"),
		hostError("three", "warn", "third warning"),
	}
	value.status = "sent to session 00"

	content := ansi.Strip(value.View().Content)
	if lines := strings.Count(content, "\n") + 1; lines > value.height {
		t.Fatalf("view height = %d, want <= %d:\n%s", lines, value.height, content)
	}
	if !strings.Contains(content, value.status) {
		t.Fatalf("status line missing from view under starvation:\n%s", content)
	}
	if _, ok := value.selectedRow(); !ok {
		t.Fatal("no selected row")
	}
}

// TestViewNeverExceedsHeightAcrossSweep is the height-axis counterpart to the
// width sweeps that already exist: PR #64's narrow-width regression survived
// 12 individual reviews because nothing checked every width, and the same
// structural gap exists on the height axis (agent-deck-followups Task A).
// It sweeps height 1..30 x width 50/80/120 x status present/absent x
// errors present/absent and asserts the rendered line count never exceeds
// height, and that the status line still wins its floor slot whenever the
// screen is tall enough to show anything past the fixed frame at all.
func TestViewNeverExceedsHeightAcrossSweep(t *testing.T) {
	for height := 1; height <= 30; height++ {
		for _, width := range []int{50, 80, 120} {
			for _, hasStatus := range []bool{false, true} {
				for _, hasErrors := range []bool{false, true} {
					value := readyModel()
					value.width = width
					value.height = height
					value.result.Sessions = longSessionList(40)
					value.refreshVisible()
					if hasStatus {
						value.status = "killing session 00 in 3s · u undo"
					}
					if hasErrors {
						value.result.Errors = []output.HostError{
							hostError("one", "failed", "first diagnostic"),
						}
					}

					content := ansi.Strip(value.View().Content)
					lines := strings.Count(content, "\n") + 1
					if lines > height {
						t.Fatalf("h=%d w=%d status=%v errors=%v: view rendered %d lines, want <= %d:\n%s",
							height, width, hasStatus, hasErrors, lines, height, content)
					}
					// Below height 3 the last-resort tail clamp (see View)
					// keeps only the footer, so the statusFloor contract
					// (PR #24) has nothing left to hold onto; from height 3
					// up it must still win its slot per that contract.
					if hasStatus && height >= 3 && !strings.Contains(content, value.status) {
						t.Fatalf("h=%d w=%d errors=%v: status line missing though height had room:\n%s",
							height, width, hasErrors, content)
					}
				}
			}
		}
	}
}

func TestSmallHeightKeepsSelectedRowFooterAndHelpVisible(t *testing.T) {
	model := readyModel()
	model.width = 120
	model.height = 10
	model.result.Sessions = nil
	for index := range 16 {
		item := twoSessions()[1]
		item.NativeID = fmt.Sprintf("0195f5dc-9e3f-7c26-8000-%012d", index)
		item.Title = fmt.Sprintf("session %02d", index)
		model.result.Sessions = append(model.result.Sessions, item)
	}
	model.result.Errors = []output.HostError{
		hostError("one", "failed", "first diagnostic"),
		hostError("two", "failed", "second diagnostic"),
	}
	model = openAllGroups(model)
	for range len(model.rows) - 2 {
		model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	}

	content := ansi.Strip(model.View().Content)
	for _, want := range []string{
		"> └─ ○  session 15",
		"0195f5dc-9e3f-7c26-8000-000000000015",
		"↑↓/jk move",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q: %q", want, content)
		}
	}
	if lines := strings.Count(content, "\n") + 1; lines > model.height {
		t.Fatalf("view height = %d, want <= %d:\n%s", lines, model.height, content)
	}
}

func TestSmallHeightBoundsMaximumLengthCWDDetails(t *testing.T) {
	model := readyModel()
	model.width = 48
	model.height = 10
	model.noColor = true
	item := twoSessions()[0]
	item.CWD = "/" + strings.Repeat("c", session.MaxCWDBytes-1)
	model.result.Sessions = []session.Session{item}
	model.refreshVisible()

	content := model.View().Content
	for _, want := range []string{
		"> ",
		"/cccccccc",
		"123e4567-e89b-42d3-a456-426614174000",
		"1d ago",
		"↑↓/jk move",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q: %q", want, content)
		}
	}
	if lines := strings.Count(content, "\n") + 1; lines > model.height {
		t.Fatalf("view height = %d, want <= %d:\n%s", lines, model.height, content)
	}
}

func TestViewRendersOneLineGroupsAndNeutralProviderLocation(t *testing.T) {
	model := readyModel()
	model.noColor = false
	model.width = 120
	model.height = 24
	model.result.Sessions[0].Host = "server"
	model = openAllGroups(model)
	model.move(-1)
	content := model.View().Content
	plain := ansi.Strip(content)
	for _, want := range []string{
		"ars", "1 attached", "1 idle", "▾ ars (1)", "▾ api (1)", "claude", "server",
		"attached(1)", "↑↓/jk move",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q: %q", want, plain)
		}
	}
	if strings.Count(plain, "connection check") != 1 {
		t.Fatalf("session row did not render exactly once: %q", plain)
	}
	lines := strings.Split(content, "\n")
	row := lines[lineContaining(t, strippedLines(lines), "connection check")]
	if missing := cellsWithoutBackground(row); len(missing) != ansi.StringWidth(row) {
		t.Fatalf("unselected row unexpectedly has background: %q", row)
	}
	for _, identity := range []string{"connection check", "[server]", "1d"} {
		assertSpanForeground(t, row, identity, false)
	}
	for _, state := range []string{"●", "attached(1)"} {
		assertSpanForeground(t, row, state, true)
		if styled := model.styles.attached.Render(state); !strings.Contains(row, styled) {
			t.Fatalf("state %q does not use attached style: %q", state, row)
		}
	}
	assertSpanForeground(t, row, "claude", true)
	if styled := model.styles.providerClaude.Render("claude"); !strings.Contains(row, styled) {
		t.Fatalf("provider %q does not use the claude coral style: %q", "claude", row)
	}
	if got := model.View(); !got.AltScreen {
		t.Fatal("View() did not request alternate screen")
	}
}

// TestViewKeepsBalancedVerticalRhythm covers the no-preview rhythm: header,
// pill bar, one blank, then the body starts immediately with no panel title
// (previewVisible is false without a Preview dependency).
func TestViewKeepsBalancedVerticalRhythm(t *testing.T) {
	value := readyModel()
	value = openAllGroups(value)
	value.width, value.height = 120, 24
	lines := strings.Split(ansi.Strip(value.View().Content), "\n")

	header := lineContaining(t, lines, "ars  ● 1 attached · ○ 1 idle")
	pillBar := lineContaining(t, lines, "[All]")
	firstHeader := lineContaining(t, lines, "▾ ars (1)")
	activeRow := lineContaining(t, lines, "attached(1)")
	secondHeader := lineContaining(t, lines, "▾ api (1)")
	recentRow := lineContaining(t, lines, "API repair")
	details := lineContaining(t, lines, "/work/ars")
	help := lineContaining(t, lines, "↑↓/jk move")

	if pillBar != header+1 {
		t.Fatalf("pill bar is not immediately below the header:\n%s", strings.Join(lines, "\n"))
	}
	for _, pair := range []struct {
		before int
		after  int
	}{
		{pillBar, firstHeader},
		{recentRow, details},
		{details, help},
	} {
		if pair.after != pair.before+2 || lines[pair.before+1] != "" {
			t.Fatalf("lines %d and %d are not separated by one blank line:\n%s", pair.before, pair.after, strings.Join(lines, "\n"))
		}
	}
	if secondHeader != activeRow+1 {
		t.Fatalf("groups are separated by a blank line:\n%s", strings.Join(lines, "\n"))
	}
}

// TestViewKeepsBalancedVerticalRhythmWithPreview is the split-view variant of
// the rhythm test: at 120 columns with a Preview dependency wired, the panel
// title (SESSIONS + underline) sits between the pill bar's blank line and the
// first group header, consuming two of the body's rows rather than adding
// extra rows on top of the fixed frame.
func TestViewKeepsBalancedVerticalRhythmWithPreview(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live output"), nil
	})
	value = openAllGroups(value)
	value.width, value.height = 120, 24
	if !value.previewVisible() {
		t.Fatal("preview should be visible at 120 columns")
	}
	lines := strings.Split(ansi.Strip(value.View().Content), "\n")

	pillBar := lineContaining(t, lines, "[All]")
	title := lineContaining(t, lines, "SESSIONS")
	underline := lineContaining(t, lines, "──────")
	firstHeader := lineContaining(t, lines, "▾ ars (1)")

	if title != pillBar+2 || lines[pillBar+1] != "" {
		t.Fatalf("panel title is not separated from the pill bar by one blank line:\n%s", strings.Join(lines, "\n"))
	}
	if underline != title+1 {
		t.Fatalf("panel title underline does not immediately follow the title:\n%s", strings.Join(lines, "\n"))
	}
	if firstHeader != underline+1 {
		t.Fatalf("first group header does not immediately follow the underline:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[title], "PREVIEW") {
		t.Fatalf("preview panel title missing from title row:\n%s", lines[title])
	}
}

func TestSecondaryUIUsesHierarchyStyles(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, false
	value.result.Warnings = []output.HostError{hostError("localhost", "partial", "metadata partial")}
	value.status = "attach finished"
	value.searching = true
	value.query = "API"
	value.refreshVisible()

	lines := strings.Split(value.View().Content, "\n")
	plain := strippedLines(lines)

	selected, ok := value.selectedSession()
	if !ok {
		t.Fatal("no selected session")
	}
	_, width := contentFrame(value.width)
	details := detailLines(selected, width, value.deps.Now())
	for _, text := range append(details, "metadata partial (partial)", "attach finished") {
		line := lines[lineContaining(t, plain, text)]
		want := " " + value.styles.muted.Render(text)
		if line != want {
			t.Fatalf("secondary UI hierarchy = %q, want muted %q", line, want)
		}
	}

	search := lines[lineContaining(t, plain, "/API")]
	wantSearch := " " + value.styles.selectedCursor.Render("/") + "API" + value.styles.muted.Render("   1/2")
	if search != wantSearch {
		t.Fatalf("active search hierarchy = %q, want %q", search, wantSearch)
	}
}

// TestFooterWithoutKeyTokenStaysFullyMuted covers the searching/composing
// hints (view.go's mutedFooterText path): they carry no key token to chip,
// so the whole line stays muted end to end, same as task 2 split the header
// case out of TestSecondaryUIUsesHierarchyStyles into its own test.
func TestFooterWithoutKeyTokenStaysFullyMuted(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, false
	value.searching = true
	value.refreshVisible()

	_, width := contentFrame(value.width)
	got := value.help(width)
	want := value.styles.muted.Render("type to filter   enter apply   esc cancel")
	if got != want {
		t.Fatalf("searching footer = %q, want fully muted %q", got, want)
	}
}

// TestFooterHelpUsesKeyChips covers the normal (non-searching,
// non-composing) footer hints: each hint's key token renders as a
// background chip and its description stays muted, per task 10's brief.
func TestFooterHelpUsesKeyChips(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 170, 24, false
	value.refreshVisible()

	_, width := contentFrame(value.width)
	got := value.help(width)
	wantMove := value.styles.keyChip.Render("↑↓/jk") + " " + value.styles.muted.Render("move")
	if !strings.Contains(got, wantMove) {
		t.Fatalf("footer help = %q, want to contain chip %q", got, wantMove)
	}
	wantSearch := value.styles.keyChip.Render("/") + " " + value.styles.muted.Render("search")
	if !strings.Contains(got, wantSearch) {
		t.Fatalf("footer help = %q, want to contain chip %q", got, wantSearch)
	}
	wantHelp := value.styles.keyChip.Render("?") + " " + value.styles.muted.Render("help")
	if !strings.Contains(got, wantHelp) {
		t.Fatalf("footer help = %q, want to contain chip %q", got, wantHelp)
	}
}

// TestFooterHelpWidthInvariantAcrossColor asserts the core width-invariance
// contract from task 10's brief: stripping ANSI from the colored footer
// render must equal the noColor render byte for byte, so key chips add SGR
// only and never widen the line (which would break the width-budget tests).
func TestFooterHelpWidthInvariantAcrossColor(t *testing.T) {
	for _, width := range []int{60, 75, 120, 140, 170} {
		colored := readyModel()
		colored.width, colored.height, colored.noColor = width, 24, false
		colored.refreshVisible()

		plain := readyModel()
		plain.width, plain.height, plain.noColor = width, 24, true
		plain.refreshVisible()

		_, contentWidth := contentFrame(width)
		coloredHelp := colored.help(contentWidth)
		plainHelp := plain.help(contentWidth)
		if ansi.Strip(coloredHelp) != plainHelp {
			t.Fatalf("width %d: ansi.Strip(colored help) = %q, want %q", width, ansi.Strip(coloredHelp), plainHelp)
		}
	}
}

func TestViewShowsSelectedCanonicalDetailsAndBoundedDiagnostics(t *testing.T) {
	model := readyModel()
	model.width = 120
	model.height = 24
	model.result.Errors = append(model.result.Errors, hostError("server", "ssh_failed", strings.Repeat("failed ", 100)))
	model.result.Warnings = append(model.result.Warnings, hostError("localhost", "corrupt", "Claude discovery partial"))
	model.status = strings.Repeat("status ", 100)

	content := ansi.Strip(model.View().Content)
	for _, want := range []string{
		"/work/ars", "123e4567-e89b-42d3-a456-426614174000", "1d ago",
		"✕ server: failed", "Claude discovery partial · unreadable session data skipped", "status",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q: %q", want, content)
		}
	}
	for _, line := range strings.Split(content, "\n") {
		if len(line) > 0 && ansi.StringWidth(line) > model.width {
			t.Fatalf("line width = %d, want <= %d: %q", ansi.StringWidth(line), model.width, line)
		}
	}
	warning := lineContaining(t, strippedLines(strings.Split(content, "\n")), "Claude discovery partial")
	if strings.HasPrefix(strings.TrimSpace(strings.Split(content, "\n")[warning]), "✕") {
		t.Fatalf("warning line should not carry the error prefix: %q", content)
	}
}

func TestDiagnosticLineExplainsKnownCodes(t *testing.T) {
	cases := []struct {
		name  string
		value output.HostError
		want  string
	}{
		{
			name:  "resource limit",
			value: hostError("localhost", "resource_limit", "Claude discovery partial"),
			want:  "Claude discovery partial · session limit reached, oldest hidden",
		},
		{
			name:  "incompatible",
			value: hostError("localhost", "incompatible", "Claude discovery partial"),
			want:  "Claude discovery partial · unrecognized session data skipped",
		},
		{
			name:  "corrupt",
			value: hostError("localhost", "corrupt", "Claude discovery partial"),
			want:  "Claude discovery partial · unreadable session data skipped",
		},
		{
			name:  "unavailable",
			value: hostError("localhost", "unavailable", "Claude discovery failed"),
			want:  "Claude discovery failed · some session files could not be read",
		},
		{
			name:  "remote host keeps its prefix",
			value: hostError("baleen@host", "corrupt", "Codex discovery partial"),
			want:  "baleen@host: Codex discovery partial · unreadable session data skipped",
		},
		{
			name:  "unknown code falls back to the raw code",
			value: hostError("localhost", "tmux_failed", "Runtime inspection failed"),
			want:  "Runtime inspection failed (tmux_failed)",
		},
		{
			name:  "unknown remote code falls back to the raw code",
			value: hostError("server", "ssh_failed", "SSH collection failed"),
			want:  "server: SSH collection failed (ssh_failed)",
		},
		{
			name:  "empty code keeps the message alone",
			value: hostError("localhost", "", "Claude discovery partial"),
			want:  "Claude discovery partial",
		},
		{
			name:  "empty remote code keeps the prefixed message alone",
			value: hostError("server", "", "SSH collection failed"),
			want:  "server: SSH collection failed",
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := diagnosticLine(item.value, "localhost"); got != item.want {
				t.Fatalf("diagnosticLine() = %q, want %q", got, item.want)
			}
		})
	}
}

func TestNarrowNoColorViewKeepsRequiredFields(t *testing.T) {
	model := readyModel()
	model.width = 60
	model.height = 12
	model.noColor = true
	content := model.View().Content
	if ansi.Strip(content) != content {
		t.Fatalf("NO_COLOR emitted ANSI: %q", content)
	}
	for _, want := range []string{"connection check", "attached", "1d"} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q: %q", want, content)
		}
	}
	for _, line := range strings.Split(content, "\n") {
		if ansi.StringWidth(line) > model.width {
			t.Fatalf("line width = %d, want <= %d: %q", ansi.StringWidth(line), model.width, line)
		}
	}
}

func TestViewHidesLocalhostPresentation(t *testing.T) {
	model := readyModel()
	model.width = 120
	model.height = 24
	model.result.Hosts = []output.HostResult{
		{Target: "localhost", Status: output.HostOK},
		{Target: "server", Status: output.HostOK},
	}
	model.result.Warnings = []output.HostError{{Host: "localhost", Code: "corrupt", Message: "Claude discovery partial"}}
	content := ansi.Strip(model.View().Content)
	row := selectedRow(content)
	if strings.Contains(row, "localhost") || strings.Contains(row, "  local  ") {
		t.Fatalf("local row exposes local target: %q", row)
	}
	if !strings.Contains(content, "1 peer") || strings.Contains(content, "localhost: Claude") {
		t.Fatalf("local presentation leaked: %q", content)
	}
	if !strings.Contains(content, "Claude discovery partial · unreadable session data skipped") {
		t.Fatalf("local diagnostic missing: %q", content)
	}
}

func TestNarrowRowKeepsLongTitleLocationRuntimeAndActivityVisible(t *testing.T) {
	model := readyModel()
	model.width = 60
	model.height = 12
	model.noColor = true
	item := twoSessions()[0]
	item.Host = "remote-host-" + strings.Repeat("a", session.MaxHostBytes-len("remote-host-"))
	item.Title = "critical-title-" + strings.Repeat("b", 200)
	model.result.Sessions = []session.Session{item}
	// Changing the host changes the session identity, so reset the stale
	// selection before rebuilding the rows.
	model.selected = 0
	model.selectedRef = rowRef{}
	model.refreshVisible()

	row := selectedRow(model.View().Content)
	for _, want := range []string{"critical-t", "remote-host", "attached(1)", "1d"} {
		if !strings.Contains(row, want) {
			t.Fatalf("row missing %q: %q", want, row)
		}
	}
	if width := ansi.StringWidth(row); width > model.width {
		t.Fatalf("row width = %d, want <= %d: %q", width, model.width, row)
	}
}

func TestNarrowViewRemovesOptionalColumnsInOrder(t *testing.T) {
	model := readyModel()
	model.noColor = true
	model.height = 24
	// The second fixture session's [server] location badge would otherwise
	// claim columns from the shared title/location budget and truncate the
	// active row's title before the width steps below get a chance to drop
	// provider and client count; this test is about column removal order, not
	// the badge, so keep every session local.
	model.result.Sessions[1].Host = "localhost"
	model.refreshVisible()

	model.width = 100
	wide := activeRow(model.View().Content)
	for _, want := range []string{"claude", "attached(1)"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("wide row missing %q: %q", want, wide)
		}
	}
	if strings.Contains(wide, " ars ") {
		t.Fatalf("session row still renders a project column: %q", wide)
	}

	model.width = 60
	withoutProvider := activeRow(model.View().Content)
	if strings.Contains(withoutProvider, "claude") || !strings.Contains(withoutProvider, "attached(1)") {
		t.Fatalf("provider was not removed second: %q", withoutProvider)
	}

	model.width = 50
	withoutClients := activeRow(model.View().Content)
	if strings.Contains(withoutClients, "attached(1)") || !strings.Contains(withoutClients, "attached") {
		t.Fatalf("client count was not removed third: %q", withoutClients)
	}
}

func TestNewModelHonorsNoColorEnvironment(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	model := newModel(context.Background(), Dependencies{Collect: staticCollect(Result{})})
	if !model.noColor {
		t.Fatal("NO_COLOR environment was ignored")
	}
}

func TestRunRejectsInvalidDependencies(t *testing.T) {
	var output bytes.Buffer
	if err := Run(context.Background(), Dependencies{}, strings.NewReader(""), &output); err == nil || err.Error() != "invalid TUI dependencies" {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestNoColorPreservesSelectionAndStateWithoutANSI(t *testing.T) {
	value := readyModel()
	value = openAllGroups(value)
	value.width, value.height, value.noColor = 120, 24, true
	content := value.View().Content
	if ansi.Strip(content) != content {
		t.Fatalf("NO_COLOR emitted ANSI: %q", content)
	}
	for _, want := range []string{"> └─ ●", "attached(1)", "▾ api (1)", "○"} {
		if !strings.Contains(content, want) {
			t.Fatalf("NO_COLOR missing %q: %q", want, content)
		}
	}
}

func TestViewGroupsSessionsUnderProjectHeaders(t *testing.T) {
	value := readyModel()
	value = openAllGroups(value)
	value.width, value.height, value.noColor = 120, 24, true
	plain := ansi.Strip(value.View().Content)
	arsAt := strings.Index(plain, "▾ ars (1)")
	apiAt := strings.Index(plain, "▾ api (1)")
	if arsAt == -1 || apiAt == -1 || arsAt > apiAt {
		t.Fatalf("headers missing or misordered:\n%s", plain)
	}
	if !strings.Contains(plain, "└─ ●  connection check") {
		t.Fatalf("missing tree guide session row:\n%s", plain)
	}
	if strings.Contains(plain, "Active") || strings.Contains(plain, "Recent") {
		t.Fatalf("legacy headings remain:\n%s", plain)
	}
}

func TestViewCollapsedHeaderShowsCountAndActiveMarker(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, true
	value.toggle("ars")
	plain := ansi.Strip(value.View().Content)
	if !strings.Contains(plain, "▸ ars (1) ●") {
		t.Fatalf("collapsed header missing marker:\n%s", plain)
	}
	if strings.Contains(plain, "connection check") {
		t.Fatalf("collapsed session still rendered:\n%s", plain)
	}
}

func TestViewTreeGuidesMarkLastSession(t *testing.T) {
	value := readyModel()
	items := twoSessions()
	items[1].CWD = items[0].CWD
	items[1].Host = items[0].Host
	value.result.Sessions = items
	value.width, value.height, value.noColor = 120, 24, true
	value = openAllGroups(value)
	plain := ansi.Strip(value.View().Content)
	if !strings.Contains(plain, "├─ ●  connection check") ||
		!strings.Contains(plain, "└─ ○  API repair") {
		t.Fatalf("guides wrong:\n%s", plain)
	}
}

func TestViewRendersMoreRowForAutoPartialGroups(t *testing.T) {
	value := readyModel()
	value.result.Sessions = mixedProjectSessions()
	value.width, value.height, value.noColor = 120, 24, true
	value.refreshVisible()
	plain := ansi.Strip(value.View().Content)
	if !strings.Contains(plain, "▾ ars (2)") ||
		!strings.Contains(plain, "├─ ●  connection check") ||
		!strings.Contains(plain, "└─ … 1 more") {
		t.Fatalf("auto partial group rows wrong:\n%s", plain)
	}
	if strings.Contains(plain, "API repair") {
		t.Fatalf("recent session leaked into partial group:\n%s", plain)
	}

	value.noColor = false
	if raw := value.View().Content; !strings.Contains(raw, value.styles.muted.Render("… 1 more")) {
		t.Fatalf("more row is not muted: %q", raw)
	}
}

func TestViewLinesStayWithinWidthWithTree(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 46, 12, true
	for _, line := range strings.Split(value.View().Content, "\n") {
		if ansi.StringWidth(line) > value.width {
			t.Fatalf("line exceeds width %d: %q", value.width, line)
		}
	}
}

func TestViewUntitledFallbackUsesShortID(t *testing.T) {
	value := readyModel()
	items := twoSessions()
	items[0].Title = ""
	value.result.Sessions = items
	value.width, value.height, value.noColor = 120, 24, true
	value.refreshVisible()
	row := selectedRow(value.View().Content)
	if !strings.Contains(row, "123e4567") {
		t.Fatalf("missing short id fallback: %q", row)
	}
	if strings.Contains(row, " · ") {
		t.Fatalf("fallback still includes project: %q", row)
	}
}

func openAllGroups(value model) model {
	if value.groupMode == nil {
		value.groupMode = make(map[string]groupMode)
	}
	for _, item := range value.result.Sessions {
		value.groupMode[session.Project(item.CWD)] = groupModeOpen
	}
	value.refreshVisible()
	return value
}

func activeRow(content string) string {
	lines := strings.Split(ansi.Strip(content), "\n")
	for _, line := range lines {
		if strings.Contains(line, "connection check") && strings.Contains(line, "attached") {
			return line
		}
	}
	return ""
}

func selectedRow(content string) string {
	for _, line := range strings.Split(ansi.Strip(content), "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " "), "> ") {
			return line
		}
	}
	return ""
}

func strippedLines(lines []string) []string {
	plain := make([]string, len(lines))
	for index, line := range lines {
		plain[index] = ansi.Strip(line)
	}
	return plain
}

func lineContaining(t *testing.T, lines []string, want string) int {
	t.Helper()
	for index, line := range lines {
		if strings.Contains(line, want) {
			return index
		}
	}
	t.Fatalf("missing line containing %q:\n%s", want, strings.Join(lines, "\n"))
	return -1
}

func assertSpanForeground(t *testing.T, line, text string, want bool) {
	t.Helper()
	plain := ansi.Strip(line)
	index := strings.Index(plain, text)
	if index < 0 {
		t.Fatalf("line is missing span %q: %q", text, plain)
	}
	start := ansi.StringWidth(plain[:index])
	width := ansi.StringWidth(text)
	foreground := foregroundCells(line)
	if start+width > len(foreground) {
		t.Fatalf("foreground cells = %d, span %q ends at %d: %q", len(foreground), text, start+width, line)
	}
	for cell := start; cell < start+width; cell++ {
		if foreground[cell] != want {
			t.Fatalf("span %q cell %d foreground = %t, want %t: %q", text, cell-start, foreground[cell], want, line)
		}
	}
}

func foregroundCells(line string) []bool {
	styled := false
	var cells []bool
	parser := ansi.NewParser()
	parser.SetHandler(ansi.Handler{
		Print: func(character rune) {
			for range ansi.StringWidth(string(character)) {
				cells = append(cells, styled)
			}
		},
		HandleCsi: func(command ansi.Cmd, params ansi.Params) {
			if command.Final() != 'm' {
				return
			}
			if len(params) == 0 {
				styled = false
				return
			}
			params.ForEach(0, func(_ int, parameter int, _ bool) {
				switch {
				case parameter == 0 || parameter == 39:
					styled = false
				case parameter == 38,
					parameter >= 30 && parameter <= 37,
					parameter >= 90 && parameter <= 97:
					styled = true
				}
			})
		},
	})
	parser.Parse([]byte(line))
	return cells
}

func TestViewShowsMatchCountWhileFiltering(t *testing.T) {
	model := readyModel()
	model.width = 120
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: '/'}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "API"}))
	content := ansi.Strip(model.View().Content)
	if !strings.Contains(content, "1/2") {
		t.Fatalf("filtering view missing match count: %q", content)
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	content = ansi.Strip(model.View().Content)
	if !strings.Contains(content, "1/2") {
		t.Fatalf("committed filter view missing match count: %q", content)
	}
}

func TestViewExplainsEmptyFilterAndEmptyInventory(t *testing.T) {
	model := readyModel()
	model.width = 120
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: '/'}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "zzz"}))
	content := ansi.Strip(model.View().Content)
	if !strings.Contains(content, `no matches for "zzz"`) || !strings.Contains(content, "esc") {
		t.Fatalf("empty filter view missing guidance: %q", content)
	}

	model = readyModel()
	model.width = 120
	model.result.Sessions = nil
	model.refreshVisible()
	content = ansi.Strip(model.View().Content)
	if !strings.Contains(content, "no sessions yet") {
		t.Fatalf("empty inventory view missing message: %q", content)
	}
	if !strings.Contains(content, "ars remote add <host>") {
		t.Fatalf("empty inventory view missing next-action hint: %q", content)
	}
}

func TestViewShowsHumanizedTimestampInDetails(t *testing.T) {
	model := readyModel()
	model.width, model.height, model.noColor = 120, 24, true
	content := ansi.Strip(model.View().Content)
	if !strings.Contains(content, "1d ago") {
		t.Fatalf("detail line missing humanized timestamp: %q", content)
	}
	if strings.Contains(content, "2026-07-19T12:00:00Z") {
		t.Fatalf("detail line still shows raw RFC3339 timestamp: %q", content)
	}
}

func TestStateSymbolMapsRuntimeStates(t *testing.T) {
	for _, testCase := range []struct {
		state session.RuntimeState
		want  string
	}{
		{session.RuntimeAttached, "●"},
		{session.RuntimeRunning, "◐"},
		{session.RuntimeSaved, "○"},
	} {
		if got := stateSymbol(testCase.state); got != testCase.want {
			t.Fatalf("stateSymbol(%q) = %q, want %q", testCase.state, got, testCase.want)
		}
	}
}

func TestHelpOverlayListsBindingsIncludingDetach(t *testing.T) {
	model := readyModel()
	model.width, model.height, model.noColor = 120, 24, true
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: '?', Text: "?"}))
	content := ansi.Strip(model.View().Content)
	for _, want := range []string{
		"↑↓ / jk", "h / l", "g / G", "PgUp / PgDn", "Ctrl+U / Ctrl+D",
		"search", "enter", "space", "refresh", "quit",
		"Ctrl+Q", "detach",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("help overlay missing %q: %q", want, content)
		}
	}
}

func TestHeaderShowsFilterIndicatorWhenActive(t *testing.T) {
	model := readyModel()
	model.width, model.noColor = 120, true
	content := ansi.Strip(model.View().Content)
	if strings.Contains(content, "[● 1]") || strings.Contains(content, "[○ 1]") {
		t.Fatalf("pill bar shows an active pill with no filter active: %q", content)
	}
	if !strings.Contains(content, "[All]") {
		t.Fatalf("pill bar missing active All pill with no filter active: %q", content)
	}

	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Text: "!"}))
	content = ansi.Strip(model.View().Content)
	if !strings.Contains(content, "[● 1]") {
		t.Fatalf("pill bar missing active pill for attached: %q", content)
	}

	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Text: "#"}))
	content = ansi.Strip(model.View().Content)
	if !strings.Contains(content, "[● 1]") || !strings.Contains(content, "[○ 1]") {
		t.Fatalf("pill bar missing combined active pills: %q", content)
	}
}

func TestEmptyFilterResultNamesActiveFilters(t *testing.T) {
	tests := []struct {
		name          string
		stateFilter   map[session.RuntimeState]bool
		waitingFilter bool
		want          string
	}{
		{
			name:        "single state filter: attached",
			stateFilter: map[session.RuntimeState]bool{session.RuntimeAttached: true},
			want:        "no attached sessions · esc to clear",
		},
		{
			name:        "single state filter: running",
			stateFilter: map[session.RuntimeState]bool{session.RuntimeRunning: true},
			want:        "no running sessions · esc to clear",
		},
		{
			name:        "single state filter: saved",
			stateFilter: map[session.RuntimeState]bool{session.RuntimeSaved: true},
			want:        "no saved sessions · esc to clear",
		},
		{
			name: "multiple state filters joined",
			stateFilter: map[session.RuntimeState]bool{
				session.RuntimeAttached: true,
				session.RuntimeRunning:  true,
			},
			want: "no attached / running sessions · esc to clear",
		},
		{
			name:          "needs-input filter alone",
			waitingFilter: true,
			want:          "no sessions need input · esc to clear",
		},
		{
			name:          "needs-input combined with a state filter",
			stateFilter:   map[session.RuntimeState]bool{session.RuntimeAttached: true},
			waitingFilter: true,
			want:          "no attached / needs-input sessions · esc to clear",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := readyModel()
			model.width = 120
			model.result.Sessions = nil
			model.stateFilter = test.stateFilter
			model.waitingFilter = test.waitingFilter
			model.refreshVisible()
			if len(model.rows) != 0 {
				t.Fatalf("rows with active filter = %+v, want none", model.rows)
			}
			content := ansi.Strip(model.View().Content)
			if !strings.Contains(content, test.want) {
				t.Fatalf("empty filter view missing guidance %q: %q", test.want, content)
			}
		})
	}
}

func TestEmptyFilterResultKeepsSearchMessageUnchanged(t *testing.T) {
	model := readyModel()
	model.width = 120
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Text: "/"}))
	model.query = "nonexistent-query"
	model.refreshVisible()
	content := ansi.Strip(model.View().Content)
	if !strings.Contains(content, `no matches for "nonexistent-query" · esc to clear`) {
		t.Fatalf("empty search view missing guidance: %q", content)
	}
}

func TestHelpOverlayShowsStateSymbolLegend(t *testing.T) {
	model := readyModel()
	model.width, model.height, model.noColor = 120, 24, true
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: '?', Text: "?"}))
	content := ansi.Strip(model.View().Content)
	if !strings.Contains(content, "● attached · ◐ running · ? needs input · ○ idle") {
		t.Fatalf("help overlay missing state symbol legend: %q", content)
	}
}

func TestHelpOverlayFitsNarrowTerminal(t *testing.T) {
	model := readyModel()
	model.width, model.height, model.noColor = 40, 20, true
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: '?', Text: "?"}))
	content := model.View().Content
	for _, line := range strings.Split(content, "\n") {
		if ansi.StringWidth(line) > model.width {
			t.Fatalf("help overlay line exceeds width %d: %q", model.width, line)
		}
	}
}

func TestFooterShowsEscClearWhenFilterActiveWithoutQuery(t *testing.T) {
	model := readyModel()
	model.width = 120
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Text: "!"}))
	content := ansi.Strip(model.View().Content)
	if !strings.Contains(content, "esc clear") {
		t.Fatalf("footer missing esc clear hint while filter active: %q", content)
	}
}

func TestHelpOverlayAndFooterAdvertiseGroupJump(t *testing.T) {
	model := readyModel()
	model.width = 120
	content := ansi.Strip(model.help(model.contentWidth()))
	if !strings.Contains(content, "1-9 group") {
		t.Fatalf("footer help missing group jump hint: %q", content)
	}

	model.showHelp = true
	overlay := ansi.Strip(model.View().Content)
	if !strings.Contains(overlay, "jump to group") {
		t.Fatalf("help overlay missing group jump binding:\n%s", overlay)
	}
}

func TestFooterHelpIncludesHelpHint(t *testing.T) {
	model := readyModel()
	model.width = 120
	content := ansi.Strip(model.View().Content)
	if !strings.Contains(content, "? help") {
		t.Fatalf("footer help missing ? help hint: %q", content)
	}
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "? help") && strings.Contains(line, "…") {
			t.Fatalf("footer line truncated instead of dropping lower priority hints: %q", line)
		}
	}
}

func TestFooterAtWideWidthShowsAllHints(t *testing.T) {
	model := readyModel()
	model.width = 170
	content := ansi.Strip(model.View().Content)
	for _, want := range []string{"!@#$ filter", "1-9 group", "g/G top/end", "h/l fold", "a older", "? help"} {
		if !strings.Contains(content, want) {
			t.Fatalf("wide footer missing %q: %q", want, content)
		}
	}
}

// TestFooterAtCommonWidthDropsLowPriorityHintsBeforeHighPriorityOnes is the
// measured contract update task 13's brief calls for: at width 120 the
// 3-space separator no longer fits every hint plus "p preview" (task 6
// added it to help()'s item list without a droppable entry), so
// fitFooterItems now drops "!@#$ filter" under 3-space spacing. That drop
// itself makes the 2-space rendering keep strictly more hints for the same
// width, so help()'s separator-choice fallback (view.go) picks 2-space
// instead — "!@#$ filter" survives here, spaced tighter, rather than being
// dropped.
func TestFooterAtCommonWidthDropsLowPriorityHintsBeforeHighPriorityOnes(t *testing.T) {
	model := readyModel()
	model.width = 120
	content := ansi.Strip(model.View().Content)
	if !strings.Contains(content, "!@#$ filter") {
		t.Fatalf("footer at width 120 should keep !@#$ filter once the 2-space fallback recovers it: %q", content)
	}
	for _, want := range []string{"? help", "q quit", "r refresh", "enter attach", "/ search", "↑↓/jk move"} {
		if !strings.Contains(content, want) {
			t.Fatalf("footer at width 120 missing high priority hint %q: %q", want, content)
		}
	}
}

// TestFooterWithPreviewVisibleAtWideWidthDropsOnlyLowestPriorityHint is the
// measured contract update task 13's brief calls for. Previously, with the
// preview open, "f full" and "</> resize" added roughly 20 columns which
// pushed both "g/G top/end" and "P pin" past the 3-space separator's
// droppable boundary at width 170. Now that a 3-space drop makes the
// 2-space rendering strictly wider-fitting, help()'s separator fallback
// switches to 2-space, which only needs to drop the single lowest-priority
// droppable hint ("g/G top/end") to fit — everything else, including "P
// pin" and "p preview" (added to help()'s item list since), survives.
func TestFooterWithPreviewVisibleAtWideWidthDropsOnlyLowestPriorityHint(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live"), nil
	})
	value.width = 170
	content := ansi.Strip(value.View().Content)
	for _, want := range []string{
		"f full", "</> resize", "!@#$ filter", "1-9 group", "a older", "? help",
		"P pin", "p preview",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("wide footer with preview missing %q: %q", want, content)
		}
	}
	if strings.Contains(content, "g/G top/end") {
		t.Fatalf("wide footer with preview should drop g/G top/end to fit: %q", content)
	}
}

// TestFooterWithPreviewVisibleAtCommonWidthShowsResizeHint locks in that at
// the common 120 width, "f full" and "</> resize" still fit — they only
// compete with hints readyModel's no-preview contract already drops at 120.
func TestFooterWithPreviewVisibleAtCommonWidthShowsResizeHint(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live"), nil
	})
	value.width = 120
	content := ansi.Strip(value.View().Content)
	for _, want := range []string{"f full", "</> resize", "? help", "q quit", "enter attach"} {
		if !strings.Contains(content, want) {
			t.Fatalf("footer with preview at width 120 missing %q: %q", want, content)
		}
	}
	if strings.Contains(content, "!@#$ filter") {
		t.Fatalf("footer with preview at width 120 should drop !@#$ filter: %q", content)
	}
}

func TestHeaderShowsSpinnerFrameWhileCollecting(t *testing.T) {
	model := readyModel()
	model.width, model.noColor = 120, true
	model.collecting = true
	model.spinner = 0
	content := ansi.Strip(model.View().Content)
	if !strings.Contains(content, spinnerFrames[0]) {
		t.Fatalf("header missing spinner frame while collecting: %q", content)
	}
	if !strings.Contains(content, "refreshing") {
		t.Fatalf("header missing generic refreshing text: %q", content)
	}
}

func TestHelpAdaptsToSelectionSearchAndQuery(t *testing.T) {
	model := readyModel()
	model.width = 120

	content := ansi.Strip(model.View().Content)
	if !strings.Contains(content, "enter attach") {
		t.Fatalf("session help missing attach: %q", content)
	}

	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'k', Text: "k"}))
	content = ansi.Strip(model.View().Content)
	if !strings.Contains(content, "enter toggle") || strings.Contains(content, "enter attach") {
		t.Fatalf("header help missing toggle: %q", content)
	}

	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: '/'}))
	content = ansi.Strip(model.View().Content)
	if !strings.Contains(content, "enter apply") || !strings.Contains(content, "esc cancel") {
		t.Fatalf("search help missing apply/cancel: %q", content)
	}

	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "API"}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	content = ansi.Strip(model.View().Content)
	if !strings.Contains(content, "esc clear") {
		t.Fatalf("committed query help missing clear hint: %q", content)
	}
}

func TestHelpShowsFoldHintOnWideTerminals(t *testing.T) {
	model := readyModel()
	model.width = 120
	content := ansi.Strip(model.View().Content)
	if !strings.Contains(content, "h/l fold") {
		t.Fatalf("wide help missing fold hint: %q", content)
	}
}

func TestFilteredRowsKeepStableColumnLayout(t *testing.T) {
	model := readyModel()
	model.width = 120
	providerStart := func(content string) int {
		for _, line := range strings.Split(content, "\n") {
			if strings.Contains(line, "connection check") {
				return strings.Index(line, "claude")
			}
		}
		return -1
	}
	unfiltered := providerStart(ansi.Strip(model.View().Content))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: '/'}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "connection"}))
	filtered := providerStart(ansi.Strip(model.View().Content))
	if unfiltered < 0 || unfiltered != filtered {
		t.Fatalf("provider column moved while filtering: %d -> %d", unfiltered, filtered)
	}
}

func TestHeaderCountsPeersWithGrammar(t *testing.T) {
	model := readyModel()
	model.width = 120
	model.result.Hosts = []output.HostResult{
		{Target: "localhost", Status: output.HostOK},
		{Target: "server", Status: output.HostOK},
	}
	content := ansi.Strip(model.View().Content)
	if !strings.Contains(content, "1 peer") || strings.Contains(content, "1 peers") {
		t.Fatalf("header peer grammar: %q", content)
	}

	model.result.Hosts = []output.HostResult{{Target: "localhost", Status: output.HostOK}}
	content = ansi.Strip(model.View().Content)
	if strings.Contains(content, "peer") || strings.Contains(content, "hosts") {
		t.Fatalf("header shows peer count with no peers: %q", content)
	}
}

func TestHelpOffersExpandOnMoreRow(t *testing.T) {
	model := readyModel()
	model.width = 120
	active := twoSessions()[0]
	saved := twoSessions()[0]
	saved.NativeID = "223e4567-e89b-42d3-a456-426614174000"
	saved.Runtime = session.Runtime{State: session.RuntimeSaved}
	model.result.Sessions = []session.Session{active, saved}
	model.refreshVisible()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	if row, ok := model.selectedRow(); !ok || row.kind != rowMore {
		t.Fatalf("selection is not the more row: %+v", row)
	}
	content := ansi.Strip(model.View().Content)
	if !strings.Contains(content, "enter expand") || strings.Contains(content, "enter attach") {
		t.Fatalf("more-row help missing expand: %q", content)
	}
}

func TestHeaderUsesOnlyGenericRefreshingCopy(t *testing.T) {
	model := readyModel()
	model.width, model.noColor = 120, true
	model.collecting = true
	model.loading = []string{"localhost", "server", "cached", "recent-first", "complete"}
	header := strings.Split(ansi.Strip(model.View().Content), "\n")[0]
	if !strings.Contains(header, "refreshing") {
		t.Fatalf("header missing generic refreshing copy: %q", header)
	}
	for _, forbidden := range []string{
		"cached", "cache", "recent-first", "refreshing recent",
		"finishing history", "exhaustive", "complete",
		"loading localhost", "loading server",
	} {
		if strings.Contains(header, forbidden) {
			t.Fatalf("header exposed %q: %q", forbidden, header)
		}
	}
}

func TestActiveInitialEmptyShowsGenericLoadingSessions(t *testing.T) {
	model := readyModel()
	model.width, model.noColor = 120, true
	model.result = Result{}
	model.refreshVisible()
	model.collecting = true
	content := ansi.Strip(model.View().Content)
	if !strings.Contains(content, "loading sessions…") {
		t.Fatalf("active empty view missing loading copy: %q", content)
	}
	if strings.Contains(content, "no sessions yet") {
		t.Fatalf("active empty view showed completed guidance: %q", content)
	}
}

func TestCompletedHealthyEmptyGuidanceUnchanged(t *testing.T) {
	model := readyModel()
	model.width, model.noColor = 120, true
	model.result = Result{}
	model.refreshVisible()
	model.collecting = false
	content := ansi.Strip(model.View().Content)
	for _, want := range []string{
		"no sessions yet",
		"start a claude/codex session, or add a remote with: ars remote add <host>",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("completed empty view missing %q: %q", want, content)
		}
	}
	if strings.Contains(content, "loading sessions…") {
		t.Fatalf("completed empty view kept loading copy: %q", content)
	}
}

// footerLine extracts the last non-blank rendered line, which is where the
// footer help lands regardless of layout (hidden, stacked, or dual preview).
func footerLine(content string) string {
	lines := strings.Split(content, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}

// TestFooterQuitAndHelpSurviveWidthSweep is the regression test for the
// narrow-width bug found in the final integration review of the agent-deck
// series (task 13): "q quit" and "? help" disappeared from the footer at
// widths 40-73 and 77-79 because "p preview" had no droppable entry in
// fitFooterItems and the 3-space separator (>= width 75) had no fallback to
// the narrower 2-space one. Sweeping every width, rather than spot-checking
// a couple of fixed values, is what the earlier per-task reviews lacked —
// each individually-reasonable footer addition passed review but the
// combination silently broke a range no single review width happened to hit.
//
// Below footerGuaranteedWidth (63) the never-dropped hints alone ("↑↓/jk
// move", "/ search", "enter attach", "r refresh", "q quit", "? help") no
// longer fit even with every droppable hint removed and the 2-space
// separator in play; that floor is measured, not assumed, and is a
// pre-existing limitation this task does not attempt to fix (see the
// constant's doc comment in view.go).
func TestFooterQuitAndHelpSurviveWidthSweep(t *testing.T) {
	check := func(t *testing.T, label string, build func(width int) model) {
		t.Helper()
		prevCount := -1
		for width := footerGuaranteedWidth; width <= 200; width++ {
			value := build(width)
			footer := footerLine(ansi.Strip(value.View().Content))
			if !strings.Contains(footer, "q quit") {
				t.Fatalf("%s: width %d missing \"q quit\": %q", label, width, footer)
			}
			if !strings.Contains(footer, "? help") {
				t.Fatalf("%s: width %d missing \"? help\": %q", label, width, footer)
			}
			n := 0
			for _, hint := range []string{
				"↑↓/jk move", "h/l fold", "g/G top/end", "1-9 group", "!@#$ filter",
				"a older", "x kill", "m msg", "P pin", "/ search", "esc clear",
				"p preview", "p preview off", "f full", "</> resize",
				"enter attach", "enter toggle", "enter expand", "r refresh", "q quit", "? help",
			} {
				if strings.Contains(footer, hint) {
					n++
				}
			}
			if prevCount != -1 && n < prevCount {
				t.Fatalf("%s: hint count dropped non-monotonically at width %d (%d -> %d): %q", label, width, prevCount, n, footer)
			}
			prevCount = n
		}
	}

	t.Run("no preview", func(t *testing.T) {
		check(t, "no preview", func(width int) model {
			value := readyModel()
			value.height = 40
			value.width = width
			return value
		})
	})

	t.Run("preview visible", func(t *testing.T) {
		check(t, "preview visible", func(width int) model {
			value := previewModel(func(context.Context, session.Session) ([]byte, error) {
				return []byte("live"), nil
			})
			value.height = 30
			value.width = width
			return value
		})
	})
}

// TestFooterBelowGuaranteedWidthIsAPreexistingLimitation documents, rather
// than asserts a fix for, the sub-footerGuaranteedWidth range: the
// never-dropped hints alone cannot fit, so "q quit"/"? help" can still be
// truncated there. This is intentionally not covered by the sweep above —
// task 13's brief calls for naming the floor, not for making every width
// down to the terminal minimum work.
func TestFooterBelowGuaranteedWidthIsAPreexistingLimitation(t *testing.T) {
	value := readyModel()
	value.height = 40
	value.width = footerGuaranteedWidth - 1
	footer := footerLine(ansi.Strip(value.View().Content))
	if strings.Contains(footer, "q quit") && strings.Contains(footer, "? help") {
		t.Fatalf("width %d unexpectedly fits both tail hints; footerGuaranteedWidth could be lowered: %q", footerGuaranteedWidth-1, footer)
	}
}

func TestActiveQueryAndFilterEmptyGuidanceUnchanged(t *testing.T) {
	t.Run("query", func(t *testing.T) {
		model := readyModel()
		model.width, model.noColor = 120, true
		model.collecting = true
		model.query = "missing"
		model.refreshVisible()
		content := ansi.Strip(model.View().Content)
		if !strings.Contains(content, `no matches for "missing" · esc to clear`) ||
			strings.Contains(content, "loading sessions…") {
			t.Fatalf("active query empty guidance: %q", content)
		}
	})

	t.Run("filter", func(t *testing.T) {
		model := readyModel()
		model.width, model.noColor = 120, true
		model.collecting = true
		model.stateFilter = map[session.RuntimeState]bool{session.RuntimeSaved: true}
		model.result = Result{Sessions: []session.Session{twoSessions()[0]}}
		model.refreshVisible()
		content := ansi.Strip(model.View().Content)
		if !strings.Contains(content, "no saved sessions · esc to clear") ||
			strings.Contains(content, "loading sessions…") {
			t.Fatalf("active filter empty guidance: %q", content)
		}
	})
}
