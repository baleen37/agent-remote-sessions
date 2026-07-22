# TUI Active-Partial Tree + Codex Titles Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Codex sessions get titles from their first user message, and the session tree starts with only active sessions visible (active groups partially expanded with a `… N more` row, inactive groups collapsed).

**Architecture:** Two independent changes. (1) `internal/provider/codex.go` additionally parses `event_msg`/`user_message` lines while scanning a session file and normalizes the first one into a title. (2) `internal/tui` replaces the boolean per-project collapse map with a three-state `groupMode` (auto/open/closed); `buildRows` emits a new `rowMore` row kind for auto-mode groups that have hidden saved sessions.

**Tech Stack:** Go 1.x, bubbletea v2, lipgloss v2. Test with the standard `testing` package (no assertion libs).

**Spec:** `docs/superpowers/specs/2026-07-22-tui-active-partial-tree-codex-title-design.md`

## Global Constraints

- Every commit must leave `go build ./...` and `go test ./...` green — the `internal/tui` behavior change and its test updates (including `pty_integration_test.go`) must land in the same commit.
- "Active" means `Runtime.State != session.RuntimeSaved` (i.e. `running` or `attached`).
- Titles must satisfy `session.ValidateCandidate`: valid UTF-8, ≤ `session.MaxTitleBytes` (1024) bytes, no `unicode.IsControl` runes. `codexTitle` guarantees this by construction (strips controls, bounds bytes), so no second validation pass is needed — a deliberate simplification of the spec's "drop title on validation failure" fallback, which becomes unreachable.
- Match existing style: value receivers named `value`, table tests with `t.Run`, `t.Fatalf` with `got, want` phrasing, no new dependencies.
- Run `gofmt -l internal/` (expect no output) and `go vet ./...` before each commit.

---

### Task 1: Codex title from first user message

**Files:**
- Modify: `internal/provider/codex.go`
- Test: `internal/provider/codex_test.go`

**Interfaces:**
- Consumes: `session.Candidate`, `session.MaxTitleBytes`, existing `readHistory` scan loop.
- Produces: codex `session.Candidate.Title` populated from the first `user_message`; unexported helper `codexTitle(message string) string` (returns "" when nothing usable). No API change — later tasks rely only on `Title` being non-empty sometimes, which the TUI already renders via `sessionTitle`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/provider/codex_test.go` (add `"encoding/json"`, `"strings"`, `"unicode/utf8"` to the test file imports):

```go
func TestCodexDiscoverTitlesSessionsFromFirstUserMessage(t *testing.T) {
	home := t.TempDir()
	installExecutable(t, "codex")
	id := fixtureID(1)
	writeFile(t, filepath.Join(home, ".codex", "sessions", "titled.jsonl"),
		codexMeta(id, "/synthetic/codex/titled", "cli", "user")+
			codexUserMessage("fix the flaky test\nplus more context")+
			codexUserMessage("second message is ignored"))

	result := (codexAdapter{}).Discover(context.Background(), home)
	if result.Status != OK || len(result.Sessions) != 1 {
		t.Fatalf("Discover() = %#v, want one OK session", result)
	}
	if got := result.Sessions[0].Title; got != "fix the flaky test" {
		t.Fatalf("Title = %q, want first line of first user message", got)
	}
}

