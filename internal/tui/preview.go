package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

const (
	previewMinWidth = 100
	previewInterval = 2 * time.Second
	previewGutter   = 2
)

type previewMsg struct {
	key     sessionKey
	content []byte
	err     error
}

type previewTickMsg struct {
	key sessionKey
}

// previewVisible reports whether the preview panel should render: enabled by
// the user, wide enough, and wired with a Preview dependency.
func (value model) previewVisible() bool {
	return value.previewOn && value.deps.Preview != nil && value.contentWidth() >= previewMinWidth
}

// previewWidth splits the content width, giving the list priority and the
// preview roughly 40%.
func previewWidth(total int) (list, preview int) {
	preview = total * 2 / 5
	list = total - preview - previewGutter
	return list, preview
}

// syncPreview issues a capture for the current selection when the preview is
// visible and the selection changed since the last load. Saved sessions need
// no capture; their panel is rendered locally. It also (re)starts the tick.
func (value *model) syncPreview() tea.Cmd {
	if !value.previewVisible() {
		return nil
	}
	selected, ok := value.selectedSession()
	if !ok {
		value.previewKey = sessionKey{}
		value.previewContent = nil
		value.previewErr = ""
		value.previewPending = false
		return nil
	}
	key := keyOf(selected)
	if key == value.previewKey {
		return nil
	}
	value.previewKey = key
	value.previewContent = nil
	value.previewErr = ""
	if selected.Runtime.State == session.RuntimeSaved {
		value.previewPending = false
		return nil
	}
	value.previewPending = true
	return tea.Batch(capturePreview(value.ctx, value.deps.Preview, selected), previewTick(key))
}

func capturePreview(ctx context.Context, capture func(context.Context, session.Session) ([]byte, error), item session.Session) tea.Cmd {
	key := keyOf(item)
	return func() tea.Msg {
		content, err := capture(ctx, item)
		return previewMsg{key: key, content: content, err: err}
	}
}

func previewTick(key sessionKey) tea.Cmd {
	return tea.Tick(previewInterval, func(time.Time) tea.Msg {
		return previewTickMsg{key: key}
	})
}

func (value model) updatePreview(message previewMsg) (model, tea.Cmd) {
	if message.key != value.previewKey {
		return value, nil
	}
	value.previewPending = false
	if message.err != nil {
		value.previewErr = message.err.Error()
		value.previewContent = nil
		return value, nil
	}
	value.previewErr = ""
	value.previewContent = splitPreview(message.content)
	return value, nil
}

func (value model) updatePreviewTick(message previewTickMsg) (model, tea.Cmd) {
	if message.key != value.previewKey || !value.previewVisible() {
		return value, nil
	}
	selected, ok := value.selectedSession()
	if !ok || keyOf(selected) != message.key || selected.Runtime.State == session.RuntimeSaved {
		return value, nil
	}
	// A capture is already in flight; keep the tick alive but do not stack a
	// second capture, so a slow SSH probe cannot pile up processes.
	if value.previewPending {
		return value, previewTick(message.key)
	}
	value.previewPending = true
	return value, tea.Batch(capturePreview(value.ctx, value.deps.Preview, selected), previewTick(message.key))
}

func splitPreview(content []byte) []string {
	text := strings.TrimRight(string(content), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// previewPanel renders the preview column: a session header, a divider, and
// the tail of the captured pane fitted to the panel box.
func (value model) previewPanel(width, height int) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	lines := make([]string, 0, height)
	selected, ok := value.selectedSession()
	if !ok {
		return value.padPanel(lines, width, height)
	}

	header := sessionTitle(selected)
	lines = append(lines, value.previewHeader(header, width))
	if height > 1 {
		lines = append(lines, value.mutedText(strings.Repeat("─", width), width))
	}

	body := value.previewBody(selected, width, height-len(lines))
	lines = append(lines, body...)
	return value.padPanel(lines, width, height)
}

func (value model) previewHeader(text string, width int) string {
	if value.noColor {
		return fitLine(text, width)
	}
	return value.styles.title.Render(fitLine(text, width))
}

func (value model) previewBody(selected session.Session, width, height int) []string {
	if height <= 0 {
		return nil
	}
	if selected.Runtime.State == session.RuntimeSaved {
		return []string{value.mutedText("no live pane", width)}
	}
	if value.previewErr != "" {
		return []string{value.mutedText("preview unavailable", width)}
	}
	if len(value.previewContent) == 0 {
		return []string{value.mutedText("loading preview…", width)}
	}
	lines := value.previewContent
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	fitted := make([]string, len(lines))
	for index, line := range lines {
		fitted[index] = fitLine(ansi.Strip(line), width)
	}
	return fitted
}

// joinPreview places the preview panel to the right of the list body, one row
// per line, separated by a fixed gutter. The joined block is as tall as the
// panel so the preview can use the full body height even when the list is
// short; missing list rows become blank padding.
func (value model) joinPreview(body []string, listWidth, previewCols, height int) []string {
	if height < len(body) {
		height = len(body)
	}
	panel := value.previewPanel(previewCols, height)
	gutter := strings.Repeat(" ", previewGutter)
	joined := make([]string, height)
	for index := range height {
		line := ""
		if index < len(body) {
			line = body[index]
		}
		if pad := listWidth - lipgloss.Width(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		right := ""
		if index < len(panel) {
			right = panel[index]
		}
		joined[index] = line + gutter + right
	}
	return joined
}

func (value model) padPanel(lines []string, width, height int) []string {
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	for index, line := range lines {
		if pad := width - lipgloss.Width(line); pad > 0 {
			lines[index] = line + strings.Repeat(" ", pad)
		}
	}
	return lines[:height]
}
