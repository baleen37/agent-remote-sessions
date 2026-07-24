package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

const killGracePeriod = 3 * time.Second

type killFireMsg struct {
	seq uint64
}

type killDoneMsg struct {
	title string
	err   error
}

// startKill registers row.session as the pending kill, replacing any prior
// pending kill, and schedules the grace-period tick. It is a no-op for
// non-session rows and for sessions without a live tmux session.
func (value model) startKill(row listRow) (model, tea.Cmd) {
	if row.kind != rowSession {
		return value, nil
	}
	if row.session.Runtime.State == session.RuntimeSaved {
		value.status = "no live session to kill"
		return value, nil
	}
	value.killSeq++
	value.killPending = true
	value.killTarget = row.session
	value.status = boundedStatus("killing " + sessionTitle(row.session) + " in 3s · u undo")
	return value, killTick(value.killSeq)
}

// cancelKill clears a pending kill, if any.
func (value model) cancelKill() model {
	if !value.killPending {
		return value
	}
	value.killPending = false
	value.killTarget = session.Session{}
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
	target := value.killTarget
	return value, runKill(value.ctx, value.deps.Kill, target)
}

func runKill(ctx context.Context, kill func(context.Context, session.Session) error, target session.Session) tea.Cmd {
	return func() tea.Msg {
		err := kill(ctx, target)
		return killDoneMsg{title: sessionTitle(target), err: err}
	}
}

func (value model) updateKillDone(message killDoneMsg) (model, tea.Cmd) {
	value.killPending = false
	value.killTarget = session.Session{}
	if message.err != nil {
		value.status = boundedStatus("kill failed: " + message.err.Error())
		return value, nil
	}
	value.status = "killed " + message.title
	return value.restartCollection()
}
