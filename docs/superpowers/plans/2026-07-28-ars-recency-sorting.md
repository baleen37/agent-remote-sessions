# ARS Recency Sorting Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Order ARS project groups and sessions by pinned status followed by newest activity, without changing the rest of the TUI.

**Architecture:** Keep the existing `buildRows` and `groupSessions` pipeline. Remove runtime state from the two stable sort comparators, rank groups by `latestActivity` across all members, and retain runtime state only for header display and automatic folding.

**Tech Stack:** Go 1.25, Bubble Tea v2, standard library `sort` and `time`, Go testing

## Global Constraints

- Project groups sort by pinned membership descending, then the newest `UpdatedAt` across every group member descending.
- Sessions sort by pinned status descending, then `UpdatedAt` descending.
- Equal keys retain input order through stable sorting.
- Runtime state remains visible and continues to drive filters and automatic folding, but does not affect ordering.
- Keep grouping, search, filters, pin behavior, selection restoration, rendering, and keyboard controls unchanged.
- Do not add a sort menu, configuration, persisted preference, or status text.

---

### Task 1: Replace state-first ordering with recency-first ordering

**Files:**
- Modify: `internal/tui/tree.go:110-166`
- Test: `internal/tui/tree_test.go:25-60`
- Test: `internal/tui/tree_test.go:160-205`
- Test: `internal/tui/pin_test.go:85-160`

**Interfaces:**
- Consumes: `groupSessions(items []session.Session, pins map[sessionKey]bool) []sessionGroup`, `latestActivity(items []session.Session) time.Time`, and the existing stable input order.
- Produces: the unchanged `groupSessions` signature with `pinned → UpdatedAt` ordering at both group and session tiers.

- [ ] **Step 1: Rewrite the state-first tests as failing recency-first tests**

In `internal/tui/tree_test.go`, replace
`TestBuildRowsGroupsAndOrdersByStateThenActivity` with:

```go
func TestBuildRowsGroupsAndOrdersByPinThenActivity(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	items := []session.Session{
		treeSession("blog", "blog-running", session.RuntimeRunning, base.Add(-3*time.Hour)),
		treeSession("ars", "ars-saved", session.RuntimeSaved, base),
		treeSession("ars", "ars-running", session.RuntimeRunning, base.Add(-2*time.Hour)),
	}
	modes := map[string]groupMode{"ars": groupModeOpen, "blog": groupModeOpen}
	rows := buildRows(items, modes, false, nil)
	want := []struct {
		kind    rowKind
		project string
		id      string
		last    bool
	}{
		{rowHeader, "ars", "", false},
		{rowSession, "ars", "ars-saved", false},
		{rowSession, "ars", "ars-running", true},
		{rowHeader, "blog", "", false},
		{rowSession, "blog", "blog-running", true},
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
	if rows[3].count != 1 || rows[3].state != session.RuntimeRunning {
		t.Fatalf("blog header = %+v", rows[3])
	}
}
```

Replace `TestBuildRowsRanksGroupsByDisplayedRows` with:

```go
func TestBuildRowsRanksGroupsByLatestActivity(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	items := []session.Session{
		treeSession("ars", "ars-running", session.RuntimeRunning, base.Add(-5*time.Hour)),
		treeSession("blog", "blog-running", session.RuntimeRunning, base.Add(-6*time.Hour)),
		treeSession("blog", "blog-saved", session.RuntimeSaved, base),
	}
	rows := buildRows(items, nil, false, nil)
	if rows[0].kind != rowHeader || rows[0].project != "blog" {
		t.Fatalf("first header = %+v, want blog with the latest activity", rows[0])
	}
}
```

Add stable-tie coverage:

```go
func TestBuildRowsKeepsInputOrderWhenActivityTies(t *testing.T) {
	updated := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	items := []session.Session{
		treeSession("blog", "blog-first", session.RuntimeSaved, updated),
		treeSession("ars", "ars-first", session.RuntimeRunning, updated),
		treeSession("ars", "ars-second", session.RuntimeSaved, updated),
	}
	modes := map[string]groupMode{"ars": groupModeOpen, "blog": groupModeOpen}
	rows := buildRows(items, modes, false, nil)
	if projects := headerProjects(rows); !reflect.DeepEqual(projects, []string{"blog", "ars"}) {
		t.Fatalf("group order = %v, want stable input order", projects)
	}
	if ids := sessionIDs(rows); !reflect.DeepEqual(ids, []string{"blog-first", "ars-first", "ars-second"}) {
		t.Fatalf("session order = %v, want stable input order", ids)
	}
}
```

Update the adjacent expansion-stability test comment so it states that group
mode is applied after the all-member activity ranking and therefore cannot
change project order.

- [ ] **Step 2: Run the focused tests and confirm the old implementation fails**

Run:

```bash
go test ./internal/tui -run 'TestBuildRows(GroupsAndOrdersByPinThenActivity|RanksGroupsByLatestActivity|KeepsInputOrderWhenActivityTies|GroupOrderStableWhenFoldedSessionsRevealed)$' -count=1
```

Expected: FAIL because the saved `ars` session remains below its older running
session, and the active `ars` project remains above the more recently updated
`blog` project.

- [ ] **Step 3: Implement the minimal comparator change**

In `internal/tui/tree.go`, keep the pin comparisons and remove the state
comparisons from both stable sorts:

```go
for _, group := range groups {
	members := group.sessions
	sort.SliceStable(members, func(left, right int) bool {
		leftPinned := pins[keyOf(members[left])]
		rightPinned := pins[keyOf(members[right])]
		if leftPinned != rightPinned {
			return leftPinned
		}
		return members[left].UpdatedAt.After(members[right].UpdatedAt)
	})
}
sort.SliceStable(groups, func(left, right int) bool {
	leftPinned := hasPinned(groups[left].sessions, pins)
	rightPinned := hasPinned(groups[right].sessions, pins)
	if leftPinned != rightPinned {
		return leftPinned
	}
	return latestActivity(groups[left].sessions).After(latestActivity(groups[right].sessions))
})
```

Delete `rankedActivity`; it becomes unused. Update the `groupSessions` comment
to describe the new shared `pin → recency` rule. Keep `groupState`,
`activeSessions`, and `latestActivity`, because rendering and automatic folding
still use them.

- [ ] **Step 4: Update pin-test wording and verify pin precedence**

In `internal/tui/pin_test.go`, change comments and failure messages that refer
to “state-then-recency” or only “newest” so they describe `pin → recency`.
Do not change test behavior.

Run:

```bash
go test ./internal/tui -run 'Test(BuildRows|ModelShiftP|Pinned)' -count=1
```

Expected: PASS, including pinned session precedence, pinned group precedence,
newest-first order within the pinned tier, stable ties, and the existing group
folding behavior.

- [ ] **Step 5: Run repository verification**

Run:

```bash
go test ./...
go vet ./...
go build -o /tmp/ars-recency-sorting ./cmd/ars
git diff --check
```

Expected: every command exits 0.

- [ ] **Step 6: Verify the rendered order in a real terminal**

Start the production build in a fresh tmux server:

```bash
tmux -L ars-sorting kill-server 2>/dev/null || true
tmux -L ars-sorting new-session -d -x 120 -y 30 /tmp/ars-recency-sorting
tmux -L ars-sorting capture-pane -p
```

Compare the captured project and session order with the inventory timestamps.
Confirm pinned rows lead their tiers and the remaining visible rows are
newest-first. Exercise group open/close once and capture again to confirm the
project order stays fixed. Stop only the dedicated verification server:

```bash
tmux -L ars-sorting kill-server
```

- [ ] **Step 7: Commit the implementation**

```bash
git add internal/tui/tree.go internal/tui/tree_test.go internal/tui/pin_test.go
git commit -m "fix(tui): sort sessions by recent activity"
```
