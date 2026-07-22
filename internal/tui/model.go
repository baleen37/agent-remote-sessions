package tui

import (
	"context"
	"io"
	"os"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/output"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

const maxStatusBytes = 256

type Result struct {
	Hosts    []output.HostResult
	Sessions []session.Session
	Errors   []output.HostError
	Warnings []output.HostError
}

type ExecCommand interface {
	Run() error
	SetStdin(io.Reader)
	SetStdout(io.Writer)
	SetStderr(io.Writer)
}

type Dependencies struct {
	Collect     func(context.Context) Result
	Attach      func(context.Context, session.Session) (ExecCommand, error)
	LocalTarget string
	Now         func() time.Time
	NoColor     bool
}

type collectDoneMsg struct {
	generation uint64
	result     Result
}

type attachDoneMsg struct {
	err error
}

type model struct {
	ctx         context.Context
	deps        Dependencies
	result      Result
	visible     []session.Session
	selected    int
	selectedKey sessionKey
	query       string
	searching   bool
	collecting  bool
	generation  uint64
	status      string
	width       int
	height      int
	noColor     bool
	styles      viewStyles
}

func newModel(ctx context.Context, deps Dependencies) model {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	_, noColor := os.LookupEnv("NO_COLOR")
	return model{
		ctx:        ctx,
		deps:       deps,
		collecting: true,
		generation: 1,
		noColor:    deps.NoColor || noColor,
		styles:     newViewStyles(true),
	}
}

func (value model) Init() tea.Cmd {
	return tea.Batch(
		value.collectCommand(value.generation),
		tea.RequestBackgroundColor,
	)
}

func (value model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	updated, command := updateModel(value, message)
	return updated, command
}

func updateModel(value model, message tea.Msg) (model, tea.Cmd) {
	switch message := message.(type) {
	case collectDoneMsg:
		if message.generation != value.generation {
			return value, nil
		}
		value.collecting = false
		value.result = message.result
		value.refreshVisible()
		return value, nil
	case attachDoneMsg:
		if message.err != nil {
			value.status = boundedStatus("attach failed: " + message.err.Error())
		} else {
			value.status = "attach finished"
		}
		return value.restartCollection()
	case tea.BackgroundColorMsg:
		value.styles = newViewStyles(message.IsDark())
		return value, nil
	case tea.WindowSizeMsg:
		value.width = message.Width
		value.height = message.Height
		return value, nil
	case tea.KeyPressMsg:
		return value.updateKey(message)
	default:
		return value, nil
	}
}

func (value model) updateKey(message tea.KeyPressMsg) (model, tea.Cmd) {
	key := message.Key()
	if key.Code == 'c' && key.Mod&tea.ModCtrl != 0 {
		return value, tea.Quit
	}
	if value.searching {
		switch key.Code {
		case tea.KeyEscape, tea.KeyEnter:
			value.searching = false
		case tea.KeyBackspace:
			_, size := utf8.DecodeLastRuneInString(value.query)
			if size > 0 {
				value.query = value.query[:len(value.query)-size]
			}
			value.refreshVisible()
		default:
			if printable(key.Text) {
				value.query += key.Text
				value.refreshVisible()
			}
		}
		return value, nil
	}

	switch key.Code {
	case tea.KeyUp, 'k':
		value.move(-1)
	case tea.KeyDown, 'j':
		value.move(1)
	case '/':
		value.searching = true
	case 'r':
		if value.collecting {
			return value, nil
		}
		return value.restartCollection()
	case tea.KeyEnter:
		if len(value.visible) == 0 {
			return value, nil
		}
		command, err := value.deps.Attach(value.ctx, value.visible[value.selected])
		if err != nil {
			return updateModel(value, attachDoneMsg{err: err})
		}
		return value, tea.Exec(command, func(err error) tea.Msg {
			return attachDoneMsg{err: err}
		})
	case 'q':
		return value, tea.Quit
	}
	return value, nil
}

func (value model) restartCollection() (model, tea.Cmd) {
	value.generation++
	value.collecting = true
	return value, value.collectCommand(value.generation)
}

func (value model) collectCommand(generation uint64) tea.Cmd {
	return func() tea.Msg {
		return collectDoneMsg{generation: generation, result: value.deps.Collect(value.ctx)}
	}
}

func (value *model) refreshVisible() {
	value.visible = filterSessions(displayOrder(value.result.Sessions), value.query, value.deps.LocalTarget)
	if len(value.visible) == 0 {
		value.selected = 0
		value.selectedKey = sessionKey{}
		return
	}
	for index, item := range value.visible {
		if keyOf(item) == value.selectedKey {
			value.selected = index
			return
		}
	}
	value.selected = 0
	value.selectedKey = keyOf(value.visible[0])
}

func (value *model) move(delta int) {
	if len(value.visible) == 0 {
		return
	}
	value.selected = (value.selected + delta + len(value.visible)) % len(value.visible)
	value.selectedKey = keyOf(value.visible[value.selected])
}

func printable(text string) bool {
	if text == "" {
		return false
	}
	for _, character := range text {
		if !unicode.IsPrint(character) {
			return false
		}
	}
	return true
}

func boundedStatus(status string) string {
	if len(status) <= maxStatusBytes {
		return status
	}
	status = status[:maxStatusBytes]
	for !utf8.ValidString(status) {
		status = status[:len(status)-1]
	}
	return status
}
