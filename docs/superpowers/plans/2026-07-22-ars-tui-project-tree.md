# ARS TUI Project Tree Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the flat Active/Recent session list with a project-grouped collapsible tree whose headers and session rows share one cursor.

**Architecture:** Add a pure row-building layer (`internal/tui/tree.go`) that flattens filtered sessions into header and session rows honoring collapse state, move model selection onto that unified row slice, and render headers plus tree-guided session rows from the existing shared row layout. Everything stays inside `internal/tui`.

**Tech Stack:** Go 1.26, Bubble Tea v2.0.8, Lip Gloss v2.0.5, `github.com/charmbracelet/x/ansi`, Go `testing`

## Prerequisite

Execute `docs/superpowers/plans/2026-07-22-ars-tui-visual-balance.md` first. This plan modifies the files it creates (`internal/tui/styles.go`, `internal/tui/row.go`) and assumes its symbols exist: `viewStyles`, `model.styles`, `rowLayout`, `newRowLayout`, `contentFrame`, `rowPadding`, `column`, `allocateWidths`, `runtimeLabel`, `columnGutter`, `rowPrefixSize`.

## Global Constraints

- Group by `session.Project(CWD)`; groups derive from sessions, so empty groups never render; zero sessions keep the single `none` line.
- Groups with a non-saved session sort first, then by most recent session activity descending; inside a group non-saved sessions sort first, then most recent activity descending.
- Header format: `▾ name (count)` expanded, `▸ name (count)` collapsed; a collapsed group holding a non-saved session appends ` ✻` styled with the group's strongest runtime state (attached over running).
- Session rows drop the project column permanently and prefix tree guides `├─ ` (non-last) / `└─ ` (last); the untitled fallback title is `NativeID[:8]` alone.
- One cursor across headers and session rows; `enter`/`space` on a header toggles (never attaches); `enter` on a session attaches; selection keeps the `> ` prefix plus adaptive cyan background, prefix only under `NO_COLOR`.
- All groups start expanded; collapse state keys on project name, lives only in memory, survives refreshes, resets on restart.
- When the row under the cursor disappears, selection prefers the same key, then the session's group header, then the nearest remaining index.
- While a `/` query is active, collapse state is ignored, matching sessions render under their headers, groups without matches are hidden; clearing the query restores collapse state.
- Active/Recent subheadings disappear; header statistics, details, diagnostics, search, help, vertical rhythm, responsive column removal, width fitting, and `NO_COLOR` rules from the visual-balance plan stay in force.
- Every rendered line, including guides and headers, stays within the reported terminal width.
- No widget library, theme system, or configuration surface.

---

### Task 1: Pure row builder

**Files:**
- Create: `internal/tui/tree.go`
- Create: `internal/tui/tree_test.go`

**Interfaces:**
- Consumes: `session.Session`, `session.Project(string) string`, `runtimeOrder(session.RuntimeState) int` (filter.go), `sessionKey`/`keyOf` (filter.go).
- Produces: `type rowKind int` (`rowHeader`, `rowSession`), `type listRow struct{ kind rowKind; project string; count int; state session.RuntimeState; collapsed bool; last bool; session session.Session }`, `type rowRef struct{ kind rowKind; project string; key sessionKey }`, `func refOf(listRow) rowRef`, `func buildRows(items []session.Session, collapsed map[string]bool, searchActive bool) []listRow`.

- [ ] **Step 1: Write failing grouping, ordering, collapse, and search tests**

Create `internal/tui/tree_test.go`:

