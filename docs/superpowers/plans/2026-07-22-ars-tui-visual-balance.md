# ARS TUI Visual Balance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the existing `ars` session list visually balanced with consistent spacing, aligned columns, a full-width subtle selection, and restrained adaptive state colors.

**Architecture:** Keep behavior and rendering changes inside `internal/tui`. Add one small style set owned by the Bubble Tea model, move shared row measurement into a focused file, and keep the existing `View` responsible for vertical layout and terminal bounds.

**Tech Stack:** Go 1.26, Bubble Tea v2.0.8, Lip Gloss v2.0.5, `github.com/charmbracelet/x/ansi`, Go `testing`

## Global Constraints

- Preserve the flat Active/Recent one-line list, keyboard behavior, session data, and attach flow.
- Do not add tabs, panels, borders, mouse support, themes, provider colors, configuration, or a widget library.
- Keep one blank line after the header, between Active and Recent, between the list and details, and before help; keep headings adjacent to their first row.
- Use a two-space gutter between visible columns, one space of internal row padding when width permits, and preserve the optional-column removal order: project, provider, attached-client count.
- Keep title, provider, location, and project on the normal foreground. Use color only for selection, runtime state, errors, and muted secondary UI.
- Support light, dark, reduced-color, and `NO_COLOR` output without making color the only state cue.
- Use rendered terminal width for every inset, padding, Unicode truncation, and line-bound check.
- Report automated tests and interactive terminal acceptance separately.

---

### Task 1: Add one adaptive TUI style set

**Files:**
- Create: `internal/tui/styles.go`
- Create: `internal/tui/styles_test.go`
- Modify: `internal/tui/model.go:44-114`
- Modify: `internal/tui/model_test.go:16-66`

**Interfaces:**
- Consumes: `tea.BackgroundColorMsg.IsDark()`, `lipgloss.LightDark(bool)`, and the existing `model.noColor` flag.
- Produces: `type viewStyles struct`, `func newViewStyles(dark bool) viewStyles`, and `model.styles viewStyles`.

- [ ] **Step 1: Write failing palette and background-response tests**

Create `internal/tui/styles_test.go`:

```go
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
```

Update `TestModelInitialCollectionNavigatesFiltersAndAttaches` to read the collection command from the `tea.BatchMsg` returned by `Init`:

```go
func collectionMessage(t *testing.T, command tea.Cmd) collectDoneMsg {
	t.Helper()
	message := command()
	batch, ok := message.(tea.BatchMsg)
	if !ok {
		t.Fatalf("Init command result = %T, want tea.BatchMsg", message)
	}
	for _, child := range batch {
		if collected, ok := child().(collectDoneMsg); ok {
			return collected
		}
	}
	t.Fatal("Init batch did not contain collection")
	return collectDoneMsg{}
}
```

- [ ] **Step 2: Run the tests and verify the red state**

Run:

```bash
go test ./internal/tui -run 'Test(ViewStylesAdaptSelectionToTerminalBackground|ModelUpdatesStylesFromBackgroundColor|ModelInitialCollectionNavigatesFiltersAndAttaches)$'
```

Expected: FAIL because `newViewStyles` and `model.styles` do not exist and `Init` does not return a batch.

- [ ] **Step 3: Implement the minimal style set**

Create `internal/tui/styles.go`:

```go
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
```

Add `styles viewStyles` to `model`, initialize `styles: newViewStyles(true)` in `newModel`, batch the existing collection with the terminal query, and handle the response:

```go
func (value model) Init() tea.Cmd {
	return tea.Batch(
		value.collectCommand(value.generation),
		tea.RequestBackgroundColor,
	)
}

// In updateModel, before tea.WindowSizeMsg:
case tea.BackgroundColorMsg:
	value.styles = newViewStyles(message.IsDark())
	return value, nil
```

- [ ] **Step 4: Run package tests**

Run: `go test ./internal/tui`

Expected: PASS.

- [ ] **Step 5: Commit the style foundation**

```bash
git add internal/tui/model.go internal/tui/model_test.go internal/tui/styles.go internal/tui/styles_test.go
git commit -m "feat: add adaptive tui styles"
```

---

### Task 2: Align rows and add balanced horizontal inset

**Files:**
- Create: `internal/tui/row.go`
- Create: `internal/tui/row_test.go`
- Modify: `internal/tui/view.go:15-228`
- Modify: `internal/tui/view_test.go:16-220`

