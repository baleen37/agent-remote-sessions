package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// helpContext names the situation the user is in when they open the overlay.
// The row kind contexts are mutually exclusive; helpPreview and helpKillPending
// are additive on top of one.
type helpContext int

const (
	helpGroupRow helpContext = iota
	helpSessionRow
	helpMoreRow
	helpPreview
	helpKillPending
	helpFilterActive
)

// helpBinding is one overlay row. featured lists the contexts that lift the row
// into the leading section; altDescription replaces description in those
// contexts where the phrasing only makes sense there (closing the preview).
type helpBinding struct {
	key            string
	text           string
	altDescription string
	altContext     helpContext
	featured       []helpContext
}

// description renders the row's text for value's context, preferring
// altDescription when that context applies.
func (binding helpBinding) description(value model) string {
	if binding.altDescription != "" && value.helpContexts()[binding.altContext] {
		return binding.altDescription
	}
	return binding.text
}

// helpBindings is the full keyset in its baseline order. Featured rows are
// lifted out of this order into the context section; nothing is added or
// dropped, so the overlay always documents exactly these keys.
var helpBindings = []helpBinding{
	{key: "↑↓ / jk", text: "move"},
	{key: "h / l", text: "fold / unfold group", featured: []helpContext{helpGroupRow}},
	{key: "g / G · Home / End", text: "jump to top / end"},
	{key: "1-9", text: "jump to group", featured: []helpContext{helpGroupRow}},
	{key: "PgUp / PgDn · Ctrl+U / Ctrl+D", text: "page up / down"},
	{key: "/", text: "search"},
	{key: "! / @ / #", text: "filter attached / running / saved", featured: []helpContext{helpFilterActive}},
	{
		key:            "p",
		text:           "toggle preview pane",
		altDescription: "close preview pane",
		altContext:     helpPreview,
		featured:       []helpContext{helpPreview},
	},
	{key: "f", text: "fullscreen preview", featured: []helpContext{helpPreview}},
	{key: "P", text: "pin / unpin session", featured: []helpContext{helpSessionRow}},
	{key: "x", text: "kill session / group (3s grace · u undo)", featured: []helpContext{helpGroupRow, helpSessionRow}},
	{key: "u", text: "undo the pending kill", featured: []helpContext{helpKillPending}},
	{key: "m", text: "send a line without attaching", featured: []helpContext{helpSessionRow}},
	{key: "enter", text: "attach session · toggle group", featured: []helpContext{helpGroupRow, helpSessionRow, helpMoreRow}},
	{key: "space", text: "toggle group", featured: []helpContext{helpGroupRow}},
	{key: "r", text: "refresh"},
	{key: "q", text: "quit"},
	{key: "Ctrl+Q", text: "detach from an attached session"},
}

// helpContexts reports which contexts apply to the current model state. Search
// and compose are absent by construction: updateKey returns early in both
// modes, so ? is typed literally there and the overlay can never be open.
func (value model) helpContexts() map[helpContext]bool {
	contexts := map[helpContext]bool{}
	if row, ok := value.selectedRow(); ok {
		switch row.kind {
		case rowHeader:
			contexts[helpGroupRow] = true
		case rowMore:
			contexts[helpMoreRow] = true
		default:
			contexts[helpSessionRow] = true
		}
	}
	if value.previewVisible() {
		contexts[helpPreview] = true
	}
	if value.killPending {
		contexts[helpKillPending] = true
	}
	if value.filterActive() {
		contexts[helpFilterActive] = true
	}
	return contexts
}

// helpContextLabel names the leading section. A pending kill outranks the row
// kind because undoing it is the only thing the user can still do in time. It
// falls back to a generic label rather than "" whenever any context applies, so
// a featured row can never end up in a section the renderer suppresses.
func (value model) helpContextLabel() string {
	contexts := value.helpContexts()
	switch {
	case contexts[helpKillPending]:
		return "kill pending:"
	case contexts[helpGroupRow]:
		return "on a group header:"
	case contexts[helpMoreRow]:
		return "on a … more row:"
	case contexts[helpSessionRow]:
		return "on a session:"
	case contexts[helpPreview]:
		return "preview open:"
	case contexts[helpFilterActive]:
		return "filter active:"
	default:
		return "here:"
	}
}

// featuredHelp splits the keyset into the context section and the remainder.
// Featured rows keep the baseline relative order within each priority band —
// undo first, then the row-kind bindings, then the additive preview and filter
// ones — and never repeat below, so every key appears exactly once.
func (value model) featuredHelp() (featured, rest []helpBinding) {
	contexts := value.helpContexts()
	bands := [][]helpContext{
		{helpKillPending},
		{helpGroupRow, helpSessionRow, helpMoreRow},
		{helpPreview, helpFilterActive},
	}
	isFeatured := make(map[string]bool, len(helpBindings))
	for _, band := range bands {
		for _, binding := range helpBindings {
			for _, context := range binding.featured {
				if contexts[context] && contained(band, context) && !isFeatured[binding.key] {
					isFeatured[binding.key] = true
					featured = append(featured, binding)
				}
			}
		}
	}
	for _, binding := range helpBindings {
		if !isFeatured[binding.key] {
			rest = append(rest, binding)
		}
	}
	return featured, rest
}

func contained(contexts []helpContext, want helpContext) bool {
	for _, context := range contexts {
		if context == want {
			return true
		}
	}
	return false
}

// helpOverlay renders the full-screen key reference, leading with the bindings
// that matter in the current context so they are not buried mid-list. The
// overlay has never clamped to height — a short terminal scrolls the alt screen
// — and the context section keeps that behavior, costing at most a label and a
// blank line over the plain list.
func (value model) helpOverlay(inset, width int) tea.View {
	featured, rest := value.featuredHelp()
	keyWidth := 0
	for _, binding := range helpBindings {
		keyWidth = max(keyWidth, lipgloss.Width(binding.key))
	}
	title := "ars keys"
	if !value.noColor {
		title = value.styles.title.Render(title)
	}
	lines := []string{fitLine(title, width), ""}
	if label := value.helpContextLabel(); label != "" && len(featured) > 0 {
		lines = append(lines, value.mutedText(label, width))
		lines = append(lines, value.helpRows(featured, keyWidth, width)...)
		lines = append(lines, "", value.mutedText("all keys:", width))
	}
	lines = append(lines, value.helpRows(rest, keyWidth, width)...)
	legend := "● attached · ◐ running · " + activityWaitingSymbol + " needs input · ○ saved"
	lines = append(lines, "", value.mutedText(legend, width), value.mutedText("? / esc / q to close", width))
	margin := strings.Repeat(" ", inset)
	for index, line := range lines {
		if line != "" {
			lines[index] = margin + line
		}
	}
	return tea.View{Content: strings.Join(lines, "\n"), AltScreen: true}
}

func (value model) helpRows(bindings []helpBinding, keyWidth, width int) []string {
	lines := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		key := binding.key + strings.Repeat(" ", max(0, keyWidth-lipgloss.Width(binding.key)))
		plain := fitLine(key+"  "+binding.description(value), width)
		if !value.noColor {
			description := strings.TrimPrefix(plain, key+"  ")
			plain = key + "  " + value.styles.muted.Render(description)
		}
		lines = append(lines, plain)
	}
	return lines
}
