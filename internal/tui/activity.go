package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)

const (
	activityInterval      = 7 * time.Second
	activityProbeCap      = 4
	activityTailLines     = 15
	activityWaitingSymbol = "?"
)

// activityState is what the pane content says about a running session: whether
// its agent is still working or has stopped and is waiting for a reply. It is
// derived in the view layer from a pane capture, never from the session itself.
type activityState int

const (
	activityUnknown activityState = iota
	activityWorking
	activityWaiting
)

type activityEntry struct {
	state activityState
	at    time.Time
}

type activityMsg struct {
	key   sessionKey
	state activityState
}

type activityTickMsg struct{}

// detectActivity classifies a pane capture. It is deliberately conservative:
// only the markers Claude Code is known to render count, and anything else
// stays unknown so the row keeps its plain running symbol.
func detectActivity(content []byte) activityState {
	lines := activityTail(ansi.Strip(string(content)), activityTailLines)
	for _, line := range lines {
		if strings.Contains(line, "esc to interrupt") {
			return activityWorking
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "Do you want") || trimmed == "❯" {
			return activityWaiting
		}
		if rest, ok := strings.CutPrefix(trimmed, "❯ "); ok && strings.TrimSpace(rest) == "" {
			return activityWaiting
		}
	}
	return activityUnknown
}

// activityTail returns the last count non-blank lines, oldest first.
func activityTail(text string, count int) []string {
	all := strings.Split(text, "\n")
	tail := make([]string, 0, count)
	for index := len(all) - 1; index >= 0 && len(tail) < count; index-- {
		if strings.TrimSpace(all[index]) == "" {
			continue
		}
		tail = append(tail, all[index])
	}
	for left, right := 0, len(tail)-1; left < right; left, right = left+1, right-1 {
		tail[left], tail[right] = tail[right], tail[left]
	}
	return tail
}

func activityTick() tea.Cmd {
	return tea.Tick(activityInterval, func(time.Time) tea.Msg {
		return activityTickMsg{}
	})
}

// updateActivityTick probes up to activityProbeCap running sessions that have
// no probe outstanding and reschedules itself. The tick always keeps running,
// like the spinner: staleness is handled per probe by the session key.
func (value model) updateActivityTick(activityTickMsg) (model, tea.Cmd) {
	if value.deps.Preview == nil {
		return value, activityTick()
	}
	commands := []tea.Cmd{activityTick()}
	probes := 0
	for _, item := range value.result.Sessions {
		if probes == activityProbeCap {
			break
		}
		if item.Runtime.State != session.RuntimeRunning {
			continue
		}
		key := keyOf(item)
		if value.activityPending[key] {
			continue
		}
		if value.activityPending == nil {
			value.activityPending = make(map[sessionKey]bool)
		}
		value.activityPending[key] = true
		commands = append(commands, probeActivity(value.ctx, value.deps.Preview, item))
		probes++
	}
	return value, tea.Batch(commands...)
}

func probeActivity(ctx context.Context, capture func(context.Context, session.Session) ([]byte, error), item session.Session) tea.Cmd {
	key := keyOf(item)
	return func() tea.Msg {
		content, err := capture(ctx, item)
		if err != nil {
			return activityMsg{key: key, state: activityUnknown}
		}
		return activityMsg{key: key, state: detectActivity(content)}
	}
}

// updateActivity records a probe result, dropping it when the session is no
// longer part of the running inventory: a slow capture can land after the
// session was killed, detached, or saved.
func (value model) updateActivity(message activityMsg) (model, tea.Cmd) {
	delete(value.activityPending, message.key)
	if !value.runningKey(message.key) {
		return value, nil
	}
	if value.activity == nil {
		value.activity = make(map[sessionKey]activityEntry)
	}
	value.activity[message.key] = activityEntry{state: message.state, at: value.deps.Now()}
	return value, nil
}

func (value model) runningKey(key sessionKey) bool {
	for _, item := range value.result.Sessions {
		if keyOf(item) == key {
			return item.Runtime.State == session.RuntimeRunning
		}
	}
	return false
}

// evictActivity drops entries whose session left the inventory so the maps do
// not grow across refreshes.
func (value *model) evictActivity() {
	if len(value.activity) == 0 && len(value.activityPending) == 0 {
		return
	}
	live := make(map[sessionKey]struct{}, len(value.result.Sessions))
	for _, item := range value.result.Sessions {
		live[keyOf(item)] = struct{}{}
	}
	for key := range value.activity {
		if _, ok := live[key]; !ok {
			delete(value.activity, key)
		}
	}
	for key := range value.activityPending {
		if _, ok := live[key]; !ok {
			delete(value.activityPending, key)
		}
	}
}

// activitySymbol renders the state marker for a session row, swapping the
// running symbol for the needs-input marker when the last probe saw the agent
// waiting on a reply.
func (value model) activitySymbol(item session.Session) string {
	if item.Runtime.State == session.RuntimeRunning && value.activity[keyOf(item)].state == activityWaiting {
		if value.noColor {
			return activityWaitingSymbol
		}
		return value.styles.failure.Render(activityWaitingSymbol)
	}
	return value.stateText(stateSymbol(item.Runtime.State), item.Runtime.State)
}
