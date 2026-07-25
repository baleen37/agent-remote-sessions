package tui

import (
	"context"
	"io"
	"os"
	"strings"
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
	Preview     func(context.Context, session.Session) ([]byte, error)
	Kill        func(context.Context, session.Session) error
	Send        func(ctx context.Context, item session.Session, text string) error
	LocalTarget string
	Now         func() time.Time
	NoColor     bool
}

type collectUpdateMsg struct {
	generation uint64
	update     Update
	channel    <-chan Update
}

type spinnerTickMsg struct {
	generation uint64
}

type attachDoneMsg struct {
	err error
}

type model struct {
	ctx               context.Context
	deps              Dependencies
	result            Result
	rows              []listRow
	selected          int
	selectedRef       rowRef
	groupMode         map[string]groupMode
	stateFilter       map[session.RuntimeState]bool
	waitingFilter     bool
	showAll           bool
	staleHidden       int
	query             string
	matched           int
	searching         bool
	composing         bool
	compose           string
	composeTarget     session.Session
	showHelp          bool
	previewOn         bool
	previewFullscreen bool
	previewKey        sessionKey
	previewContent    []string
	previewErr        string
	previewPending    bool
	activity          map[sessionKey]activityEntry
	activityPending   map[sessionKey]bool
	pins              map[sessionKey]bool
	killSeq           uint64
	killPending       bool
	killTargets       []session.Session
	killGroup         string
	collecting        bool
	spinner           int
	generation        uint64
	stale             map[string]struct{}
	cancelCollect     context.CancelFunc
	initialCollect    tea.Cmd
	status            string
	width             int
	height            int
	noColor           bool
	styles            viewStyles
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
		previewOn:  true,
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
		spinnerTick(value.generation),
		activityTick(),
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
		value.evictActivity()
		return value, tea.Batch(waitForUpdate(message.generation, message.channel), value.syncPreview())
	case previewMsg:
		return value.updatePreview(message)
	case previewTickMsg:
		return value.updatePreviewTick(message)
	case activityMsg:
		return value.updateActivity(message)
	case activityTickMsg:
		return value.updateActivityTick(message)
	case killFireMsg:
		return value.updateKillFire(message)
	case killDoneMsg:
		return value.updateKillDone(message)
	case sendDoneMsg:
		return value.updateSendDone(message)
	case spinnerTickMsg:
		if message.generation != value.generation || !value.collecting {
			return value, nil
		}
		value.spinner = (value.spinner + 1) % len(spinnerFrames)
		return value, spinnerTick(value.generation)
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
		// A terminal narrowed below previewMinWidth leaves nothing to show
		// fullscreen, so drop back to the list rather than render an empty view.
		if value.previewFullscreen && !value.previewVisible() {
			value.previewFullscreen = false
		}
		return value, value.syncPreview()
	case tea.KeyPressMsg:
		updated, command := value.updateKey(message)
		if command != nil {
			return updated, command
		}
		return updated, updated.syncPreview()
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
	if value.showHelp {
		switch {
		case key.Text == "?", key.Code == tea.KeyEscape, key.Code == 'q':
			value.showHelp = false
		}
		return value, nil
	}
	if value.searching {
		if key.Code == 'u' && key.Mod&tea.ModCtrl != 0 {
			value.query = ""
			value.refreshVisible()
			return value, nil
		}
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
	if value.composing {
		if key.Code == 'u' && key.Mod&tea.ModCtrl != 0 {
			value.compose = ""
			return value, nil
		}
		switch key.Code {
		case tea.KeyEnter:
			return value.submitCompose()
		case tea.KeyEscape:
			value.composing = false
			value.compose = ""
			value.composeTarget = session.Session{}
		case tea.KeyBackspace:
			_, size := utf8.DecodeLastRuneInString(value.compose)
			if size > 0 {
				value.compose = value.compose[:len(value.compose)-size]
			}
		default:
			if printable(key.Text) {
				value.compose += key.Text
			}
		}
		return value, nil
	}

	// The fullscreen preview hides the list, so it swallows every key but the
	// ones below — like the help overlay — rather than moving or acting on a
	// selection the user cannot see. p exits and closes the pane, keeping the
	// two states consistent.
	if value.previewFullscreen {
		switch {
		case key.Text == "f", key.Code == tea.KeyEscape:
			value.previewFullscreen = false
		case key.Text == "p":
			value.previewFullscreen = false
			value.previewOn = false
			value = value.closePreview()
		case key.Text == "?":
			// The overlay features the f binding, so it has to stay reachable
			// from here. Leaving fullscreen means closing the overlay returns to
			// the split view rather than a mode the user can no longer see.
			value.showHelp = true
			value.previewFullscreen = false
		case key.Code == 'q':
			// Unlike the help overlay — a transient thing q dismisses —
			// fullscreen is a regular view, so q quits ars as it does in the
			// list rather than closing the view.
			if value.cancelCollect != nil {
				value.cancelCollect()
			}
			return value, tea.Quit
		}
		return value, nil
	}

	switch key.Code {
	case tea.KeyUp, 'k':
		value.move(-1)
	case tea.KeyDown, 'j':
		value.move(1)
	case 'g', 'G', tea.KeyHome, tea.KeyEnd:
		if len(value.rows) == 0 {
			return value, nil
		}
		if key.Text == "G" || key.Code == tea.KeyEnd {
			value.selectRow(len(value.rows) - 1)
		} else {
			value.selectRow(0)
		}
	case tea.KeyPgDown:
		value.movePage(1)
	case tea.KeyPgUp:
		value.movePage(-1)
	case tea.KeyLeft, 'h':
		value.foldLeft()
	case tea.KeyRight, 'l':
		value.foldRight()
	case 'd':
		if key.Mod&tea.ModCtrl != 0 {
			value.movePage(1)
		}
	case 'u':
		if key.Mod&tea.ModCtrl != 0 {
			value.movePage(-1)
		} else {
			value = value.cancelKill()
		}
	case 'x':
		if row, ok := value.selectedRow(); ok {
			return value.startKill(row)
		}
	case 'm':
		if row, ok := value.selectedRow(); ok {
			return value.startCompose(row)
		}
	case tea.KeyEscape:
		if value.query != "" {
			value.query = ""
			value.refreshVisible()
		} else if value.filterActive() {
			value.stateFilter = nil
			value.waitingFilter = false
			value.refreshVisible()
		}
	case '/':
		value.searching = true
	case 'p', 'P':
		if key.Text == "P" {
			if row, ok := value.selectedRow(); ok {
				value.togglePin(row)
			}
			return value, nil
		}
		value.previewOn = !value.previewOn
		if !value.previewOn {
			value = value.closePreview()
		}
	case 'f':
		if value.previewVisible() {
			value.previewFullscreen = true
		}
	case '?':
		value.showHelp = true
	case 'a':
		value.showAll = !value.showAll
		value.refreshVisible()
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
	case 'q':
		if value.cancelCollect != nil {
			value.cancelCollect()
		}
		return value, tea.Quit
	default:
		if len(key.Text) == 1 && key.Text[0] >= '1' && key.Text[0] <= '9' {
			value.jumpToGroup(int(key.Text[0] - '0'))
		}
		switch key.Text {
		case "!":
			value.toggleStateFilter(session.RuntimeAttached)
		case "@":
			value.toggleStateFilter(session.RuntimeRunning)
		case "#":
			value.toggleStateFilter(session.RuntimeSaved)
		case "$":
			value.waitingFilter = !value.waitingFilter
			value.refreshVisible()
		}
	}
	return value, nil
}

// jumpToGroup selects the Nth rowHeader in the visible rows (1-indexed). It
// is a no-op if fewer than n groups are visible.
func (value *model) jumpToGroup(n int) {
	count := 0
	for index, row := range value.rows {
		if row.kind != rowHeader {
			continue
		}
		count++
		if count == n {
			value.selectRow(index)
			return
		}
	}
}

func (value model) restartCollection() (model, tea.Cmd) {
	if value.cancelCollect != nil {
		value.cancelCollect()
	}
	collectCtx, cancel := context.WithCancel(value.ctx)
	value.cancelCollect = cancel
	value.generation++
	value.collecting = true
	value.spinner = 0
	value.killPending = false
	value.killTargets = nil
	value.killGroup = ""
	return value, tea.Batch(
		waitForUpdate(value.generation, value.deps.Collect(collectCtx)),
		spinnerTick(value.generation),
	)
}

func spinnerTick(generation uint64) tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg {
		return spinnerTickMsg{generation: generation}
	})
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
	value.staleHidden = 0
	if value.query == "" {
		_, value.staleHidden = filterByStale(value.result.Sessions, value.deps.Now(), value.showAll, value.pins)
	}
	filtered := value.visibleSessions()
	value.matched = len(filtered)
	value.rows = buildRows(filtered, value.groupMode, value.query != "", value.pins)
	value.restoreSelection()
}

