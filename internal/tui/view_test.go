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

func TestViewRendersOneLineGroupsAndNeutralProviderLocation(t *testing.T) {
	model := readyModel()
	model.noColor = false
	model.width = 120
	model.height = 24
	content := model.View().Content
	plain := ansi.Strip(content)
	for _, want := range []string{
		"ars", "1 active", "1 recent", "Active", "Recent", "claude", "local",
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
	model.result.Warnings = append(model.result.Warnings, hostError("macbook", "corrupt", "Claude discovery partial"))
	model.status = strings.Repeat("status ", 100)

	content := ansi.Strip(model.View().Content)
	for _, want := range []string{
		"/work/ars", "123e4567-e89b-42d3-a456-426614174000", "2026-07-19T12:00:00Z",
		"server: failed", "macbook: Claude discovery partial", "status",
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
	for _, want := range []string{"local", "attached", "1d"} {
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
	model := newModel(context.Background(), Dependencies{})
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

func activeRow(content string) string {
	lines := strings.Split(ansi.Strip(content), "\n")
	for _, line := range lines {
		if strings.Contains(line, "local") && strings.Contains(line, "attached") {
			return line
		}
	}
	return ""
}

func selectedRow(content string) string {
	for _, line := range strings.Split(ansi.Strip(content), "\n") {
		if strings.HasPrefix(line, "> ") {
			return line
		}
	}
	return ""
}