**Interfaces:**
- Consumes: `session.Session`, `model.styles`, `model.noColor`, and the existing responsive width thresholds.
- Produces: `type rowLayout`, `func newRowLayout([]session.Session, int, time.Time, string) rowLayout`, `func (model) renderRow(session.Session, rowLayout) string`, and `func contentFrame(int) (inset int, usable int)`.

- [ ] **Step 1: Write failing alignment, inset, and selected-width tests**

Create `internal/tui/row_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

func TestWideRowsAlignRuntimeAndAgeColumns(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, true
	short := twoSessions()[0]
	long := short
	long.NativeID = "223e4567-e89b-42d3-a456-426614174000"
	long.Title = "a much longer session title"
	long.Host = "server"
	value.result.Sessions = []session.Session{short, long}
	value.refreshVisible()

	rows := sessionRows(value.View().Content)
	if len(rows) != 2 {
		t.Fatalf("session rows = %q", rows)
	}
	if strings.Index(rows[0], "attached") != strings.Index(rows[1], "attached") ||
		strings.LastIndex(rows[0], "1d") != strings.LastIndex(rows[1], "1d") {
		t.Fatalf("runtime columns are not aligned: %q", rows)
	}
}

func TestSelectedRowFillsUsableWidthInsideInset(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, false
	line := styledSelectedRow(value.View().Content)
	inset, usable := contentFrame(value.width)
	padding := rowPadding(usable)
	if !strings.HasPrefix(ansi.Strip(line), strings.Repeat(" ", inset+padding)+">") {
		t.Fatalf("selected row missing inset: %q", ansi.Strip(line))
	}
	if ansi.StringWidth(line)-inset != usable {
		t.Fatalf("selected width = %d, want usable %d + inset %d",
			ansi.StringWidth(line), usable, inset)
	}
}

func TestVeryNarrowFrameDropsInset(t *testing.T) {
	if inset, usable := contentFrame(39); inset != 0 || usable != 39 {
		t.Fatalf("contentFrame(39) = (%d, %d)", inset, usable)
	}
	if inset, usable := contentFrame(40); inset != 1 || usable != 38 {
		t.Fatalf("contentFrame(40) = (%d, %d)", inset, usable)
	}
}
```

Add helpers that find rows without depending on ANSI sequences:

```go
func sessionRows(content string) []string {
	var rows []string
	for _, line := range strings.Split(ansi.Strip(content), "\n") {
		if strings.Contains(line, "attached") {
			rows = append(rows, line)
		}
	}
	return rows
}

func styledSelectedRow(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimLeft(ansi.Strip(line), " "), "> ") {
			return line
		}
	}
	return ""
}
```

- [ ] **Step 2: Run the tests and verify the red state**

Run:

```bash
go test ./internal/tui -run 'Test(WideRowsAlignRuntimeAndAgeColumns|SelectedRowFillsUsableWidthInsideInset|VeryNarrowFrameDropsInset)$'
```

Expected: FAIL because rows use per-row widths, selected backgrounds stop at text width, and `contentFrame` does not exist.

- [ ] **Step 3: Implement shared measurement in `internal/tui/row.go`**

Use these concrete types and helpers:

```go
package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

const (
	columnGutter  = "  "
	rowPrefixSize = 2
	minInsetWidth = 40
)

type rowLayout struct {
	width                      int
	showProvider, showProject  bool
	showClients                bool
	title, provider, location  int
	project, runtime, activity int
}

func contentFrame(width int) (int, int) {
	if width >= minInsetWidth {
		return 1, width - 2
	}
	return 0, width
}

func rowPadding(width int) int {
	if width >= 4 {
		return 1
	}
	return 0
}

func allocateWidths(natural []int, budget int) []int {
	widths := make([]int, len(natural))
	pending := make([]int, len(natural))
	for index := range natural {
		pending[index] = index
	}
	for len(pending) > 0 {
		share := budget / len(pending)
		placed := false
		for pendingIndex, fieldIndex := range pending {
			if natural[fieldIndex] > share {
				continue
			}
			widths[fieldIndex] = natural[fieldIndex]
			budget -= natural[fieldIndex]
			pending = append(pending[:pendingIndex], pending[pendingIndex+1:]...)
			placed = true
			break
		}
		if placed {
			continue
		}
		for pendingIndex, fieldIndex := range pending {
			widths[fieldIndex] = budget / (len(pending) - pendingIndex)
			budget -= widths[fieldIndex]
		}
		break
	}
	return widths
}

func column(value string, width int, right bool) string {
	value = ansi.Truncate(value, width, "…")
	padding := strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
	if right {
		return padding + value
	}
	return value + padding
}
```