```go
package tui

import (
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func treeSession(project, id string, state session.RuntimeState, updated time.Time) session.Session {
	return session.Session{
		Host: "localhost",
		Candidate: session.Candidate{
			Provider:  session.Claude,
			NativeID:  id,
			UpdatedAt: updated,
			CWD:       "/work/" + project,
			Title:     id,
		},
		Runtime: session.Runtime{State: state},
	}
}

func TestBuildRowsGroupsAndOrdersByStateThenActivity(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	items := []session.Session{
		treeSession("blog", "blog-old", session.RuntimeSaved, base.Add(-3*time.Hour)),
		treeSession("ars", "ars-saved", session.RuntimeSaved, base),
		treeSession("ars", "ars-live", session.RuntimeRunning, base.Add(-2*time.Hour)),
	}
	rows := buildRows(items, nil, false)
	want := []struct {
		kind    rowKind
		project string
		id      string
		last    bool
	}{
		{rowHeader, "ars", "", false},
		{rowSession, "ars", "ars-live", false},
		{rowSession, "ars", "ars-saved", true},
		{rowHeader, "blog", "", false},
		{rowSession, "blog", "blog-old", true},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %d, want %d", len(rows), len(want))
	}
	for index, expect := range want {
		row := rows[index]
		if row.kind != expect.kind || row.project != expect.project || row.last != expect.last {
			t.Fatalf("row %d = %+v, want %+v", index, row, expect)
		}
		if expect.kind == rowSession && row.session.NativeID != expect.id {
			t.Fatalf("row %d id = %s, want %s", index, row.session.NativeID, expect.id)
		}
	}
	if rows[0].count != 2 || rows[0].state != session.RuntimeRunning {
		t.Fatalf("ars header = %+v", rows[0])
	}
	if rows[3].count != 1 || rows[3].state != session.RuntimeSaved {
		t.Fatalf("blog header = %+v", rows[3])
	}
}

func TestBuildRowsCollapseHidesSessionsUnlessSearching(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	items := []session.Session{
		treeSession("ars", "ars-live", session.RuntimeRunning, base),
	}
	collapsed := map[string]bool{"ars": true}
	rows := buildRows(items, collapsed, false)
	if len(rows) != 1 || !rows[0].collapsed {
		t.Fatalf("collapsed rows = %+v", rows)
	}
	rows = buildRows(items, collapsed, true)
	if len(rows) != 2 || rows[0].collapsed {
		t.Fatalf("search rows = %+v", rows)
	}
}

func TestRefOfDistinguishesHeadersAndSessions(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	item := treeSession("ars", "ars-live", session.RuntimeRunning, base)
	rows := buildRows([]session.Session{item}, nil, false)
	header, leaf := refOf(rows[0]), refOf(rows[1])
	if header.kind != rowHeader || header.project != "ars" || header.key != (sessionKey{}) {
		t.Fatalf("header ref = %+v", header)
	}
	if leaf.kind != rowSession || leaf.key != keyOf(item) {
		t.Fatalf("session ref = %+v", leaf)
	}
}
```

- [ ] **Step 2: Run the tests and verify the red state**

Run:

```bash
go test ./internal/tui -run 'Test(BuildRowsGroupsAndOrdersByStateThenActivity|BuildRowsCollapseHidesSessionsUnlessSearching|RefOfDistinguishesHeadersAndSessions)$'
```

Expected: FAIL to compile because `buildRows`, `listRow`, `rowRef`, and `refOf` do not exist.

- [ ] **Step 3: Implement `internal/tui/tree.go`**

