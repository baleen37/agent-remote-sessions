package tui

import (
	"context"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

const killGracePeriod = 3 * time.Second

type killFireMsg struct {
	seq uint64
}

// killDoneMsg reports one armed kill's outcome. A single-session kill sets
// title and err; a group batch sets group, total and failed, with err carrying
// the first failure so the status can name it.
type killDoneMsg struct {
	seq    uint64
	title  string
	group  string
	total  int
	failed int
	err    error
}

// startKill arms the pending kill for row, replacing any prior pending kill,
// and schedules the grace-period tick. A session row arms that one session; a
// group header arms every live session in the group as one batch sharing a
// single grace period and a single undo. Other rows are a no-op.
func (value model) startKill(row listRow) (model, tea.Cmd) {
	switch row.kind {
	case rowSession:
		if row.session.Runtime.State == session.RuntimeSaved {
			value.status = "no live session to kill"
			return value, nil
		}
		return value.armKill([]session.Session{row.session}, "", "killing "+sessionTitle(row.session)+" in 3s · u undo")
	case rowHeader:
		targets := value.liveGroupSessions(row.project)
		if len(targets) == 0 {
			value.status = boundedStatus("no live sessions in " + row.project)
			return value, nil
		}
		status := "killing " + strconv.Itoa(len(targets)) + " sessions in " + row.project + " in 3s · u undo"
		return value.armKill(targets, row.project, status)
	default:
		return value, nil
	}
}

func (value model) armKill(targets []session.Session, group, status string) (model, tea.Cmd) {
	value.killSeq++
	value.killPending = true
	value.killTargets = targets
	value.killGroup = group
	value.status = boundedStatus(status)
	return value, killTick(value.killSeq)
}

// liveGroupSessions returns the sessions of project that have a live pane, from
// the same filtered inventory the rows are built from: the header stands for
// every member the active search and state filter admit — including ones a
// collapsed or auto-folded group hides — and never for one the filters removed.
// Saved sessions are skipped because they have no process to kill.
func (value model) liveGroupSessions(project string) []session.Session {
	var targets []session.Session
	for _, item := range value.visibleSessions() {
		if session.Project(item.CWD) != project {
			continue
		}
		if item.Runtime.State == session.RuntimeSaved {
			continue
		}
		targets = append(targets, item)
	}
	return targets
}

// cancelKill clears a pending kill, if any.
func (value model) cancelKill() model {
	if !value.killPending {
		return value
	}
	value.killPending = false
	value.killTargets = nil
	value.killGroup = ""
	value.status = "kill canceled"
	return value
}

func killTick(seq uint64) tea.Cmd {
	return tea.Tick(killGracePeriod, func(time.Time) tea.Msg {
		return killFireMsg{seq: seq}
	})
}

func (value model) updateKillFire(message killFireMsg) (model, tea.Cmd) {
	if !value.killPending || message.seq != value.killSeq {
		return value, nil
	}
	return value, runKill(value.ctx, value.deps.Kill, message.seq, value.killTargets, value.killGroup)
}

func runKill(ctx context.Context, kill func(context.Context, session.Session) error, seq uint64, targets []session.Session, group string) tea.Cmd {
	return func() tea.Msg {
		done := killDoneMsg{seq: seq, group: group, total: len(targets)}
		if group == "" && len(targets) == 1 {
			done.title = sessionTitle(targets[0])
		}
		for _, target := range targets {
			if err := kill(ctx, target); err != nil {
				done.failed++
				if done.err == nil {
					done.err = err
				}
			}
		}
		return done
	}
}

func (value model) updateKillDone(message killDoneMsg) (model, tea.Cmd) {
	// A newer x can arm a different pending kill while this one is still in
	// flight (e.g. a slow SSH kill). The invariant that wins is "the user's
	// latest x stays armed": a stale completion (seq mismatch) still reports
	// its own outcome and still restarts collection (the old session really
	// did die/fail), but must not clear the newer pending kill or its status.
	current := value.killSeq == message.seq && value.killPending
	if !current {
		return value.restartCollectionKeepingPending()
	}
	value.killPending = false
	value.killTargets = nil
	value.killGroup = ""
	if message.err != nil {
		value.status = boundedStatus(killFailedStatus(message))
		if message.failed == message.total {
			return value, nil
		}
		// Some members died, so the inventory is stale either way.
		return value.restartCollection()
	}
	value.status = boundedStatus(killedStatus(message))
	return value.restartCollection()
}

// killFailedStatus keeps the "kill failed: " prefix for batches too, not just
// the single-session path: diagnostics() picks the error style off that exact
// prefix, so a batch phrased any other way would report a failed destructive
// action in the muted style.
func killFailedStatus(message killDoneMsg) string {
	if message.group == "" {
		return "kill failed: " + message.err.Error()
	}
	return "kill failed: " + strconv.Itoa(message.failed) + " of " + strconv.Itoa(message.total) +
		" sessions in " + message.group + ": " + message.err.Error()
}

func killedStatus(message killDoneMsg) string {
	if message.group == "" {
		return "killed " + message.title
	}
	return "killed " + strconv.Itoa(message.total) + " sessions in " + message.group
}

// restartCollectionKeepingPending restarts collection for a stale kill
// completion without disturbing a newer pending kill: restartCollection
// otherwise unconditionally clears killPending/killTargets, which would drop
// the pending kill the user just armed with a later x.
func (value model) restartCollectionKeepingPending() (model, tea.Cmd) {
	pending, targets, group, seq := value.killPending, value.killTargets, value.killGroup, value.killSeq
	updated, command := value.restartCollection()
	updated.killPending, updated.killTargets, updated.killGroup, updated.killSeq = pending, targets, group, seq
	return updated, command
}
