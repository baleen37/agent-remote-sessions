package tui

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type updateChoiceModel struct {
	current   string
	latest    string
	selected  int
	confirmed bool
	update    bool
	styles    viewStyles
}

func newUpdateChoiceModel(current, latest string) updateChoiceModel {
	return updateChoiceModel{
		current: current,
		latest:  latest,
		styles:  newViewStyles(true),
	}
}

func (value updateChoiceModel) Init() tea.Cmd {
	return tea.RequestBackgroundColor
}

func (value updateChoiceModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.BackgroundColorMsg:
		value.styles = newViewStyles(message.IsDark())
	case tea.KeyPressMsg:
		key := message.Key()
		switch {
		case key.Code == tea.KeyUp, key.Code == tea.KeyDown:
			value.selected = 1 - value.selected
		case key.Code == '1':
			return value.confirm(0)
		case key.Code == '2':
			return value.confirm(1)
		case key.Code == tea.KeyEnter:
			return value.confirm(value.selected)
		case key.Code == 'q', key.Code == tea.KeyEscape:
			return value.confirm(1)
		case key.Code == 'c' && key.Mod&tea.ModCtrl != 0:
			return value.confirm(1)
		}
	}
	return value, nil
}

func (value updateChoiceModel) View() tea.View {
	lines := []string{
		value.styles.title.Render(fmt.Sprintf("ars v%s available (current v%s)", value.latest, value.current)),
		"",
		value.choiceRow(0, fmt.Sprintf("1. Update to v%s", value.latest)),
		value.choiceRow(1, fmt.Sprintf("2. Continue with v%s", value.current)),
		"",
		value.styles.muted.Render("↑/↓ move · 1/2 choose · enter confirm"),
	}
	return tea.View{Content: strings.Join(lines, "\n")}
}

func (value updateChoiceModel) choiceRow(index int, label string) string {
	if value.selected != index {
		return "  " + label
	}
	return value.styles.selectedCursor.Render(">") + " " + value.styles.selected.Render(label)
}

func (value updateChoiceModel) confirm(selected int) (tea.Model, tea.Cmd) {
	value.selected = selected
	value.confirmed = true
	value.update = selected == 0
	return value, tea.Quit
}

// ChooseUpdate asks whether to install the latest release before the main TUI.
func ChooseUpdate(ctx context.Context, input io.Reader, output io.Writer, current, latest string) bool {
	program := tea.NewProgram(
		newUpdateChoiceModel(current, latest),
		tea.WithContext(ctx),
		tea.WithInput(input),
		tea.WithOutput(output),
	)
	final, err := program.Run()
	if err != nil {
		return false
	}
	value, ok := final.(updateChoiceModel)
	return ok && value.confirmed && value.update
}