```go
package tui

import (
	"sort"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

type rowKind int

const (
	rowHeader rowKind = iota
	rowSession
)

type listRow struct {
	kind      rowKind
	project   string
	count     int
	state     session.RuntimeState
	collapsed bool
	last      bool
	session   session.Session
}

type rowRef struct {
	kind    rowKind
	project string
	key     sessionKey
}

func refOf(row listRow) rowRef {
	if row.kind == rowHeader {
		return rowRef{kind: rowHeader, project: row.project}
	}
	return rowRef{kind: rowSession, project: row.project, key: keyOf(row.session)}
}

type sessionGroup struct {
	project  string
	sessions []session.Session
}

func buildRows(items []session.Session, collapsed map[string]bool, searchActive bool) []listRow {
	var rows []listRow
	for _, group := range groupSessions(items) {
		folded := collapsed[group.project] && !searchActive
		rows = append(rows, listRow{
			kind:      rowHeader,
			project:   group.project,
			count:     len(group.sessions),
			state:     groupState(group.sessions),
			collapsed: folded,
		})
		if folded {
			continue
		}
		for position, item := range group.sessions {
			rows = append(rows, listRow{
				kind:    rowSession,
				project: group.project,
				session: item,
				last:    position == len(group.sessions)-1,
			})
		}
	}
	return rows
}

func groupSessions(items []session.Session) []sessionGroup {
	positions := make(map[string]int)
	var groups []sessionGroup
	for _, item := range items {
		project := session.Project(item.CWD)
		position, seen := positions[project]
		if !seen {
			position = len(groups)
			positions[project] = position
			groups = append(groups, sessionGroup{project: project})
		}
		groups[position].sessions = append(groups[position].sessions, item)
	}
	for _, group := range groups {
		items := group.sessions
		sort.SliceStable(items, func(left, right int) bool {
			leftSaved := items[left].Runtime.State == session.RuntimeSaved
			rightSaved := items[right].Runtime.State == session.RuntimeSaved
			if leftSaved != rightSaved {
				return rightSaved
			}
			return items[left].UpdatedAt.After(items[right].UpdatedAt)
		})
	}
	sort.SliceStable(groups, func(left, right int) bool {
		leftActive := groupState(groups[left].sessions) != session.RuntimeSaved
		rightActive := groupState(groups[right].sessions) != session.RuntimeSaved
		if leftActive != rightActive {
			return leftActive
		}
		return latestActivity(groups[left].sessions).After(latestActivity(groups[right].sessions))
	})
	return groups
}

func groupState(items []session.Session) session.RuntimeState {
	strongest := session.RuntimeSaved
	for _, item := range items {
		if runtimeOrder(item.Runtime.State) < runtimeOrder(strongest) {
			strongest = item.Runtime.State
		}
	}
	return strongest
}

func latestActivity(items []session.Session) time.Time {
	var latest time.Time
	for _, item := range items {
		if item.UpdatedAt.After(latest) {
			latest = item.UpdatedAt
		}
	}
	return latest
}
```

- [ ] **Step 4: Run the tests and verify they pass**

Run: `go test ./internal/tui -run 'Test(BuildRowsGroupsAndOrdersByStateThenActivity|BuildRowsCollapseHidesSessionsUnlessSearching|RefOfDistinguishesHeadersAndSessions)$'`

Expected: PASS. The rest of the package still compiles because nothing consumes the new symbols yet.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/tree.go internal/tui/tree_test.go
git commit -m "feat: build project tree rows"
```

---

### Task 2: Model navigation over unified rows

**Files:**
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/filter.go` (remove `displayOrder`/`runtimeOrder` consumers change: keep `runtimeOrder`, delete `displayOrder`)
- Modify: `internal/tui/view.go` (only `selectedSession` and the `sessionLines` call sites needed to keep the package compiling; full rendering lands in Task 3)

**Interfaces:**
- Consumes: `buildRows`, `listRow`, `rowRef`, `refOf` from Task 1.
- Produces: `model.rows []listRow`, `model.collapsed map[string]bool`, `model.selected int`, `model.selectedRef rowRef`, `func (value *model) refreshVisible()`, `func (value *model) toggle(project string)`, `func (value model) selectedRow() (listRow, bool)`.

- [ ] **Step 1: Write failing model tests**

In `internal/tui/model_test.go`, add:

