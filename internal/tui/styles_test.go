package tui

import (
	"image/color"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestViewStylesAdaptSelectionToTerminalBackground(t *testing.T) {
	light := newViewStyles(false)
	dark := newViewStyles(true)
	if light.selected.GetBackground() == dark.selected.GetBackground() {
		t.Fatal("selection background did not adapt")
	}
	if light.attached.GetForeground() == nil ||
		light.running.GetForeground() == nil ||
		light.failure.GetForeground() == nil {
		t.Fatal("semantic state colors are missing")
	}
	if !light.muted.GetFaint() || !dark.muted.GetFaint() {
		t.Fatal("secondary text is not muted")
	}
}

func TestModelUpdatesStylesFromBackgroundColor(t *testing.T) {
	value := readyModel()
	before := value.styles.selected.GetBackground()
	value, _ = updateModel(value, tea.BackgroundColorMsg{
		Color: color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
	})
	if value.styles.selected.GetBackground() == before {
		t.Fatal("background response did not update styles")
	}
}