Implement `newRowLayout` with the existing width thresholds. Measure all visible sessions once, subtract fixed fields and gutters, then allocate the remaining width to title, location, and project:

```go
func newRowLayout(items []session.Session, width int, now time.Time, localTarget string) rowLayout {
	layout := rowLayout{
		width:        width,
		showProvider: width >= providerColumnWidth,
		showProject:  width >= projectColumnWidth,
		showClients:  width >= clientColumnWidth,
	}
	for _, item := range items {
		layout.title = max(layout.title, lipgloss.Width(sessionTitle(item)))
		layout.location = max(layout.location, lipgloss.Width(location(item, localTarget)))
		layout.runtime = max(layout.runtime, lipgloss.Width(runtimeLabel(item, layout.showClients)))
		layout.activity = max(layout.activity, lipgloss.Width(activityAge(now, item.UpdatedAt)))
		if layout.showProvider {
			layout.provider = max(layout.provider, lipgloss.Width(string(item.Provider)))
		}
		if layout.showProject {
			layout.project = max(layout.project, lipgloss.Width(session.Project(item.CWD)))
		}
	}

	fieldCount := 5 // marker, title, location, runtime, activity
	fixed := 2*rowPadding(width) + rowPrefixSize + 1 + layout.runtime + layout.activity
	if layout.showProvider {
		fieldCount++
		fixed += layout.provider
	}
	if layout.showProject {
		fieldCount++
	}
	fixed += (fieldCount - 1) * lipgloss.Width(columnGutter)

	flexible := []int{layout.title, layout.location}
	if layout.showProject {
		flexible = append(flexible, layout.project)
	}
	allocated := allocateWidths(flexible, max(0, width-fixed))
	layout.title, layout.location = allocated[0], allocated[1]
	if layout.showProject {
		layout.project = allocated[2]
	}
	return layout
}

func runtimeLabel(item session.Session, clients bool) string {
	if clients && item.Runtime.State == session.RuntimeAttached {
		return fmt.Sprintf("attached(%d)", item.Runtime.AttachedClients)
	}
	return string(item.Runtime.State)
}
```

- [ ] **Step 4: Render every row from the shared layout**

Move `renderRow` from `view.go` to `row.go`. Build fields in the existing order, right-align runtime and activity, and leave title/provider/location/project unstyled. Give every row the same left and right internal padding so columns do not shift when selection moves:

```go
func (value model) renderRow(item session.Session, layout rowLayout) string {
	selected := keyOf(item) == value.selectedKey
	cursor := "  "
	if selected {
		cursor = "> "
		if !value.noColor {
			cursor = value.styles.selectedCursor.Render(cursor)
		}
	}
	marker := "∙"
	if item.Runtime.State != session.RuntimeSaved {
		marker = "✻"
	}
	fields := []string{
		value.stateText(marker, item.Runtime.State),
		column(sessionTitle(item), layout.title, false),
	}
	if layout.showProvider {
		fields = append(fields, column(string(item.Provider), layout.provider, false))
	}
	fields = append(fields, column(location(item, value.deps.LocalTarget), layout.location, false))
	if layout.showProject {
		fields = append(fields, column(session.Project(item.CWD), layout.project, false))
	}
	fields = append(fields,
		column(value.stateText(runtimeLabel(item, layout.showClients), item.Runtime.State), layout.runtime, true),
		column(activityAge(value.deps.Now(), item.UpdatedAt), layout.activity, true),
	)

	padding := rowPadding(layout.width)
	innerWidth := layout.width - 2*padding
	row := fitLine(cursor+strings.Join(fields, columnGutter), innerWidth)
	row = strings.Repeat(" ", padding) + row
	row += strings.Repeat(" ", max(0, layout.width-padding-lipgloss.Width(row)))
	row += strings.Repeat(" ", padding)
	if selected && !value.noColor {
		row = value.styles.selected.Render(row)
	}
	return row
}
```

Compute one layout in `sessionLines` and pass it to both Active and Recent groups. Remove the superseded `truncateFlexibleFields` and old `renderRow` from `view.go`.

- [ ] **Step 5: Apply the horizontal frame once in `View`**

Start `View` with:

```go
terminalWidth := value.contentWidth()
inset, width := contentFrame(terminalWidth)
```

