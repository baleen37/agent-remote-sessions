package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// runningSessions builds count running sessions in one project plus one saved
// and one attached session, so probe selection has non-running sessions to skip.
func runningSessions(count int) []session.Session {
	items := []session.Session{
		{
			Host: "localhost",
			Candidate: session.Candidate{
				Provider:  session.Claude,
				NativeID:  "aaaaaaaa-e89b-42d3-a456-426614174000",
				UpdatedAt: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
				CWD:       "/work/ars",
				Title:     "attached one",
			},
			Runtime: session.Runtime{State: session.RuntimeAttached, AttachedClients: 1},
		},
		{
			Host: "localhost",
			Candidate: session.Candidate{
				Provider:  session.Claude,
				NativeID:  "bbbbbbbb-e89b-42d3-a456-426614174000",
				UpdatedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
				CWD:       "/work/ars",
				Title:     "saved one",
			},
			Runtime: session.Runtime{State: session.RuntimeSaved},
		},
	}
	for index := range count {
		items = append(items, session.Session{
			Host: "server",
			Candidate: session.Candidate{
				Provider:  session.Claude,
				NativeID:  fmt.Sprintf("run%05d-e89b-42d3-a456-426614174000", index),
				UpdatedAt: time.Date(2026, 7, 19, 11, 0, 0, index, time.UTC),
				CWD:       "/work/ars",
				Title:     fmt.Sprintf("running %d", index),
			},
			Runtime: session.Runtime{State: session.RuntimeRunning},
		})
	}
	return items
}

// activityModel builds a ready model over the given sessions with a Preview
// capture that records every probed session and returns pane.
func activityModel(items []session.Session, pane string) (model, *probeRecorder) {
	recorder := &probeRecorder{}
	result := Result{Sessions: items}
	deps := Dependencies{
		Collect:     staticCollect(result),
		Attach:      func(context.Context, session.Session) (ExecCommand, error) { return &fakeExecCommand{}, nil },
		Preview:     recorder.capture(pane),
		LocalTarget: "localhost",
		Now:         func() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) },
		NoColor:     true,
	}
	value := newModel(context.Background(), deps)
	message, hasCollection, _ := initialCommands(value.Init())
	if !hasCollection {
		panic("activityModel: Init did not produce collectUpdateMsg")
	}
	value, _ = updateModel(value, message)
	value.width = 120
	value.height = 40
	return value, recorder
}

type probeRecorder struct {
	mutex  sync.Mutex
	probed []sessionKey
}

func (recorder *probeRecorder) capture(pane string) func(context.Context, session.Session) ([]byte, error) {
	return func(_ context.Context, item session.Session) ([]byte, error) {
		recorder.mutex.Lock()
		recorder.probed = append(recorder.probed, keyOf(item))
		recorder.mutex.Unlock()
		return []byte(pane), nil
	}
}

func (recorder *probeRecorder) keys() []sessionKey {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	return append([]sessionKey(nil), recorder.probed...)
}

// activityProbes runs the probe children of an activity tick command and
// returns the activityMsg results. tea.Tick children are skipped: they would
// block for the interval.
func activityProbes(command tea.Cmd) []activityMsg {
	if command == nil {
		return nil
	}
	message := command()
	batch, ok := message.(tea.BatchMsg)
	if !ok {
		if probe, ok := message.(activityMsg); ok {
			return []activityMsg{probe}
		}
		return nil
	}
	// The batch always carries the rescheduled tea.Tick alongside the probes,
	// and that child only delivers after activityInterval, so the children run
	// off the calling goroutine and the drain is bounded by a timeout rather
	// than by every child finishing.
	//
	// The buffered channel is deliberately never closed: group.Done() runs after
	// the send, so a straggling child can still be mid-send once Wait returns
	// (or once the timeout fires), and closing underneath it is a data race.
	// Draining what has arrived without closing leaves any late send harmlessly
	// buffered.
	results := make(chan activityMsg, len(batch))
	var group sync.WaitGroup
	for _, child := range batch {
		group.Add(1)
		go func(child tea.Cmd) {
			defer group.Done()
			if probe, ok := child().(activityMsg); ok {
				results <- probe
			}
		}(child)
	}
	done := make(chan struct{})
	go func() {
		group.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
	}
	var probes []activityMsg
	for {
		select {
		case probe := <-results:
			probes = append(probes, probe)
		default:
			return probes
		}
	}
}

