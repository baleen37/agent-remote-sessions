package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestHeaderComposition locks the agent-deck style header assembly: compact
// status logo, title, status-count summary (symbol colored, label muted),
// and a right-aligned version. readyModel has one attached and one idle
// session, so running/waiting are omitted.
func TestHeaderComposition(t *testing.T) {
	value := readyModel()
	value.width, value.noColor = 120, false

	got := value.header(value.width)
	want := value.styles.muted.Render("⟨") +
		value.styles.attached.Render("●") + value.styles.saved.Render("○○") +
		value.styles.muted.Render("⟩") + " " +
		value.styles.title.Render("ars") +
		"  " +
		value.styles.attached.Render("●") + " " + value.styles.muted.Render("1 attached") +
		" · " +
		value.styles.saved.Render("○") + " " + value.styles.muted.Render("1 idle")
	if got != want {
		t.Fatalf("header = %q, want %q", got, want)
	}
}

func TestHeaderLogoFillsWithLiveSessionCount(t *testing.T) {
	cases := []struct {
		name string
		live int
		want string
	}{
		{"zero live sessions", 0, "○○○"},
		{"one live session", 1, "●○○"},
		{"three or more live sessions caps at three", 5, "●●●"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := readyModel()
			value.noColor = true
			got := value.statusLogo(tc.live)
			want := "⟨" + tc.want + "⟩"
			if got != want {
				t.Fatalf("statusLogo(%d) = %q, want %q", tc.live, got, want)
			}
		})
	}
}

func TestHeaderOmitsZeroCounts(t *testing.T) {
	value := readyModel()
	value.noColor = true
	got := value.statusCounts(0, 0, 0, 1)
	want := "○ 1 idle"
	if got != want {
		t.Fatalf("statusCounts with only idle = %q, want %q", got, want)
	}
	if strings.Contains(got, "attached") || strings.Contains(got, "running") || strings.Contains(got, "waiting") {
		t.Fatalf("statusCounts leaked a zero-count label: %q", got)
	}
}

func TestHeaderCountsWaitingSessionsSeparatelyFromRunning(t *testing.T) {
	value := waitingModel(3, 1)
	value.noColor = true
	value.width = 120

	content := value.headerContent()
	for _, want := range []string{"2 running", "1 waiting"} {
		if !strings.Contains(content, want) {
			t.Fatalf("header content = %q, missing %q", content, want)
		}
	}
}

func TestHeaderVersionRightAlignsAndDropsWhenNarrow(t *testing.T) {
	value := readyModel()
	value.noColor = true
	value.deps.Version = "1.2.3"

	wide := value.header(120)
	if !strings.HasSuffix(wide, "v1.2.3") {
		t.Fatalf("header at width 120 = %q, want suffix v1.2.3", wide)
	}
	if got := len([]rune(wide)); got > 120 {
		t.Fatalf("header width = %d, want <= 120", got)
	}

	narrow := value.header(20)
	if strings.Contains(narrow, "v1.2.3") {
		t.Fatalf("header at width 20 = %q, want version dropped", narrow)
	}
	if got := len([]rune(narrow)); got > 20 {
		t.Fatalf("header width = %d, want <= 20", got)
	}
}

func TestHeaderVersionOmittedWhenEmpty(t *testing.T) {
	value := readyModel()
	value.noColor = true
	value.width = 120
	value.deps.Version = ""

	if got := value.versionText(); got != "" {
		t.Fatalf("versionText() = %q, want empty for dev build", got)
	}
	want := "⟨●○○⟩ ars  ● 1 attached · ○ 1 idle"
	if got := value.header(value.width); got != want {
		t.Fatalf("header with no Version set = %q, want %q (no trailing version)", got, want)
	}
}

func TestHeaderNeverExceedsRealisticTerminalWidths(t *testing.T) {
	for _, width := range []int{120, 140} {
		value := readyModel()
		value.noColor = true
		value.width = width
		value.deps.Version = "1.2.3"

		got := value.header(value.width)
		if w := lipgloss.Width(got); w > width {
			t.Fatalf("header width at terminal width %d = %d, want <= %d: %q", width, w, width, got)
		}
	}
}

func TestHeaderVersionAvoidsDoubleVPrefix(t *testing.T) {
	value := readyModel()
	value.noColor = true

	value.deps.Version = "v2.0.0"
	if got := value.versionText(); got != "v2.0.0" {
		t.Fatalf("versionText() with v-prefixed input = %q, want %q", got, "v2.0.0")
	}

	value.deps.Version = "2.0.0"
	if got := value.versionText(); got != "v2.0.0" {
		t.Fatalf("versionText() with bare input = %q, want %q", got, "v2.0.0")
	}
}

func TestHeaderNoColorRendersPlainText(t *testing.T) {
	value := readyModel()
	value.noColor = true
	value.width = 120
	value.deps.Version = "1.0.0"

	got := value.header(value.width)
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("header under noColor contains ANSI escapes: %q", got)
	}
	left := "⟨●○○⟩ ars  ● 1 attached · ○ 1 idle"
	want := left + strings.Repeat(" ", 120-lipgloss.Width(left)-lipgloss.Width("v1.0.0")) + "v1.0.0"
	if got != want {
		t.Fatalf("noColor header = %q, want %q", got, want)
	}
}