Keep all existing height calculations in usable `width`. Immediately before returning the `tea.View`, prepend `strings.Repeat(" ", inset)` to each non-empty line. This leaves an implicit one-column right inset because every content line is bounded to `terminalWidth - 2`.

Update existing test helpers to call `strings.TrimLeft(line, " ")` before checking the selected cursor. Do not weaken any maximum-width or narrow-column assertions.

- [ ] **Step 6: Run the row and package tests**

Run: `go test ./internal/tui`

Expected: PASS, including existing narrow-width, small-height, local-presentation, search, and attach tests.

- [ ] **Step 7: Commit balanced spacing**

```bash
git add internal/tui/row.go internal/tui/row_test.go internal/tui/view.go internal/tui/view_test.go
git commit -m "feat: balance tui row spacing"
```

---

### Task 3: Apply semantic hierarchy and close verification

**Files:**
- Modify: `internal/tui/view.go:22-362`
- Modify: `internal/tui/view_test.go`
- Verify: `internal/tui/model_test.go`
- Verify: `internal/tui/pty_integration_test.go`

**Interfaces:**
- Consumes: `model.styles` from Task 1 and the framed, aligned row renderer from Task 2.
- Produces: the final styled `View` with normal identity foreground, semantic state colors, muted secondary UI, and ANSI-free `NO_COLOR` output.

- [ ] **Step 1: Write failing hierarchy and vertical-rhythm tests**

Add focused tests:

```go
func TestViewKeepsBalancedVerticalRhythm(t *testing.T) {
	value := readyModel()
	value.width, value.height = 120, 24
	plain := ansi.Strip(value.View().Content)
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
```

Retain `TestViewRendersOneLineGroupsAndNeutralProviderLocation` as the regression that provider and location remain neutral identity text.

- [ ] **Step 2: Run focused tests and verify the red state**

Run:

```bash
go test ./internal/tui -run 'Test(ViewKeepsBalancedVerticalRhythm|NoColorPreservesSelectionAndStateWithoutANSI|SecondaryUIUsesHierarchyStyles|ViewRendersOneLineGroupsAndNeutralProviderLocation)$'
```

Expected: `TestSecondaryUIUsesHierarchyStyles` FAILS because header and help do not yet use the semantic style set.

- [ ] **Step 3: Replace ad-hoc styles with semantic styles**

Keep the existing counters, but render `ars` through `styles.title` and the statistics through `styles.muted`. Replace `stateText` and `errorText` with:

```go
func (value model) stateText(text string, state session.RuntimeState) string {
	if value.noColor {
		return text
	}
	switch state {
	case session.RuntimeAttached:
		return value.styles.attached.Render(text)
	case session.RuntimeRunning:
		return value.styles.running.Render(text)
	default:
		return value.styles.saved.Render(text)
	}
}

func (value model) mutedText(text string, width int) string {
	text = fitLine(text, width)
	if value.noColor {
		return text
	}
	return value.styles.muted.Render(text)
}

func (value model) errorText(text string, width int) string {
	text = fitLine(text, width)
	if value.noColor {
		return text
	}
	return value.styles.failure.Render(text)
}
```

Render details, warnings, non-error status, and help through `mutedText`. Keep search text normal and render only the active `/` prefix with `styles.selectedCursor`. Do not style provider, location, project, or title fields.

- [ ] **Step 4: Format and run focused verification**

Run:

```bash
gofmt -w internal/tui/model.go internal/tui/model_test.go internal/tui/styles.go internal/tui/styles_test.go internal/tui/row.go internal/tui/row_test.go internal/tui/view.go internal/tui/view_test.go
go test ./internal/tui
```

Expected: both commands exit 0 and every `internal/tui` test passes.

- [ ] **Step 5: Run repository-wide automated proof**

Run:

```bash
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

Expected: all commands exit 0 with no test failures, race reports, vet findings, or whitespace errors.

- [ ] **Step 6: Inspect the real TUI separately**

Run `go run ./cmd/ars` in a real terminal with provider metadata. Check a wide and narrow resize, selection contrast, aligned runtime/activity columns, navigation, search, refresh, quit, and attach.

If real metadata or an attachable session is unavailable, report those interactive checks as blocked or waived. Do not substitute automated tests for live acceptance.

- [ ] **Step 7: Commit the final hierarchy**

```bash
git add internal/tui/model.go internal/tui/model_test.go internal/tui/styles.go internal/tui/styles_test.go internal/tui/row.go internal/tui/row_test.go internal/tui/view.go internal/tui/view_test.go
git commit -m "feat: polish ars tui hierarchy"
```
