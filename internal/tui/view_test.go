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
	for range len(model.visible) - 1 {
		model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	}

	content := ansi.Strip(model.View().Content)
	for _, want := range []string{
		"> ∙  session 15",
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
	content := model.View().Content
	plain := ansi.Strip(content)
	for _, want := range []string{
		"ars", "1 active", "1 recent", "Active", "Recent", "claude", "server",
		"attached(1)", "↑↓/jk move",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q: %q", want, plain)
		}
	}
	if strings.Count(plain, "connection check") != 1 {
		t.Fatalf("session row did not render exactly once: %q", plain)
	}
	if got := model.View(); !got.AltScreen {
		t.Fatal("View() did not request alternate screen")
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
	if !strings.Contains(content, "1 hosts") || strings.Contains(content, "localhost: Claude") {
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

	row := selectedRow(model.View().Content)
	for _, want := range []string{"critical-title", "remote-host", "attached(1)", "1d"} {
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
	for _, want := range []string{"claude", "ars", "attached(1)"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("wide row missing %q: %q", want, wide)
		}
	}

	model.width = 80
	withoutProject := activeRow(model.View().Content)
	if strings.Contains(withoutProject, " ars ") || !strings.Contains(withoutProject, "claude") || !strings.Contains(withoutProject, "attached(1)") {
		t.Fatalf("project was not removed first: %q", withoutProject)
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

func TestViewKeepsBalancedVerticalRhythm(t *testing.T) {
	value := readyModel()
	value.width, value.height = 120, 24
	plain := trimmedLines(ansi.Strip(value.View().Content))
	for _, want := range []string{
		"ars  1 active · 1 recent · 0 hosts\n\n Active\n",
		"attached(1)  1d\n\n Recent\n",
		"\n\n ↑↓/jk move",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing rhythm %q:\n%s", want, plain)
		}
	}
}

func TestNoColorPreservesSelectionAndStateWithoutANSI(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, true
	content := value.View().Content
	if ansi.Strip(content) != content {
		t.Fatalf("NO_COLOR emitted ANSI: %q", content)
	}
	for _, want := range []string{"> ✻", "attached(1)", "Recent", "∙"} {
		if !strings.Contains(content, want) {
			t.Fatalf("NO_COLOR missing %q: %q", want, content)
		}
	}
}

func TestSecondaryUIUsesHierarchyStyles(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, false
	lines := strings.Split(value.View().Content, "\n")
	header, help := lines[0], lines[len(lines)-1]
	if ansi.Strip(header) == header {
		t.Fatal("header title and statistics are not styled")
	}
	if ansi.Strip(help) == help {
		t.Fatal("help is not muted")
	}
}

func trimmedLines(content string) string {
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
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
			if !strings.HasSuffix(strings.TrimRight(line, " "), faintCached) {
				t.Fatalf("stale row cached marker not faint-styled: %q", line)
			}
		case strings.Contains(line, "connection check"):
			if strings.Contains(line, "cached") {
				t.Fatalf("fresh row has cached marker: %q", line)
			}
		}
	}
}