func TestCodexTitleNormalizesAndBounds(t *testing.T) {
	tests := []struct{ name, message, want string }{
		{name: "first line only", message: "line one\nline two", want: "line one"},
		{name: "controls become spaces", message: "\t do\tthing \r", want: "do thing"},
		{name: "whitespace only", message: "   \n\t", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexTitle(tt.message); got != tt.want {
				t.Fatalf("codexTitle(%q) = %q, want %q", tt.message, got, tt.want)
			}
		})
	}

	long := strings.Repeat("가", session.MaxTitleBytes)
	got := codexTitle(long)
	if got == "" || len(got) > session.MaxTitleBytes || !utf8.ValidString(got) || !strings.HasPrefix(long, got) {
		t.Fatalf("codexTitle(long) = %d bytes, want non-empty bounded valid UTF-8 prefix", len(got))
	}
	if err := session.ValidateCandidate(session.Candidate{
		Provider: session.Codex, NativeID: fixtureID(1),
		UpdatedAt: time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC),
		CWD:       "/synthetic/codex", Title: got,
	}); err != nil {
		t.Fatalf("normalized title fails validation: %v", err)
	}
}
```

And next to the existing `codexMeta` helper at the bottom of the file:

```go
func codexUserMessage(message string) string {
	payload, err := json.Marshal(map[string]any{"type": "user_message", "message": message})
	if err != nil {
		panic(err)
	}
	return `{"type":"event_msg","payload":` + string(payload) + "}\n"
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/provider -run 'TestCodexDiscoverTitlesSessionsFromFirstUserMessage|TestCodexTitleNormalizesAndBounds' -v`
Expected: FAIL — `undefined: codexTitle` (compile error).

- [ ] **Step 3: Implement title capture in codex.go**

In `internal/provider/codex.go`:

Add `"strings"`, `"unicode"`, `"unicode/utf8"` to the imports.

Below `codexSessionMeta`, add:

```go
type codexEventMsg struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
```

In `readHistory`, declare a title alongside the existing state (`var meta *codexSessionMeta` block):

```go
	var meta *codexSessionMeta
	title := ""
	multipleMeta := false
```

Inside the scan loop, immediately after the envelope unmarshal succeeds and before the `if envelope.Type != "session_meta"` check, insert:

```go
		if envelope.Type == "event_msg" && title == "" && len(envelope.Payload) > 0 {
			var event codexEventMsg
			if json.Unmarshal(envelope.Payload, &event) == nil && event.Type == "user_message" {
				title = codexTitle(event.Message)
			}
			continue
		}
```

Change the candidate construction from `Title: ""` to `Title: title`.

Add at the bottom of the file:

```go
// codexTitle turns the first user message into a display title that always
// satisfies candidate text validation: single line, no control runes, at most
// MaxTitleBytes bytes.
func codexTitle(message string) string {
	if index := strings.IndexByte(message, '\n'); index >= 0 {
		message = message[:index]
	}
	message = strings.Join(strings.FieldsFunc(message, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
	for len(message) > session.MaxTitleBytes {
		_, size := utf8.DecodeLastRuneInString(message)
		message = message[:len(message)-size]
	}
	return strings.TrimSpace(message)
}
```

- [ ] **Step 4: Run the provider tests**

Run: `go test ./internal/provider -v`
Expected: PASS, including all pre-existing codex tests (the shipped `testdata/codex` fixtures contain no `user_message` events, so their `Title: ""` expectations still hold).

- [ ] **Step 5: Commit**

```bash
gofmt -l internal/ && go vet ./internal/provider
git add internal/provider/codex.go internal/provider/codex_test.go
git commit -m "feat: derive codex session titles from the first user message"
```

---

### Task 2: Group modes — auto-partial expansion with `… N more` rows

This is one task because the `buildRows` signature change ripples through `tree.go`, `model.go`, `view.go`, and every test file in `internal/tui`; intermediate states don't compile.

**Files:**
- Modify: `internal/tui/tree.go`
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/view.go`
- Test: `internal/tui/tree_test.go`, `internal/tui/model_test.go`, `internal/tui/view_test.go`, `internal/tui/pty_integration_test.go`

**Interfaces:**
- Consumes: `session.RuntimeState`, existing `listRow`/`rowRef`/`buildRows` plumbing.
- Produces:
  - `type groupMode int` with constants `groupModeAuto` (zero value), `groupModeOpen`, `groupModeClosed`
  - `rowMore rowKind` constant; more-rows carry `project` and `count` (hidden session count), `last: true`
  - `buildRows(items []session.Session, modes map[string]groupMode, searchActive bool) []listRow`
  - model field `groupMode map[string]groupMode` (replaces `collapsed map[string]bool`)
  - `(value *model) toggle(project string)` — closed if currently rendered expanded, else open
  - `(value *model) openGroup(project string)` — sets open, keeps cursor on the first revealed row
  - `(value model) renderMore(row listRow, selected bool, width int) string`
  - test helper `openAllGroups(value model) model` in `view_test.go`

- [ ] **Step 1: Rewrite tree_test.go for the new API and behaviors**

Replace all three existing tests (`TestBuildRowsGroupsAndOrdersByStateThenActivity`, `TestBuildRowsCollapseHidesSessionsUnlessSearching`, `TestRefOfDistinguishesHeadersAndSessions`) with the set below. Keep the `treeSession` helper and imports as-is:

```go
func TestBuildRowsGroupsAndOrdersByStateThenActivity(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	items := []session.Session{
		treeSession("blog", "blog-old", session.RuntimeSaved, base.Add(-3*time.Hour)),
		treeSession("ars", "ars-saved", session.RuntimeSaved, base),
		treeSession("ars", "ars-live", session.RuntimeRunning, base.Add(-2*time.Hour)),
	}
	modes := map[string]groupMode{"ars": groupModeOpen, "blog": groupModeOpen}
	rows := buildRows(items, modes, false)
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

func TestBuildRowsClosedHidesSessionsUnlessSearching(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	items := []session.Session{
		treeSession("ars", "ars-live", session.RuntimeRunning, base),
	}
	modes := map[string]groupMode{"ars": groupModeClosed}
	rows := buildRows(items, modes, false)
	if len(rows) != 1 || !rows[0].collapsed {
		t.Fatalf("closed rows = %+v", rows)
	}
	rows = buildRows(items, modes, true)
	if len(rows) != 2 || rows[0].collapsed {
		t.Fatalf("search rows = %+v", rows)
	}
}

func TestBuildRowsAutoShowsOnlyActiveWithMoreRow(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	items := []session.Session{
		treeSession("ars", "ars-live", session.RuntimeRunning, base),
		treeSession("ars", "ars-saved", session.RuntimeSaved, base.Add(-time.Hour)),
		treeSession("ars", "ars-older", session.RuntimeSaved, base.Add(-2*time.Hour)),
	}
	rows := buildRows(items, nil, false)
	if len(rows) != 3 {
		t.Fatalf("rows = %+v, want header, active session, more", rows)
	}
	if rows[0].kind != rowHeader || rows[0].collapsed || rows[0].count != 3 {
		t.Fatalf("header = %+v", rows[0])
	}
	if rows[1].kind != rowSession || rows[1].session.NativeID != "ars-live" || rows[1].last {
		t.Fatalf("active row = %+v", rows[1])
	}
	if rows[2].kind != rowMore || rows[2].project != "ars" || rows[2].count != 2 || !rows[2].last {
		t.Fatalf("more row = %+v", rows[2])
	}
}

func TestBuildRowsAutoCollapsesGroupsWithoutActiveSessions(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	items := []session.Session{
		treeSession("blog", "blog-old", session.RuntimeSaved, base),
	}
	rows := buildRows(items, nil, false)
	if len(rows) != 1 || rows[0].kind != rowHeader || !rows[0].collapsed {
		t.Fatalf("rows = %+v, want a single collapsed header", rows)
	}
	rows = buildRows(items, nil, true)
	if len(rows) != 2 || rows[0].collapsed || rows[1].kind != rowSession {
		t.Fatalf("search rows = %+v, want expanded group", rows)
	}
}

func TestBuildRowsAutoAllActiveHasNoMoreRow(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	items := []session.Session{
		treeSession("ars", "ars-live", session.RuntimeRunning, base),
		treeSession("ars", "ars-live2", session.RuntimeAttached, base.Add(-time.Hour)),
	}
	rows := buildRows(items, nil, false)
	if len(rows) != 3 || rows[2].kind != rowSession || !rows[2].last {
		t.Fatalf("rows = %+v, want all active sessions and no more row", rows)
	}
}

func TestRefOfDistinguishesHeadersSessionsAndMoreRows(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	item := treeSession("ars", "ars-live", session.RuntimeRunning, base)
	saved := treeSession("ars", "ars-saved", session.RuntimeSaved, base.Add(-time.Hour))
	rows := buildRows([]session.Session{item, saved}, nil, false)
	header, leaf, more := refOf(rows[0]), refOf(rows[1]), refOf(rows[2])
	if header.kind != rowHeader || header.project != "ars" || header.key != (sessionKey{}) {
		t.Fatalf("header ref = %+v", header)
	}
	if leaf.kind != rowSession || leaf.key != keyOf(item) {
		t.Fatalf("session ref = %+v", leaf)
	}
	if more.kind != rowMore || more.project != "ars" || more.key != (sessionKey{}) {
		t.Fatalf("more ref = %+v", more)
	}
}
```

Note: `treeSession` uses `RuntimeAttached` in one new test; the existing helper already accepts any state.

After this step, `go test ./internal/tui` must fail to compile with `undefined: groupMode` / `undefined: rowMore` — that is the red state for this task.

- [ ] **Step 2: Implement tree.go**

In `internal/tui/tree.go`:

Replace the `rowKind` block:

```go
type rowKind int

const (
	rowHeader rowKind = iota
	rowSession
	rowMore
)

type groupMode int

const (
	groupModeAuto groupMode = iota
	groupModeOpen
	groupModeClosed
)
```

`listRow` is unchanged (`count` doubles as the hidden count on more-rows).

Replace `refOf`:

```go
func refOf(row listRow) rowRef {
	if row.kind == rowSession {
		return rowRef{kind: rowSession, project: row.project, key: keyOf(row.session)}
	}
	return rowRef{kind: row.kind, project: row.project}
}
```

Replace `buildRows`:

```go
func buildRows(items []session.Session, modes map[string]groupMode, searchActive bool) []listRow {
	var rows []listRow
	for _, group := range groupSessions(items) {
		mode := modes[group.project]
		if searchActive {
			mode = groupModeOpen
		}
		visible := group.sessions
		hidden := 0
		if mode == groupModeAuto {
			active := activeSessions(group.sessions)
			if len(active) == 0 {
				mode = groupModeClosed
			} else {
				visible = active
				hidden = len(group.sessions) - len(active)
			}
		}
		rows = append(rows, listRow{
			kind:      rowHeader,
			project:   group.project,
			count:     len(group.sessions),
			state:     groupState(group.sessions),
			collapsed: mode == groupModeClosed,
		})
		if mode == groupModeClosed {
			continue
		}
		for position, item := range visible {
			rows = append(rows, listRow{
				kind:    rowSession,
				project: group.project,
				session: item,
				last:    position == len(visible)-1 && hidden == 0,
			})
		}
		if hidden > 0 {
			rows = append(rows, listRow{kind: rowMore, project: group.project, count: hidden, last: true})
		}
	}
	return rows
}

func activeSessions(items []session.Session) []session.Session {
	var active []session.Session
	for _, item := range items {
		if item.Runtime.State != session.RuntimeSaved {
			active = append(active, item)
		}
	}
	return active
}
```

- [ ] **Step 3: Implement model.go**

In `internal/tui/model.go`:

Replace the `collapsed map[string]bool` field with `groupMode map[string]groupMode`.

In `refreshVisible`, change the `buildRows` call to `buildRows(filtered, value.groupMode, value.query != "")`.

Replace `toggle` and add `openGroup` and `projectExpanded`:

```go
func (value *model) toggle(project string) {
	if value.groupMode == nil {
		value.groupMode = make(map[string]groupMode)
	}
	if value.projectExpanded(project) {
		value.groupMode[project] = groupModeClosed
	} else {
		value.groupMode[project] = groupModeOpen
	}
	value.selectedRef = rowRef{kind: rowHeader, project: project}
	value.refreshVisible()
}

func (value model) projectExpanded(project string) bool {
	for _, row := range value.rows {
		if row.kind == rowHeader && row.project == project {
			return !row.collapsed
		}
	}
	return false
}

func (value *model) openGroup(project string) {
	if value.groupMode == nil {
		value.groupMode = make(map[string]groupMode)
	}
	value.groupMode[project] = groupModeOpen
	index := value.selected
	value.refreshVisible()
	if index < len(value.rows) {
		value.selectRow(index)
	}
}
```

(`openGroup` re-selects the old cursor index: the more-row is replaced in place by the first revealed session, and rows only grow, so the index stays valid.)

In `updateKey`, replace the `tea.KeyEnter` and `tea.KeySpace` cases:

```go
	case tea.KeyEnter:
		row, ok := value.selectedRow()
		if !ok {
			return value, nil
		}
		switch row.kind {
		case rowHeader:
			value.toggle(row.project)
			return value, nil
		case rowMore:
			value.openGroup(row.project)
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
		if row, ok := value.selectedRow(); ok {
			switch row.kind {
			case rowHeader:
				value.toggle(row.project)
			case rowMore:
				value.openGroup(row.project)
			}
		}
		return value, nil
```

In `restoreSelection`, replace the matching loop and the fallback condition:

```go
	for index, row := range value.rows {
		ref := refOf(row)
		if ref.kind != value.selectedRef.kind {
			continue
		}
		if ref.kind == rowSession && ref.key == value.selectedRef.key {
			value.selectRow(index)
			return
		}
		if ref.kind != rowSession && ref.project == value.selectedRef.project {
			value.selectRow(index)
			return
		}
	}
	if value.selectedRef.kind != rowHeader {
		for index, row := range value.rows {
			if row.kind == rowHeader && row.project == value.selectedRef.project {
				value.selectRow(index)
				return
			}
		}
	}
```

(The trailing index-clamp fallback stays as-is.)

- [ ] **Step 4: Implement view.go**

In `internal/tui/view.go`, `sessionLines`, route more-rows:

```go
	for index, row := range value.rows {
		selected := index == value.selected
		switch row.kind {
		case rowHeader:
			lines = append(lines, value.renderHeader(row, selected, width))
		case rowMore:
			lines = append(lines, value.renderMore(row, selected, width))
		default:
			lines = append(lines, value.renderRow(row, selected, layout))
		}
	}
```

Add below `renderHeader`:

```go
func (value model) renderMore(row listRow, selected bool, width int) string {
	cursor := "  "
	if selected {
		cursor = "> "
		if !value.noColor {
			cursor = value.styles.selectedCursor.Render(cursor)
		}
	}
	text := fmt.Sprintf("… %d more", row.count)
	if !value.noColor {
		text = value.styles.muted.Render(text)
	}
	padding := rowPadding(width)
	line := fitLine(cursor+"└─ "+text, width-2*padding)
	line = strings.Repeat(" ", padding) + line
	line += strings.Repeat(" ", max(0, width-padding-lipgloss.Width(line)))
	line += strings.Repeat(" ", padding)
	if selected && !value.noColor {
		line = value.selectedBackground(line)
	}
	return line
}
```

- [ ] **Step 5: Run tree tests**

Run: `go test ./internal/tui -run 'TestBuildRows|TestRefOf' -v`
Expected: PASS for all six tree tests. (`go test ./internal/tui` as a whole still fails — model/view tests are updated next.)

- [ ] **Step 6: Update model_test.go**

Six existing tests change; three tests are added. Exact edits:

**(a) `TestModelInitialCollectionNavigatesFiltersAndAttaches`** — replace the navigation block between the initial-selection check and the `'/'` search key press (currently `j`, `j`, key-check, `k`, key-check, `j`) with:

```go
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	if row, _ := model.selectedRow(); row.kind != rowHeader || row.project != "api" || !row.collapsed {
		t.Fatalf("saved-only group not collapsed by default: %+v", row)
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	if row, _ := model.selectedRow(); row.kind != rowSession || keyOf(row.session) != keyOf(items[1]) {
		t.Fatal("selection did not reach the manually opened recent session")
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'k', Text: "k"}))
	if row, _ := model.selectedRow(); row.kind != rowHeader {
		t.Fatal("selection did not move up onto a header")
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
```

The search/attach tail of the test is unchanged.

**(b) `TestModelCursorCoversHeadersAndSessions`** — change the expectation:

```go
	want := []rowKind{rowHeader, rowSession, rowHeader}
```

(the api group is saved-only and now starts collapsed; the rest of the test is unchanged).

**(c) `TestModelEnterOnHeaderTogglesWithoutAttaching`** — the row counts change: after Enter collapses `ars`, expect `len(value.rows) != 2` (ars header + collapsed api header); after Space re-expands, expect `len(value.rows) != 3`:

```go
	if len(value.rows) != 2 || !value.rows[0].collapsed {
		t.Fatalf("group did not collapse: %+v", value.rows)
	}
	...
	if len(value.rows) != 3 {
		t.Fatalf("space did not expand: %+v", value.rows)
	}
```

**(d) `TestModelCollapseSurvivesRefreshAndSearchOverridesIt`** — the pre-search and post-search row counts change from 3 to 2:

```go
	if len(value.rows) != 2 || !value.rows[0].collapsed {
		t.Fatalf("collapse lost on refresh: %+v", value.rows)
	}
	...
	if len(value.rows) != 2 || !value.rows[0].collapsed {
		t.Fatalf("collapse not restored after search: %+v", value.rows)
	}
```

(the middle search assertion `len(value.rows) != 2 || value.rows[1].session.Title != "connection check"` already expects 2 rows and stays as-is).

**(e) `TestModelRefreshPreservesCanonicalSelection`** — the renamed session is saved-only, so its new group would start collapsed and hide it. Pin the renamed project open; insert before the `model.collecting = true` line:

```go
	model.groupMode = map[string]groupMode{"project": groupModeOpen}
```

(`session.Project("/renamed/project")` is `"project"`.)

**(f) Add three tests** (place after `TestModelSelectionFallsBackToHeaderWhenRowVanishes`):

```go
func mixedProjectSessions() []session.Session {
	items := twoSessions()
	items[1].CWD = items[0].CWD
	return items
}

func TestModelEnterOnMoreRowRevealsRecentSessionsWithoutAttaching(t *testing.T) {
	value := readyModel()
	attached := 0
	value.deps.Attach = func(context.Context, session.Session) (ExecCommand, error) {
		attached++
		return &fakeExecCommand{}, nil
	}
	value.result.Sessions = mixedProjectSessions()
	value.refreshVisible()
	if len(value.rows) != 3 || value.rows[2].kind != rowMore || value.rows[2].count != 1 {
		t.Fatalf("auto rows = %+v, want header, active, more", value.rows)
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	if row, _ := value.selectedRow(); row.kind != rowMore {
		t.Fatalf("selection did not reach more row: %+v", row)
	}
	value, command := updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command != nil || attached != 0 {
		t.Fatal("enter on more row attached")
	}
	if len(value.rows) != 3 || value.rows[2].kind != rowSession {
		t.Fatalf("more row did not open group: %+v", value.rows)
	}
	if row, _ := value.selectedRow(); row.kind != rowSession || keyOf(row.session) != keyOf(mixedProjectSessions()[1]) {
		t.Fatalf("selection did not land on first revealed session: %+v", row)
	}
}

func TestModelSpaceOnMoreRowOpensGroup(t *testing.T) {
	value := readyModel()
	value.result.Sessions = mixedProjectSessions()
	value.refreshVisible()
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	if len(value.rows) != 3 || value.rows[2].kind != rowSession {
		t.Fatalf("space on more row did not open group: %+v", value.rows)
	}
}

func TestModelHeaderToggleClosesAutoPartialThenOpensFull(t *testing.T) {
	value := readyModel()
	value.result.Sessions = mixedProjectSessions()
	value.refreshVisible()
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'k', Text: "k"}))
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if len(value.rows) != 1 || !value.rows[0].collapsed {
		t.Fatalf("auto-partial header did not close: %+v", value.rows)
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if len(value.rows) != 3 || value.rows[1].kind != rowSession || value.rows[2].kind != rowSession {
		t.Fatalf("closed header did not open fully: %+v", value.rows)
	}
}
```

- [ ] **Step 7: Update view_test.go**

Add the helper (place next to `activeRow`/`selectedRow` helpers):

```go
func openAllGroups(value model) model {
	if value.groupMode == nil {
		value.groupMode = make(map[string]groupMode)
	}
	for _, item := range value.result.Sessions {
		value.groupMode[session.Project(item.CWD)] = groupModeOpen
	}
	value.refreshVisible()
	return value
}
```

Apply these mechanical edits — each keeps the test's original intent (rendering fully expanded groups) under the new defaults:

1. `TestSmallHeightKeepsSelectedRowFooterAndHelpVisible`: replace `model.refreshVisible()` with `model = openAllGroups(model)`.
2. `TestViewRendersOneLineGroupsAndNeutralProviderLocation`: replace `model.refreshVisible()` with `model = openAllGroups(model)`.
3. `TestViewKeepsBalancedVerticalRhythm`: after `value := readyModel()`, add `value = openAllGroups(value)`.
4. `TestNoColorPreservesSelectionAndStateWithoutANSI`: after `value := readyModel()`, add `value = openAllGroups(value)`.
5. `TestViewGroupsSessionsUnderProjectHeaders`: after `value := readyModel()`, add `value = openAllGroups(value)`.
6. `TestViewTreeGuidesMarkLastSession`: replace `value.refreshVisible()` with `value = openAllGroups(value)`.
7. `TestStaleCachedColumnKeepsActivityVisible`: replace `model.refreshVisible()` with `model = openAllGroups(model)`.
8. `TestViewMarksStaleHostRowsAsCached`: replace `model.refreshVisible()` with `model = openAllGroups(model)`.

Unchanged tests (they only exercise active/selected sessions, which stay visible by default): `TestSmallHeightBoundsMaximumLengthCWDDetails`, `TestSecondaryUIUsesHierarchyStyles`, `TestViewShowsSelectedCanonicalDetailsAndBoundedDiagnostics`, `TestNarrowNoColorViewKeepsRequiredFields`, `TestViewHidesLocalhostPresentation`, `TestNarrowRowKeepsLongTitleLocationRuntimeAndActivityVisible`, `TestNarrowViewRemovesOptionalColumnsInOrder`, `TestViewCollapsedHeaderShowsCountAndActiveMarker`, `TestViewLinesStayWithinWidthWithTree`, `TestViewUntitledFallbackUsesShortID`, and the non-view tests.

Add a new view test after `TestViewTreeGuidesMarkLastSession`:

```go
func TestViewRendersMoreRowForAutoPartialGroups(t *testing.T) {
	value := readyModel()
	value.result.Sessions = mixedProjectSessions()
	value.width, value.height, value.noColor = 120, 24, true
	value.refreshVisible()
	plain := ansi.Strip(value.View().Content)
	if !strings.Contains(plain, "▾ ars (2)") ||
		!strings.Contains(plain, "├─ ✻  connection check") ||
		!strings.Contains(plain, "└─ … 1 more") {
		t.Fatalf("auto partial group rows wrong:\n%s", plain)
	}
	if strings.Contains(plain, "API repair") {
		t.Fatalf("recent session leaked into partial group:\n%s", plain)
	}

	value.noColor = false
	if raw := value.View().Content; !strings.Contains(raw, value.styles.muted.Render("… 1 more")) {
		t.Fatalf("more row is not muted: %q", raw)
	}
}
```

- [ ] **Step 8: Update pty_integration_test.go**

The fixture session starts `saved`, so its group now boots collapsed and Enter lands on the header. In `runPTYAttachDetachFixture`, replace:

```go
	waitForPTYOutput(t, &capture, runDone, func(value string) bool {
		return strings.Contains(value, "ars  0 active") && strings.Contains(value, "PTY fixture provider")
	}, "initial ARS TUI")
	if _, err := master.Write([]byte{'\r'}); err != nil {
		t.Fatalf("write Enter: %v", err)
	}
```

with:

```go
	waitForPTYOutput(t, &capture, runDone, func(value string) bool {
		return strings.Contains(value, "ars  0 active") && strings.Contains(value, "▸")
	}, "initial ARS TUI with collapsed saved group")
	if _, err := master.Write([]byte{'\r'}); err != nil {
		t.Fatalf("write Enter to expand group: %v", err)
	}
	waitForPTYOutput(t, &capture, runDone, func(value string) bool {
		return strings.Contains(value, "PTY fixture provider")
	}, "expanded saved group")
	if _, err := master.Write([]byte{'j'}); err != nil {
		t.Fatalf("write j: %v", err)
	}
	waitForPTYOutput(t, &capture, runDone, func(value string) bool {
		return strings.Contains(value, "> └─")
	}, "selected fixture session")
	if _, err := master.Write([]byte{'\r'}); err != nil {
		t.Fatalf("write Enter to attach: %v", err)
	}
```

The rest of the fixture (PID capture, Ctrl+Q detach, `"running  1h"` wait — the session is active after detach so it stays visible under auto mode) is unchanged.

- [ ] **Step 9: Run the full tui test suite**

Run: `go test ./internal/tui -v`
Expected: PASS (the PTY test needs tmux; it self-skips otherwise — if tmux is available locally it must pass).

- [ ] **Step 10: Commit**

```bash
gofmt -l internal/ && go vet ./internal/tui
git add internal/tui
git commit -m "feat: collapse inactive projects and show active sessions with a more row"
```

---

### Task 3: Full-suite verification

**Files:** none (verification only; fix regressions in place if any appear).

- [ ] **Step 1: Run everything**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all packages PASS. Pay attention to `internal/app` (its e2e/stream tests exercise sessions end-to-end but use Claude fixtures and explicit titles, so no changes are expected).

- [ ] **Step 2: Manual smoke check**

Run: `go run ./cmd/ars` in a terminal with real sessions.
Expected: projects with running/attached sessions show only those sessions plus `… N more`; other projects show as `▸ name (count)`; codex rows show first-message titles instead of ID prefixes; Enter on `… N more` reveals the rest; `/` search still surfaces everything.

- [ ] **Step 3: Commit any fixes**

Only if steps 1–2 surfaced fixes:

```bash
git add -A && git commit -m "fix: address full-suite regressions for partial tree"
```