```go
func TestModelCursorCoversHeadersAndSessions(t *testing.T) {
	value := readyModel()
	// twoSessions: attached "connection check" in /work/ars, saved "API repair" in /work/api.
	kinds := make([]rowKind, 0, len(value.rows))
	for _, row := range value.rows {
		kinds = append(kinds, row.kind)
	}
	want := []rowKind{rowHeader, rowSession, rowHeader, rowSession}
	if len(kinds) != len(want) {
		t.Fatalf("rows = %+v", value.rows)
	}
	for index := range want {
		if kinds[index] != want[index] {
			t.Fatalf("row kinds = %v, want %v", kinds, want)
		}
	}
	if value.selected != 1 {
		t.Fatalf("initial selection = %d, want first session row", value.selected)
	}
	value, _ = updateModel(value, tea.KeyPressMsg{Code: 'k'})
	if row, _ := value.selectedRow(); row.kind != rowHeader || row.project != "ars" {
		t.Fatalf("k did not land on header: %+v", row)
	}
}

func TestModelEnterOnHeaderTogglesWithoutAttaching(t *testing.T) {
	attached := 0
	value := readyModel()
	value.deps.Attach = func(context.Context, session.Session) (ExecCommand, error) {
		attached++
		return &fakeExecCommand{}, nil
	}
	value, _ = updateModel(value, tea.KeyPressMsg{Code: 'k'}) // header "ars"
	value, command := updateModel(value, tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil || attached != 0 {
		t.Fatal("enter on header attached")
	}
	if len(value.rows) != 3 || !value.rows[0].collapsed {
		t.Fatalf("group did not collapse: %+v", value.rows)
	}
	if row, _ := value.selectedRow(); row.kind != rowHeader || row.project != "ars" {
		t.Fatalf("selection left the toggled header: %+v", row)
	}
	value, _ = updateModel(value, tea.KeyPressMsg{Code: tea.KeySpace})
	if len(value.rows) != 4 {
		t.Fatalf("space did not expand: %+v", value.rows)
	}
}

func TestModelCollapseSurvivesRefreshAndSearchOverridesIt(t *testing.T) {
	value := readyModel()
	value, _ = updateModel(value, tea.KeyPressMsg{Code: 'k'})
	value, _ = updateModel(value, tea.KeyPressMsg{Code: tea.KeyEnter}) // collapse ars
	value, command := updateModel(value, tea.KeyPressMsg{Code: 'r'})
	value, _ = updateModel(value, command().(collectUpdateMsg))
	if len(value.rows) != 3 || !value.rows[0].collapsed {
		t.Fatalf("collapse lost on refresh: %+v", value.rows)
	}
	value, _ = updateModel(value, tea.KeyPressMsg{Code: '/'})
	for _, r := range "connection" {
		value, _ = updateModel(value, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if len(value.rows) != 2 || value.rows[1].session.Title != "connection check" {
		t.Fatalf("search did not override collapse: %+v", value.rows)
	}
	value, _ = updateModel(value, tea.KeyPressMsg{Code: tea.KeyEscape})
	for range "connection" {
		value, _ = updateModel(value, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	if len(value.rows) != 3 || !value.rows[0].collapsed {
		t.Fatalf("collapse not restored after search: %+v", value.rows)
	}
}

func TestModelSelectionFallsBackToHeaderWhenRowVanishes(t *testing.T) {
	value := readyModel()
	// initial selection: session "connection check" inside ars group
	header := value.rows[0]
	if header.project != "ars" {
		t.Fatalf("unexpected first group: %+v", header)
	}
	value.toggle("ars")
	if row, _ := value.selectedRow(); row.kind != rowHeader || row.project != "ars" {
		t.Fatalf("selection did not fall back to header: %+v", row)
	}
}
```

Update existing tests that index `value.visible` or assume session-only navigation:

- `TestModelInitialCollectionNavigatesFiltersAndAttaches`: navigation now walks 4 rows (`header ars`, `connection check`, `header api`, `API repair`); initial selection is row 1; pressing `j` twice lands on `API repair` (row 3); enter attaches it. Filter assertions move from `value.visible` to counting `rowSession` rows in `value.rows`.
- `TestModelRefreshPreservesCanonicalSelection`: read the selected session through `value.selectedRow()`.
- Any helper using `value.visible[...]` switches to `value.rows`.

- [ ] **Step 2: Run the tests and verify the red state**

Run: `go test ./internal/tui`

Expected: FAIL to compile (`value.rows`, `selectedRow`, `toggle` undefined).

- [ ] **Step 3: Implement the model changes**

In `internal/tui/model.go`:

Replace the `visible []session.Session` and `selectedKey sessionKey` fields:

```go
rows        []listRow
selected    int
selectedRef rowRef
collapsed   map[string]bool
```

Replace `refreshVisible`:

```go
func (value *model) refreshVisible() {
	filtered := filterSessions(value.result.Sessions, value.query, value.deps.LocalTarget)
	value.rows = buildRows(filtered, value.collapsed, value.query != "")
	value.restoreSelection()
}

func (value *model) restoreSelection() {
	if len(value.rows) == 0 {
		value.selected = 0
		value.selectedRef = rowRef{}
		return
	}
	if value.selectedRef == (rowRef{}) {
		value.selectRow(firstSessionRow(value.rows))
		return
	}
	for index, row := range value.rows {
		if refOf(row) == value.selectedRef {
			value.selectRow(index)
			return
		}
	}
	if value.selectedRef.kind == rowSession {
		for index, row := range value.rows {
			if row.kind == rowHeader && row.project == value.selectedRef.project {
				value.selectRow(index)
				return
			}
		}
	}
	index := value.selected
	if index >= len(value.rows) {
		index = len(value.rows) - 1
	}
	if index < 0 {
		index = 0
	}
	value.selectRow(index)
}

func (value *model) selectRow(index int) {
	value.selected = index
	value.selectedRef = refOf(value.rows[index])
}

func firstSessionRow(rows []listRow) int {
	for index, row := range rows {
		if row.kind == rowSession {
			return index
		}
	}
	return 0
}

func (value model) selectedRow() (listRow, bool) {
	if value.selected < 0 || value.selected >= len(value.rows) {
		return listRow{}, false
	}
	return value.rows[value.selected], true
}

func (value *model) toggle(project string) {
	if value.collapsed == nil {
		value.collapsed = make(map[string]bool)
	}
	value.collapsed[project] = !value.collapsed[project]
	value.selectedRef = rowRef{kind: rowHeader, project: project}
	value.refreshVisible()
}
```

Replace `move`:

```go
func (value *model) move(delta int) {
	if len(value.rows) == 0 {
		return
	}
	value.selectRow((value.selected + delta + len(value.rows)) % len(value.rows))
}
```

In `updateKey`, replace the `tea.KeyEnter` case and add `tea.KeySpace`:

```go
case tea.KeyEnter:
	row, ok := value.selectedRow()
	if !ok {
		return value, nil
	}
	if row.kind == rowHeader {
		value.toggle(row.project)
		return value, nil
	}
	command, err := value.deps.Attach(value.ctx, row.session)
	if err != nil {
		return updateModel(value, attachDoneMsg{err: err})
	}
	return value, tea.Exec(command, func(err error) tea.Msg {
		return attachDoneMsg{err: err}
	})
case tea.KeySpace:
	if row, ok := value.selectedRow(); ok && row.kind == rowHeader {
		value.toggle(row.project)
	}
	return value, nil
```

In `internal/tui/filter.go`, delete `displayOrder` (now unused; ordering lives in `groupSessions`). Keep `runtimeOrder` — `groupState` uses it.

In `internal/tui/view.go`, update `selectedSession` so the package compiles (full render lands in Task 3):

```go
func (value model) selectedSession() (session.Session, bool) {
	row, ok := value.selectedRow()
	if !ok || row.kind != rowSession {
		return session.Session{}, false
	}
	return row.session, true
}
```

and give `sessionLines` a temporary body that renders only session rows from `value.rows` if needed to compile; Task 3 replaces it entirely.

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/tui`

Expected: PASS for all model and tree tests. View tests asserting Active/Recent headings may fail; if they do, defer them by updating only after Task 3 — but prefer running Task 3 immediately rather than skipping tests. Do not commit with failing tests: if view tests fail here, complete Task 3 Step 3 first and commit Tasks 2–3 changes at their own steps.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/model_test.go internal/tui/filter.go internal/tui/view.go
git commit -m "feat: navigate unified tree rows"
```

---

### Task 3: Render headers and tree guides

**Files:**
- Modify: `internal/tui/view.go`
- Modify: `internal/tui/view_test.go`
- Modify: `internal/tui/row.go`
- Modify: `internal/tui/row_test.go`
- Modify: `internal/tui/filter_test.go` (drop `displayOrder` tests if present)