// TestActivityTickStartsFromInit runs the batch children concurrently: the
// activity tick only delivers after activityInterval, so draining it inline
// would stall the test for the whole interval.
func TestActivityTickStartsFromInit(t *testing.T) {
	value, _ := activityModel(runningSessions(1), waitingPane)
	batch, ok := value.Init()().(tea.BatchMsg)
	if !ok {
		t.Fatal("Init did not return a batch")
	}
	found := make(chan struct{}, len(batch))
	for _, child := range batch {
		go func(child tea.Cmd) {
			if _, ok := child().(activityTickMsg); ok {
				found <- struct{}{}
			}
		}(child)
	}
	select {
	case <-found:
	case <-time.After(activityInterval + 2*time.Second):
		t.Fatal("Init did not schedule an activity tick")
	}
}

func TestActivityTickProbesOnlyRunningSessions(t *testing.T) {
	items := runningSessions(2)
	value, recorder := activityModel(items, waitingPane)

	_, command := updateModel(value, activityTickMsg{})
	activityProbes(command)

	probed := recorder.keys()
	if len(probed) != 2 {
		t.Fatalf("probed %d sessions, want the 2 running ones: %#v", len(probed), probed)
	}
	want := map[sessionKey]bool{keyOf(items[2]): true, keyOf(items[3]): true}
	for _, key := range probed {
		if !want[key] {
			t.Fatalf("probed a non-running session: %#v", key)
		}
	}
}

func TestActivityTickCapsConcurrentProbes(t *testing.T) {
	value, recorder := activityModel(runningSessions(7), waitingPane)

	updated, command := updateModel(value, activityTickMsg{})
	activityProbes(command)
	if probed := recorder.keys(); len(probed) != activityProbeCap {
		t.Fatalf("first tick probed %d sessions, want the cap of %d", len(probed), activityProbeCap)
	}

	// The remaining running sessions are picked up by the next tick, and the
	// four still in flight are not probed again.
	_, command = updateModel(updated, activityTickMsg{})
	activityProbes(command)
	probed := recorder.keys()
	if len(probed) != 7 {
		t.Fatalf("two ticks probed %d sessions total, want all 7 exactly once", len(probed))
	}
	seen := make(map[sessionKey]int, len(probed))
	for _, key := range probed {
		seen[key]++
	}
	for key, count := range seen {
		if count != 1 {
			t.Fatalf("session %#v probed %d times while in flight, want 1", key, count)
		}
	}
}

func TestActivityProbeResultUpdatesState(t *testing.T) {
	items := runningSessions(1)
	value, _ := activityModel(items, waitingPane)
	key := keyOf(items[2])

	updated, command := updateModel(value, activityTickMsg{})
	probes := activityProbes(command)
	if len(probes) != 1 {
		t.Fatalf("tick produced %d probes, want 1", len(probes))
	}
	updated, _ = updateModel(updated, probes[0])

	if got := updated.activity[key].state; got != activityWaiting {
		t.Fatalf("activity for probed session = %v, want %v", got, activityWaiting)
	}
	if updated.activity[key].at.IsZero() {
		t.Fatal("activity entry has no timestamp")
	}
	// The in-flight guard is released, so a later tick probes it again.
	if updated.activityPending[key] {
		t.Fatal("in-flight guard still set after the probe result landed")
	}
}

func TestActivityProbeDropsResultForVanishedSession(t *testing.T) {
	value, _ := activityModel(runningSessions(1), waitingPane)
	stale := activityMsg{key: sessionKey{nativeID: "gone"}, state: activityWaiting}

	updated, _ := updateModel(value, stale)
	if _, ok := updated.activity[stale.key]; ok {
		t.Fatal("result for a session outside the inventory was applied")
	}
}

func TestActivityProbeDropsResultForSessionNoLongerRunning(t *testing.T) {
	items := runningSessions(1)
	value, _ := activityModel(items, waitingPane)
	// The attached session is present in the inventory but not running, so a
	// probe result carrying its key is stale by definition.
	stale := activityMsg{key: keyOf(items[0]), state: activityWaiting}

	updated, _ := updateModel(value, stale)
	if _, ok := updated.activity[stale.key]; ok {
		t.Fatal("result for a session that is no longer running was applied")
	}
}

func TestActivityEvictsSessionsLeavingTheInventory(t *testing.T) {
	items := runningSessions(2)
	value, _ := activityModel(items, waitingPane)
	staying, leaving := keyOf(items[2]), keyOf(items[3])

	updated, command := updateModel(value, activityTickMsg{})
	for _, probe := range activityProbes(command) {
		updated, _ = updateModel(updated, probe)
	}
	if len(updated.activity) != 2 {
		t.Fatalf("activity map has %d entries before eviction, want 2", len(updated.activity))
	}

	shrunk := append([]session.Session(nil), items[:3]...)
	updated, _ = updateModel(updated, collectUpdateMsg{
		generation: updated.generation,
		update:     Update{Result: Result{Sessions: shrunk}, Done: true},
		channel:    staticCollect(Result{})(context.Background()),
	})

	if _, ok := updated.activity[leaving]; ok {
		t.Fatal("activity entry for a departed session was not evicted")
	}
	if _, ok := updated.activity[staying]; !ok {
		t.Fatal("activity entry for a surviving session was evicted")
	}
}

