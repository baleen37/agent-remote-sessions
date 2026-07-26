package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

// errorStatusModel arms an error status ("kill failed: boom") the same way a
// real x -> killFireMsg -> killDoneMsg round trip would, and returns the
// resulting model together with the tea.Cmd updateModel produced alongside
// it (expected to carry the auto-dismiss statusTick).
func errorStatusModel(t *testing.T) (model, tea.Cmd) {
	t.Helper()
	value := readyModel()
	value.deps.Kill = func(context.Context, session.Session) error {
		return errors.New("boom")
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	seq := value.killSeq
	value, command := updateModel(value, killFireMsg{seq: seq})
	done := command().(killDoneMsg)
	return updateModel(value, done)
}

// tickSeq synthesizes the statusTickMsg for value's current statusSeq,
// mirroring how the kill tests inject killFireMsg directly rather than
// running the real tea.Tick command (which would block for real time).
func tickSeq(value model) statusTickMsg {
	return statusTickMsg{seq: value.statusSeq}
}

func TestStatusTickDismissesErrorStatusAfterFiveTicks(t *testing.T) {
	value, command := errorStatusModel(t)
	if value.status != "kill failed: boom" || value.statusRemaining != statusDismissSeconds {
		t.Fatalf("armed status = %q remaining=%d, want error status with remaining=%d", value.status, value.statusRemaining, statusDismissSeconds)
	}
	if command == nil {
		t.Fatal("arming an error status did not produce a tick command")
	}

	for remaining := statusDismissSeconds - 1; remaining >= 0; remaining-- {
		value, command = updateModel(value, tickSeq(value))
		if remaining > 0 {
			if value.status == "" {
				t.Fatalf("status cleared early with %d ticks left", remaining)
			}
			if value.statusRemaining != remaining {
				t.Fatalf("statusRemaining = %d, want %d", value.statusRemaining, remaining)
			}
			if command == nil {
				t.Fatalf("tick with %d remaining did not reschedule", remaining)
			}
		}
	}
	if value.status != "" {
		t.Fatalf("status = %q after five ticks, want cleared", value.status)
	}
	if command != nil {
		t.Fatal("the tick that cleared the status rescheduled another one")
	}
}

func TestStatusTickSeqGuardsAgainstStaleTicks(t *testing.T) {
	value, _ := errorStatusModel(t)
	staleTick := tickSeq(value)

	// A second failure re-arms the timer with a new seq, invalidating the
	// tick already in flight from the first failure - the same seq-guard
	// invariant killFireMsg relies on for the kill grace period (kill.go).
	value.deps.Kill = func(context.Context, session.Session) error {
		return errors.New("boom again")
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	seq := value.killSeq
	value, fireCommand := updateModel(value, killFireMsg{seq: seq})
	done := fireCommand().(killDoneMsg)
	value, secondCommand := updateModel(value, done)
	if secondCommand == nil {
		t.Fatal("second failure did not arm a new tick")
	}
	newRemaining := value.statusRemaining
	newStatus := value.status
	if newStatus == "" {
		t.Fatal("second failure did not set a status")
	}

	value, command := updateModel(value, staleTick)
	if command != nil {
		t.Fatal("stale tick rescheduled itself instead of being ignored")
	}
	if value.status != newStatus || value.statusRemaining != newRemaining {
		t.Fatalf("stale tick mutated state: status=%q remaining=%d, want unchanged %q/%d",
			value.status, value.statusRemaining, newStatus, newRemaining)
	}
}

func TestStatusTickNotArmedForSuccessStatus(t *testing.T) {
	value := readyModel()
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	seq := value.killSeq
	value, command := updateModel(value, killFireMsg{seq: seq})
	done := command().(killDoneMsg)

	// A successful kill also restarts collection, so command is expected to
	// be non-nil; statusRemaining staying 0 is what proves no countdown was
	// armed alongside it (a real statusTick would only surface a second
	// later, so this is the observable, non-blocking way to check).
	value, _ = updateModel(value, done)
	if !strings.HasPrefix(value.status, "killed ") {
		t.Fatalf("status = %q, want a killed-success status", value.status)
	}
	if value.statusRemaining != 0 {
		t.Fatalf("statusRemaining = %d for a success status, want 0", value.statusRemaining)
	}
}

func TestStatusAutoDismissDoesNotInterfereWithKillUndoTimer(t *testing.T) {
	value := readyModel()
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	if value.status != "killing "+mustSelectedTitle(t, value)+" in 3s · u undo" {
		t.Fatalf("armed kill status = %q", value.status)
	}
	// The kill grace status is not an error status, so it must not carry an
	// auto-dismiss countdown alongside the killTick.
	if value.statusRemaining != 0 {
		t.Fatalf("statusRemaining = %d for the kill grace status, want 0", value.statusRemaining)
	}

	killSeq := value.killSeq
	value.deps.Kill = func(context.Context, session.Session) error {
		return errors.New("boom")
	}
	value, command := updateModel(value, killFireMsg{seq: killSeq})
	done := command().(killDoneMsg)
	value, command = updateModel(value, done)
	if command == nil {
		t.Fatal("kill failure did not arm the auto-dismiss tick")
	}
	if value.killPending {
		t.Fatal("a completed kill left killPending set")
	}

	value, command = updateModel(value, tickSeq(value))
	if value.killPending {
		t.Fatal("processing a statusTick disturbed killPending")
	}
	if value.status == "" {
		t.Fatal("one statusTick with 5 remaining cleared the status early")
	}
	if command == nil {
		t.Fatal("statusTick with ticks remaining did not reschedule")
	}
}

func TestDiagnosticsCountdownSuffixIsRenderOnly(t *testing.T) {
	value, _ := errorStatusModel(t)
	original := value.status

	lines := value.diagnostics(80)
	if len(lines) == 0 {
		t.Fatal("diagnostics() returned no lines")
	}
	rendered := lines[len(lines)-1]
	if !strings.Contains(rendered, "· 5s") {
		t.Fatalf("rendered status = %q, want a countdown suffix", rendered)
	}
	if value.status != original {
		t.Fatalf("value.status = %q after diagnostics(), want unchanged %q", value.status, original)
	}
	if strings.Contains(value.status, "5s") {
		t.Fatal("the countdown suffix leaked into the stored status")
	}
}

func mustSelectedTitle(t *testing.T, value model) string {
	t.Helper()
	row, ok := value.selectedRow()
	if !ok || row.kind != rowSession {
		t.Fatal("no selected session row")
	}
	return sessionTitle(row.session)
}