**Interfaces:**
- Consumes: `model.rows`, `model.selected`, `listRow`, `rowLayout`, `viewStyles`.
- Produces: `func (value model) renderHeader(row listRow, selected bool, width int) string`, `func (value model) renderRow(row listRow, selected bool, layout rowLayout) string` (signature change), tree-aware `sessionLines`.

- [ ] **Step 1: Write failing view tests**

In `internal/tui/view_test.go`, replace heading-based assertions and add:

```go
func TestViewGroupsSessionsUnderProjectHeaders(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, true
	plain := ansi.Strip(value.View().Content)
	arsAt := strings.Index(plain, "▾ ars (1)")
	apiAt := strings.Index(plain, "▾ api (1)")
	if arsAt == -1 || apiAt == -1 || arsAt > apiAt {
		t.Fatalf("headers missing or misordered:\n%s", plain)
	}
	if !strings.Contains(plain, "└─ ✻ connection check") {
		t.Fatalf("missing tree guide session row:\n%s", plain)
	}
	if strings.Contains(plain, "Active") || strings.Contains(plain, "Recent") {
		t.Fatalf("legacy headings remain:\n%s", plain)
	}
}

func TestViewCollapsedHeaderShowsCountAndActiveMarker(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 120, 24, true
	value.toggle("ars")
	plain := ansi.Strip(value.View().Content)
	if !strings.Contains(plain, "▸ ars (1) ✻") {
		t.Fatalf("collapsed header missing marker:\n%s", plain)
	}
	if strings.Contains(plain, "connection check") {
		t.Fatalf("collapsed session still rendered:\n%s", plain)
	}
}

func TestViewTreeGuidesMarkLastSession(t *testing.T) {
	value := readyModel()
	items := twoSessions()
	items[1].CWD = items[0].CWD // same project
	value.result.Sessions = items
	value.width, value.height, value.noColor = 120, 24, true
	value.refreshVisible()
	plain := ansi.Strip(value.View().Content)
	if !strings.Contains(plain, "├─ ✻ connection check") ||
		!strings.Contains(plain, "└─ ∙ API repair") {
		t.Fatalf("guides wrong:\n%s", plain)
	}
}

func TestViewLinesStayWithinWidthWithTree(t *testing.T) {
	value := readyModel()
	value.width, value.height, value.noColor = 46, 12, true
	for _, line := range strings.Split(value.View().Content, "\n") {
		if ansi.StringWidth(line) > value.width {
			t.Fatalf("line exceeds width %d: %q", value.width, line)
		}
	}
}

func TestViewUntitledFallbackUsesShortID(t *testing.T) {
	value := readyModel()
	items := twoSessions()
	items[0].Title = ""
	value.result.Sessions = items
	value.width, value.height, value.noColor = 120, 24, true
	value.refreshVisible()
	plain := ansi.Strip(value.View().Content)
	if !strings.Contains(plain, "123e4567") {
		t.Fatalf("missing short id fallback:\n%s", plain)
	}
	if strings.Contains(plain, "ars · 123e4567") {
		t.Fatalf("fallback still includes project:\n%s", plain)
	}
}
```

Update the surviving tests: assertions on `Active`/`Recent`, per-row `project` columns, and the old `sessionTitle` fallback move to the tree equivalents; `selectedRow`/`activeRow` helpers now match rows whose stripped form starts with `> ` after trimming the inset.

- [ ] **Step 2: Run the tests and verify the red state**

Run: `go test ./internal/tui`

Expected: FAIL on the new tree-view tests (headers and guides not rendered yet).

- [ ] **Step 3: Implement rendering**

In `internal/tui/row.go`:

- Remove `showProject`/`project` from `rowLayout` and `newRowLayout` (drop the `projectColumnWidth` threshold entirely) and remove the project field from rendering.
- Add the tree guide to the fixed budget: `const treeGuide = 3` and include `treeGuide` in `fixed`.
- Change `renderRow` to the new signature and prepend the guide:

