package tui

import (
	"context"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"
)

func Run(ctx context.Context, deps Dependencies, input io.Reader, output io.Writer) error {
	if deps.Collect == nil || deps.Attach == nil {
		return fmt.Errorf("invalid TUI dependencies")
	}
	program := tea.NewProgram(
		newModel(ctx, deps),
		tea.WithContext(ctx),
		tea.WithInput(input),
		tea.WithOutput(output),
	)
	_, err := program.Run()
	return err
}
