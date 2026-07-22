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
	model.refreshVisible()
	for range len(model.rows) - 2 {
		model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	}

	content := ansi.Strip(model.View().Content)
	for _, want := range []string{
		"> └─ ∙  session 15",
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
	model.height = 9
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
		"2026-07-19T12:00:00Z",
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
	model.refreshVisible()
	model.move(-1)
	content := model.View().Content
	plain := ansi.Strip(content)
	for _, want := range []string{
		"ars", "1 active", "1 recent", "▾ ars (1)", "▾ api (1)", "claude", "server",
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
	for _, identity := range []string{"connection check", "claude", "server", "1d"} {
		assertSpanForeground(t, row, identity, false)
	}
	for _, state := range []string{"✻", "attached(1)"} {
		assertSpanForeground(t, row, state, true)
		if styled := model.styles.attached.Render(state); !strings.Contains(row, styled) {
			t.Fatalf("state %q does not use attached style: %q", state, row)
		}
	}
	if got := model.View(); !got.AltScreen {
		t.Fatal("View() did not request alternate screen")
	}
}

func TestViewKeepsBalancedVerticalRhythm(t *testing.T) {
	value := readyModel()
	value.width, value.height = 120, 24
	lines := strings.Split(ansi.Strip(value.View().Content), "\n")

	header := lineContaining(t, lines, "ars  1 active · 1 recent")
	firstHeader := lineContaining(t, lines, "▾ ars (1)")
	activeRow := lineContaining(t, lines, "attached(1)")
	secondHeader := lineContaining(t, lines, "▾ api (1)")
	recentRow := lineContaining(t, lines, "API repair")
	details := lineContaining(t, lines, "/work/ars")
	help := lineContaining(t, lines, "↑↓/jk move")

	for _, pair := range []struct {
		before int
		after  int
	}{
		{header, firstHeader},
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
	header := lines[lineContaining(t, plain, "ars  1 active · 1 recent")]
	wantHeader := " " + value.styles.title.Render("ars") + value.styles.muted.Render("  1 active · 1 recent")
	if header != wantHeader {
		t.Fatalf("header hierarchy = %q, want %q", header, wantHeader)
	}

	selected, ok := value.selectedSession()
	if !ok {
		t.Fatal("no selected session")
	}
	_, width := contentFrame(value.width)
	details := detailLines(selected, width)
	for _, text := range append(details, "metadata partial (partial)", "attach finished", value.help(width)) {
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

func TestViewShowsSelectedCanonicalDetailsAndBoundedDiagnostics(t *testing.T) {
	model := readyModel()
	model.width = 120
	model.height = 24
	model.result.Errors = append(model.result.Errors, hostError("server", "ssh_failed", strings.Repeat("failed ", 100)))
	model.result.Warnings = append(model.result.Warnings, hostError("localhost", "corrupt", "Claude discovery partial"))
	model.status = strings.Repeat("status ", 100)

	content := ansi.Strip(model.View().Content)
	for _, want := range []string{
		"/work/ars", "123e4567-e89b-42d3-a456-426614174000", "2026-07-19T12:00:00Z",
		"server: failed", "Claude discovery partial (corrupt)", "status",
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
	if !strings.Contains(content, "Claude discovery partial (corrupt)") {
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
	model.refreshVisible()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))

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
	value.width, value.height, value.noColor = 120, 24, true
	content := value.View().Content
	if ansi.Strip(content) != content {
		t.Fatalf("NO_COLOR emitted ANSI: %q", content)
	}
	for _, want := range []string{"> └─ ✻", "attached(1)", "▾ api (1)", "∙"} {
		if !strings.Contains(content, want) {
			t.Fatalf("NO_COLOR missing %q: %q", want, content)
		}
	}
}

func TestViewGroupsSessionsUnderProjectHeaders(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, true
	plain := ansi.Strip(value.View().Content)
	arsAt := strings.Index(plain, "▾ ars (1)")
	apiAt := strings.Index(plain, "▾ api (1)")
	if arsAt == -1 || apiAt == -1 || arsAt > apiAt {
		t.Fatalf("headers missing or misordered:\n%s", plain)
	}
	if !strings.Contains(plain, "└─ ✻  connection check") {
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
	if !strings.Contains(plain, "▸ ars (1) ✻") {
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
	value.result.Sessions = items
	value.width, value.height, value.noColor = 120, 24, true
	value.refreshVisible()
	plain := ansi.Strip(value.View().Content)
	if !strings.Contains(plain, "├─ ✻  connection check") ||
		!strings.Contains(plain, "└─ ∙  API repair") {
		t.Fatalf("guides wrong:\n%s", plain)
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

func TestStaleCachedColumnKeepsActivityVisible(t *testing.T) {
	model := readyModel()
	model.width, model.height, model.noColor = 80, 24, true
	items := twoSessions()
	items[1].Title = "a very long stale session title " + strings.Repeat("x", 80)
	model.result.Sessions = items
	model.stale = map[string]struct{}{"server": {}}
	model.refreshVisible()

	content := ansi.Strip(model.View().Content)
	for _, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, "a very long stale") {
			continue
		}
		if !strings.HasSuffix(strings.TrimRight(line, " "), "cached") || !strings.Contains(line, "2d") {
			t.Fatalf("stale row lost activity or cached column: %q", line)
		}
		if ansi.StringWidth(line) > model.width {
			t.Fatalf("stale row exceeds width: %q", line)
		}
		return
	}
	t.Fatalf("stale row not found:\n%s", content)
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
	if !strings.Contains(content, "no sessions") {
		t.Fatalf("empty inventory view missing message: %q", content)
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

func TestFilteredRowsKeepStableColumnLayout(t *testing.T) {
	model := readyModel()
	model.width = 120
	providerStart := func(content string) int {
		for _, line := range strings.Split(content, "\n") {
			if strings.Contains(line, "API repair") {
				return strings.Index(line, "codex")
			}
		}
		return -1
	}
	unfiltered := providerStart(ansi.Strip(model.View().Content))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: '/'}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "API"}))
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

func TestViewMarksStaleHostRowsAsCached(t *testing.T) {
	model := readyModel()
	model.width = 120
	model.stale = map[string]struct{}{"server": {}}
	model.refreshVisible()

	content := ansi.Strip(model.View().Content)
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		switch {
		case strings.Contains(line, "API repair"):
			if !strings.HasSuffix(strings.TrimRight(line, " "), "cached") {
				t.Fatalf("stale row missing cached marker: %q", line)
			}
		case strings.Contains(line, "connection check"):
			if strings.Contains(line, "cached") {
				t.Fatalf("fresh row has cached marker: %q", line)
			}
		}
	}

	model.noColor = false
	rawContent := model.View().Content
	faintCached := model.stateText("cached", session.RuntimeSaved)
	for _, line := range strings.Split(rawContent, "\n") {
		switch {
		case strings.Contains(line, "API repair"):
			if !strings.Contains(line, faintCached) {
				t.Fatalf("stale row cached marker not faint-styled: %q", line)
			}
		case strings.Contains(line, "connection check"):
			if strings.Contains(line, "cached") {
				t.Fatalf("fresh row has cached marker: %q", line)
			}
		}
	}
}