// visibleSessions is the inventory the rows are built from: everything the
// stale cutoff, active state filter and search query admit, folded or not.
// The cutoff applies first and is bypassed entirely once a search query is
// active, since search is the recovery path for finding an old session. The
// state filters and $ form one union — each admits its own sessions — and the
// query then narrows whatever that union produced.
func (value model) visibleSessions() []session.Session {
	items := value.result.Sessions
	if value.query == "" {
		items, _ = filterByStale(items, value.deps.Now(), value.showAll, value.pins)
	}
	filtered := value.filterByActiveStates(items)
	return filterSessions(filtered, value.query, value.deps.LocalTarget)
}

// filterByActiveStates applies the state and needs-input filters as a union. It
// dedupes on the way out: a waiting session whose runtime state is also filtered
// on matches both halves and must still appear once.
func (value model) filterByActiveStates(items []session.Session) []session.Session {
	if !value.waitingFilter {
		return filterByState(items, value.stateFilter)
	}
	admitted := make(map[sessionKey]struct{}, len(items))
	for _, item := range filterByWaiting(items, value.activity) {
		admitted[keyOf(item)] = struct{}{}
	}
	for _, item := range items {
		if value.stateFilter[item.Runtime.State] {
			admitted[keyOf(item)] = struct{}{}
		}
	}
	filtered := make([]session.Session, 0, len(admitted))
	for _, item := range items {
		if _, ok := admitted[keyOf(item)]; ok {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// toggleStateFilter flips the given runtime state in the active filter set.
func (value *model) toggleStateFilter(state session.RuntimeState) {
	if value.stateFilter == nil {
		value.stateFilter = make(map[session.RuntimeState]bool)
	}
	if value.stateFilter[state] {
		delete(value.stateFilter, state)
	} else {
		value.stateFilter[state] = true
	}
	value.refreshVisible()
}

func (value model) filterActive() bool {
	return len(value.stateFilter) > 0 || value.waitingFilter
}

// emptyFilterMessage names the active state/needs-input filters so the empty
// list explains why, e.g. "no attached / running sessions · esc to clear".
// A needs-input-only filter reads as "no sessions need input" instead, since
// "no needs-input sessions" doesn't scan as English; combined with a state
// filter it folds into the joined list as "needs-input".
func (value model) emptyFilterMessage() string {
	var names []string
	for _, state := range []session.RuntimeState{session.RuntimeAttached, session.RuntimeRunning, session.RuntimeSaved} {
		if value.stateFilter[state] {
			names = append(names, string(state))
		}
	}
	if len(names) == 0 && value.waitingFilter {
		return "no sessions need input · esc to clear"
	}
	if value.waitingFilter {
		names = append(names, "needs-input")
	}
	return "no " + strings.Join(names, " / ") + " sessions · esc to clear"
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
		if ref.kind == rowSession && ref.key == value.selectedRef.key {
			value.selectRow(index)
			return
		}
		if ref.kind != rowSession && ref.project == value.selectedRef.project {
			value.selectRow(index)
			return
		}
	}
	if value.query != "" {
		value.selectRow(firstSessionRow(value.rows))
		return
	}
	if value.selectedRef.kind != rowHeader {
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

// foldLeft mirrors vim tree navigation: children jump to their group header,
// an expanded header collapses, and a collapsed header stays put.
func (value *model) foldLeft() {
	row, ok := value.selectedRow()
	if !ok {
		return
	}
	if row.kind != rowHeader {
		value.selectHeader(row.project)
		return
	}
	if !row.collapsed {
		value.toggle(row.project)
	}
}

// foldRight expands a collapsed header, steps into the first child of an
// expanded header, and reveals hidden sessions on a more row.
func (value *model) foldRight() {
	row, ok := value.selectedRow()
	if !ok {
		return
	}
	switch row.kind {
	case rowMore:
		value.openGroup(row.project)
	case rowHeader:
		if row.collapsed {
			value.toggle(row.project)
			return
		}
		if next := value.selected + 1; next < len(value.rows) && value.rows[next].project == row.project {
			value.selectRow(next)
		}
	}
}

func (value *model) selectHeader(project string) {
	for index, row := range value.rows {
		if row.kind == rowHeader && row.project == project {
			value.selectRow(index)
			return
		}
	}
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
	index := value.selected + direction*value.pageStep()
	index = max(0, min(index, len(value.rows)-1))
	value.selectRow(index)
}

func (value model) pageStep() int {
	_, width := contentFrame(value.contentWidth())
	var details []string
	selected, hasSelection := value.selectedSession()
	if hasSelection {
		details = detailLines(selected, width, value.deps.Now())
	}
	searchLines := 0
	if value.query != "" || value.searching || value.composing {
		searchLines = 1
	}
	diagnostics := value.diagnostics(width)
	_, _, bodyHeight := value.boundedLayout(details, selected, diagnostics, searchLines, width)
	return max(1, bodyHeight)
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