func TestActivityNoProbesWithoutPreviewDependency(t *testing.T) {
	items := runningSessions(2)
	result := Result{Sessions: items}
	deps := Dependencies{
		Collect:     staticCollect(result),
		Attach:      func(context.Context, session.Session) (ExecCommand, error) { return &fakeExecCommand{}, nil },
		LocalTarget: "localhost",
		Now:         func() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) },
		NoColor:     true,
	}
	value := newModel(context.Background(), deps)
	message, _, _ := initialCommands(value.Init())
	value, _ = updateModel(value, message)

	updated, command := updateModel(value, activityTickMsg{})
	if probes := activityProbes(command); len(probes) != 0 {
		t.Fatalf("nil Preview produced %d probes, want none", len(probes))
	}
	if len(updated.activity) != 0 {
		t.Fatalf("nil Preview populated the activity map: %#v", updated.activity)
	}
}

// sessionRowLine returns the rendered list line holding title, or "" when no
// row shows it.
func sessionRowLine(content, title string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, title) {
			return line
		}
	}
	return ""
}

func TestActivityRowRendersNeedsInputSymbol(t *testing.T) {
	items := runningSessions(2)
	value, _ := activityModel(items, waitingPane)
	waitingKey := keyOf(items[2])
	// Auto grouping hides the saved session behind a "… more" row; open the
	// group so both running rows render.
	value.groupMode = map[string]groupMode{groupKey("localhost", "ars"): groupModeOpen}
	value.refreshVisible()

	// The footer and legend legitimately contain "?", so only the session rows
	// are checked for the marker.
	before := sessionRowLine(ansi.Strip(value.View().Content), "running 0")
	if before == "" {
		t.Fatal("running row missing from the initial view")
	}
	if strings.Contains(before, activityWaitingSymbol) {
		t.Fatalf("needs-input symbol rendered before any probe: %q", before)
	}

	updated, command := updateModel(value, activityTickMsg{})
	for _, probe := range activityProbes(command) {
		if probe.key != waitingKey {
			probe.state = activityWorking
		}
		updated, _ = updateModel(updated, probe)
	}

	content := ansi.Strip(updated.View().Content)
	waitingLine := sessionRowLine(content, "running 0")
	workingLine := sessionRowLine(content, "running 1")
	if waitingLine == "" || workingLine == "" {
		t.Fatalf("running rows missing from the view:\n%s", content)
	}
	if !strings.Contains(waitingLine, activityWaitingSymbol) {
		t.Fatalf("waiting row did not render %q: %q", activityWaitingSymbol, waitingLine)
	}
	if !strings.Contains(workingLine, stateSymbol(session.RuntimeRunning)) {
		t.Fatalf("working row lost the running symbol: %q", workingLine)
	}
	if strings.Contains(workingLine, activityWaitingSymbol) {
		t.Fatalf("working row rendered the needs-input symbol: %q", workingLine)
	}
}

func TestActivityWaitingSymbolUsesAttentionStyle(t *testing.T) {
	items := runningSessions(1)
	value, _ := activityModel(items, waitingPane)
	value.noColor = false
	value.activity = map[sessionKey]activityEntry{
		keyOf(items[2]): {state: activityWaiting, at: value.deps.Now()},
	}

	styled := value.activitySymbol(items[2])
	want := value.styles.failure.Render(activityWaitingSymbol)
	if styled != want {
		t.Fatalf("waiting symbol style = %q, want the failure/attention style %q", styled, want)
	}
}

func TestActivityHelpLegendListsNeedsInput(t *testing.T) {
	value, _ := activityModel(runningSessions(1), waitingPane)
	value.showHelp = true

	overlay := ansi.Strip(value.View().Content)
	if !strings.Contains(overlay, activityWaitingSymbol+" needs input") {
		t.Fatalf("help legend missing the needs-input symbol:\n%s", overlay)
	}
	for _, want := range []string{"● attached", "◐ running", "○ idle"} {
		if !strings.Contains(overlay, want) {
			t.Fatalf("help legend lost %q:\n%s", want, overlay)
		}
	}
}
