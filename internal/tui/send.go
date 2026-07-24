package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

type sendDoneMsg struct {
	title string
	err   error
}

// startCompose enters compose mode targeting row.session, replacing any
// in-progress compose. It is a no-op for non-session rows and for sessions
// without a live tmux session.
func (value model) startCompose(row listRow) (model, tea.Cmd) {
	if row.kind != rowSession {
		return value, nil
	}
	if row.session.Runtime.State == session.RuntimeSaved {
		value.status = "no live session"
		return value, nil
	}
	value.composing = true
	value.compose = ""
	value.composeTarget = row.session
	return value, nil
}

// submitCompose sends the composed text to the compose target, if any, or
// cancels compose when the text is empty.
func (value model) submitCompose() (model, tea.Cmd) {
	text := value.compose
	target := value.composeTarget
	value.composing = false
	value.compose = ""
	value.composeTarget = session.Session{}
	if text == "" {
		return value, nil
	}
	return value, runSend(value.ctx, value.deps.Send, target, text)
}

func runSend(ctx context.Context, send func(context.Context, session.Session, string) error, target session.Session, text string) tea.Cmd {
	return func() tea.Msg {
		err := send(ctx, target, text)
		return sendDoneMsg{title: sessionTitle(target), err: err}
	}
}

func (value model) updateSendDone(message sendDoneMsg) (model, tea.Cmd) {
	if message.err != nil {
		value.status = boundedStatus("send failed: " + message.err.Error())
		return value, nil
	}
	value.status = "sent to " + message.title
	return value, nil
}