```go
func (value model) renderRow(row listRow, selected bool, layout rowLayout) string {
	item := row.session
	cursor := "  "
	if selected {
		cursor = "> "
		if !value.noColor {
			cursor = value.styles.selectedCursor.Render(cursor)
		}
	}
	guide := "├─ "
	if row.last {
		guide = "└─ "
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
	fields = append(fields,
		column(value.stateText(runtimeLabel(item, layout.showClients), item.Runtime.State), layout.runtime, true),
		column(activityAge(value.deps.Now(), item.UpdatedAt), layout.activity, true),
	)

	padding := rowPadding(layout.width)
	innerWidth := layout.width - 2*padding
	line := fitLine(cursor+guide+strings.Join(fields, columnGutter), innerWidth)
	line = strings.Repeat(" ", padding) + line
	line += strings.Repeat(" ", max(0, layout.width-padding-lipgloss.Width(line)))
	line += strings.Repeat(" ", padding)
	if selected && !value.noColor {
		line = value.styles.selected.Render(line)
	}
	return line
}
```

In `internal/tui/view.go`:

- Replace `sessionLines`, delete `splitSessions`, `renderGroup`, and the Active/Recent `stateText` headings:

```go
func (value model) sessionLines(width int) ([]string, int) {
	if len(value.rows) == 0 {
		return []string{"  none"}, 0
	}
	layout := newRowLayout(rowSessions(value.rows), width, value.deps.Now(), value.deps.LocalTarget)
	lines := make([]string, 0, len(value.rows))
	for index, row := range value.rows {
		selected := index == value.selected
		if row.kind == rowHeader {
			lines = append(lines, value.renderHeader(row, selected, width))
			continue
		}
		lines = append(lines, value.renderRow(row, selected, layout))
	}
	return lines, value.selected
}

func rowSessions(rows []listRow) []session.Session {
	items := make([]session.Session, 0, len(rows))
	for _, row := range rows {
		if row.kind == rowSession {
			items = append(items, row.session)
		}
	}
	return items
}

func (value model) renderHeader(row listRow, selected bool, width int) string {
	cursor := "  "
	if selected {
		cursor = "> "
		if !value.noColor {
			cursor = value.styles.selectedCursor.Render(cursor)
		}
	}
	symbol := "▾"
	if row.collapsed {
		symbol = "▸"
	}
	text := fmt.Sprintf("%s %s (%d)", symbol, row.project, row.count)
	if row.collapsed && row.state != session.RuntimeSaved {
		text += " " + value.stateText("✻", row.state)
	}
	padding := rowPadding(width)
	line := fitLine(cursor+text, width-2*padding)
	line = strings.Repeat(" ", padding) + line
	line += strings.Repeat(" ", max(0, width-padding-lipgloss.Width(line)))
	line += strings.Repeat(" ", padding)
	if selected && !value.noColor {
		line = value.styles.selected.Render(line)
	}
	return line
}
```

- `sessionLines` already returns `value.selected` as the selected line index, so drop the `ansi.Strip`/`HasPrefix` scan in the caller.
- In `sessionTitle`, replace the fallback with `return item.NativeID[:8]`.
- Update the stale-marker rendering (`cached` suffix) to append after the activity column exactly as before.

Also update `internal/tui/row_test.go`: alignment tests stop asserting a project column and account for the three-cell guide, and `internal/tui/filter_test.go` drops `displayOrder` coverage if present.

- [ ] **Step 4: Format and run the package tests**

Run:

```bash
gofmt -w internal/tui
go test ./internal/tui
```

Expected: PASS.

- [ ] **Step 5: Repository-wide verification**

Run:

```bash
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

Expected: all exit 0.

- [ ] **Step 6: Inspect the real TUI separately**

Run `go run ./cmd/ars` in a real terminal. Check header cursor movement, enter/space toggling, collapsed `✻` marker, search auto-expand and restore, attach from a session row, and narrow resize. Report interactive acceptance separately; if no live sessions are available, report those checks as waived.

- [ ] **Step 7: Commit**

```bash
git add internal/tui
git commit -m "feat: render project tree tui"
```
