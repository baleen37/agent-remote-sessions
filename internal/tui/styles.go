package tui

import "charm.land/lipgloss/v2"

type viewStyles struct {
	title          lipgloss.Style
	selected       lipgloss.Style
	selectedCursor lipgloss.Style
	attached       lipgloss.Style
	running        lipgloss.Style
	saved          lipgloss.Style
	muted          lipgloss.Style
	failure        lipgloss.Style
}

func newViewStyles(dark bool) viewStyles {
	choose := lipgloss.LightDark(dark)
	return viewStyles{
		title: lipgloss.NewStyle().Bold(true),
		selected: lipgloss.NewStyle().Background(choose(
			lipgloss.Color("#DDF7F5"),
			lipgloss.Color("#153B3B"),
		)),
		selectedCursor: lipgloss.NewStyle().Bold(true).Foreground(choose(
			lipgloss.Color("#007C83"),
			lipgloss.Color("#5EEAD4"),
		)),
		attached: lipgloss.NewStyle().Foreground(choose(
			lipgloss.Color("#16803D"),
			lipgloss.Color("#4ADE80"),
		)),
		running: lipgloss.NewStyle().Foreground(choose(
			lipgloss.Color("#A15C00"),
			lipgloss.Color("#FACC15"),
		)),
		saved:  lipgloss.NewStyle().Faint(true),
		muted:  lipgloss.NewStyle().Faint(true),
		failure: lipgloss.NewStyle().Foreground(choose(
			lipgloss.Color("#B42318"),
			lipgloss.Color("#F97066"),
		)),
	}
}
