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

type Update struct {
	Result Result
	Stale  []string
	Done   bool
}

type ExecCommand interface {
	Run() error
	SetStdin(io.Reader)
	SetStdout(io.Writer)
	SetStderr(io.Writer)
}

type Dependencies struct {
	Collect     func(context.Context) <-chan Update
	Attach      func(context.Context, session.Session) (ExecCommand, error)
	LocalTarget string
	Now         func() time.Time
	NoColor     bool
}

type collectUpdateMsg struct {
	generation uint64
	update     Update
	channel    <-chan Update
}

type attachDoneMsg struct {
	err error
}

type model struct {
	ctx            context.Context
	deps           Dependencies
	result         Result
	rows           []listRow
	selected       int
	selectedRef    rowRef
	collapsed      map[string]bool
	query          string
	searching      bool
	collecting     bool
	generation     uint64
	stale          map[string]struct{}
	cancelCollect  context.CancelFunc
	initialCollect tea.Cmd
	status         string
	width          int
	height         int
	noColor        bool
	styles         viewStyles
}

func newModel(ctx context.Context, deps Dependencies) model {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	_, noColor := os.LookupEnv("NO_COLOR")
	value := model{
		ctx:        ctx,
		deps:       deps,
		collecting: true,
		generation: 1,
		noColor:    deps.NoColor || noColor,
		styles:     newViewStyles(true),
	}
	collectCtx, cancel := context.WithCancel(ctx)
	value.cancelCollect = cancel
	value.initialCollect = waitForUpdate(value.generation, deps.Collect(collectCtx))
	return value
}

func (value model) Init() tea.Cmd {
	return tea.Batch(
		value.initialCollect,
		tea.RequestBackgroundColor,
	)
}

func (value model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	updated, command := updateModel(value, message)
	return updated, command
}

func updateModel(value model, message tea.Msg) (model, tea.Cmd) {
	switch message := message.(type) {
	case collectUpdateMsg:
		if message.generation != value.generation {
			return value, nil
		}
		value.result = message.update.Result
		value.stale = make(map[string]struct{}, len(message.update.Stale))
		for _, target := range message.update.Stale {
			value.stale[target] = struct{}{}
		}
		if message.update.Done {
			value.collecting = false
		}
		value.refreshVisible()
		return value, waitForUpdate(message.generation, message.channel)
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
		if value.cancelCollect != nil {
			value.cancelCollect()
		}
		return value, tea.Quit
	}
	if value.searching {
		switch key.Code {
		case tea.KeyEnter:
			value.searching = false
		case tea.KeyEscape:
			value.searching = false
			value.query = ""
			value.refreshVisible()
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
	case 'g', 'G':
		if len(value.rows) == 0 {
			return value, nil
		}
		if key.Text == "G" {
			value.selectRow(len(value.rows) - 1)
		} else {
			value.selectRow(0)
		}
	case tea.KeyPgDown:
		value.movePage(1)
	case tea.KeyPgUp:
		value.movePage(-1)
	case 'd':
		if key.Mod&tea.ModCtrl != 0 {
			value.movePage(1)
		}
	case 'u':
		if key.Mod&tea.ModCtrl != 0 {
			value.movePage(-1)
		}
	case tea.KeyEscape:
		if value.query != "" {
			value.query = ""
			value.refreshVisible()
		}
	case '/':
		value.searching = true
	case 'r':
		if value.collecting {
			return value, nil
		}
		return value.restartCollection()
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
	case 'q':
		if value.cancelCollect != nil {
			value.cancelCollect()
		}
		return value, tea.Quit
	}
	return value, nil
}

func (value model) restartCollection() (model, tea.Cmd) {
	if value.cancelCollect != nil {
		value.cancelCollect()
	}
	collectCtx, cancel := context.WithCancel(value.ctx)
	value.cancelCollect = cancel
	value.generation++
	value.collecting = true
	return value, waitForUpdate(value.generation, value.deps.Collect(collectCtx))
}

func waitForUpdate(generation uint64, channel <-chan Update) tea.Cmd {
	return func() tea.Msg {
		update, ok := <-channel
		if !ok {
			return nil
		}
		return collectUpdateMsg{generation: generation, update: update, channel: channel}
	}
}

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
		ref := refOf(row)
		if ref.kind != value.selectedRef.kind {
			continue
		}
		if ref.kind == rowHeader && ref.project == value.selectedRef.project {
			value.selectRow(index)
			return
		}
		if ref.kind == rowSession && ref.key == value.selectedRef.key {
			value.selectRow(index)
			return
		}
	}
	if value.query != "" {
		value.selectRow(firstSessionRow(value.rows))
		return
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

func (value *model) move(delta int) {
	if len(value.rows) == 0 {
		return
	}
	value.selectRow((value.selected + delta + len(value.rows)) % len(value.rows))
}

func (value *model) movePage(direction int) {
	if len(value.rows) == 0 {
		return
	}
	step := max(1, value.height-8)
	index := value.selected + direction*step
	index = max(0, min(index, len(value.rows)-1))
	value.selectRow(index)
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
