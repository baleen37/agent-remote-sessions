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

const (
	maxStatusBytes       = 256
	interactionIdle      = 300 * time.Millisecond
	statusDismissSeconds = 5
	statusTickInterval   = time.Second
)

type Result struct {
	Hosts    []output.HostResult
	Sessions []session.Session
	Errors   []output.HostError
	Warnings []output.HostError
}

type Update struct {
	Result  Result
	Loading []string
	Done    bool
}

type ExecCommand interface {
	Run() error
	SetStdin(io.Reader)
	SetStdout(io.Writer)
	SetStderr(io.Writer)
}

type Dependencies struct {
	Collect        func(context.Context) <-chan Update
	Attach         func(context.Context, session.Session) (ExecCommand, error)
	Preview        func(context.Context, session.Session) ([]byte, error)
	PreviewHistory func(context.Context, session.Session) ([]byte, error)
	Kill           func(context.Context, session.Session) error
	Send           func(ctx context.Context, item session.Session, text string) error
	LocalTarget    string
	Now            func() time.Time
	NoColor        bool
	Version        string
	PreviewPct     int
	SavePreviewPct func(int) error
}

type collectUpdateMsg struct {
	generation uint64
	update     Update
	channel    <-chan Update
}

type spinnerTickMsg struct {
	generation uint64
}

type interactionIdleMsg struct {
	generation uint64
	sequence   uint64
}

type attachDoneMsg struct {
	err error
}

// statusTickMsg drives the error-status auto-dismiss countdown, one second at
// a time. seq guards it the same way killFireMsg guards the kill grace period
// (kill.go): a message whose seq no longer matches value.statusSeq belongs to
// a status that has since changed, and is ignored.
type statusTickMsg struct {
	seq uint64
}

func statusTick(seq uint64) tea.Cmd {
	return tea.Tick(statusTickInterval, func(time.Time) tea.Msg {
		return statusTickMsg{seq: seq}
	})
}

// updateStatusTick advances the auto-dismiss countdown. The guard re-checks
// isErrorStatus, not just the seq, in case a non-key message path (PR #48)
// changed value.status without going through updateModel's before/after
// comparison in a way that left a stale seq match.
func (value model) updateStatusTick(message statusTickMsg) (model, tea.Cmd) {
	if message.seq != value.statusSeq || !isErrorStatus(value.status) {
		return value, nil
	}
	if value.statusRemaining <= 1 {
		value.status = ""
		value.statusRemaining = 0
		return value, nil
	}
	value.statusRemaining--
	return value, statusTick(message.seq)
}

type model struct {
	ctx                    context.Context
	deps                   Dependencies
	result                 Result
	rows                   []listRow
	selected               int
	selectedRef            rowRef
	groupMode              map[string]groupMode
	stateFilter            map[session.RuntimeState]bool
	waitingFilter          bool
	showAll                bool
	staleHidden            int
	query                  string
	matched                int
	searching              bool
	composing              bool
	compose                string
	composeTarget          session.Session
	showHelp               bool
	helpFromFullscreen     bool
	previewOn              bool
	previewFullscreen      bool
	previewKey             sessionKey
	previewContent         []string
	previewErr             string
	previewPending         bool
	previewFullKey         sessionKey
	previewFullContent     []string
	previewFullErr         string
	previewFullPending     bool
	previewPct             int
	splitFlash             bool
	splitFlashSeq          uint64
	previewScrollOffset    int
	previewSearching       bool
	previewSearchQuery     string
	previewSearchActive    bool
	previewSearchNoMatches bool
	previewSearchMatches   []int
	previewSearchIndex     int
	activity               map[sessionKey]activityEntry
	activityPending        map[sessionKey]bool
	pins                   map[sessionKey]bool
	killSeq                uint64
	killPending            bool
	killTargets            []session.Session
	killGroup              string
	collecting             bool
	pendingUpdate          *Update
	coalescing             bool
	interactionSeq         uint64
	spinner                int
	generation             uint64
	loading                []string
	cancelCollect          context.CancelFunc
	initialCollect         tea.Cmd
	status                 string
	statusSeq              uint64
	statusRemaining        int
	emptyDiagnosticFloor   int
	width                  int
	height                 int
	noColor                bool
	styles                 viewStyles
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
		previewPct: clampPreviewPct(deps.PreviewPct),
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

// updateModel wraps dispatchModel to centrally arm the error-status
// auto-dismiss timer: statusTickMsg itself must not re-arm (it never changes
// value.status), so it is handled before the before/after status capture.
// Every other message goes through dispatchModel, and if that changed
// value.status, statusSeq bumps to invalidate any timer already in flight.
// Setting the same string again is not a change, so it does not reset the
// countdown — that's intentional, not an oversight.
func updateModel(value model, message tea.Msg) (model, tea.Cmd) {
	if tick, ok := message.(statusTickMsg); ok {
		updated, command := value.updateStatusTick(tick)
		return updated.settleEmptyDiagnosticFloor(message), command
	}
	before := value.status
	updated, command := dispatchModel(value, message)
	if updated.status != before {
		updated.statusSeq++
		if isErrorStatus(updated.status) {
			updated.statusRemaining = statusDismissSeconds
			command = tea.Batch(command, statusTick(updated.statusSeq))
		} else {
			updated.statusRemaining = 0
		}
	}
	return updated.settleEmptyDiagnosticFloor(message), command
}

// settleEmptyDiagnosticFloor tracks emptyDiagnosticFloor, the lower bound
// View applies to the empty-state tier's diagnostic-line reservation. A
// status auto-dismissing (statusTickMsg clearing value.status) is not a user
// action, so the empty state must not promote to a taller tier on its own
// mid-countdown; the floor holds the reservation at its highest level seen
// since the last real user action. A user action (a keypress or a resize)
// resets the floor to the current count instead, so the tier can shrink or
// grow freely again once the user has actually done something. Every other
// message (collection updates, ticks, ...) only raises the floor, never
// lowers it, so a diagnostic that legitimately clears on its own (e.g. a
// host reconnecting) still can't demote until a user action confirms it.
func (value model) settleEmptyDiagnosticFloor(message tea.Msg) model {
	count := len(value.diagnostics(value.width))
	switch message.(type) {
	case tea.KeyPressMsg, tea.WindowSizeMsg:
		value.emptyDiagnosticFloor = count
	default:
		value.emptyDiagnosticFloor = max(value.emptyDiagnosticFloor, count)
	}
	return value
}

func dispatchModel(value model, message tea.Msg) (model, tea.Cmd) {
	switch message := message.(type) {
	case collectUpdateMsg:
		if message.generation != value.generation {
			return value, nil
		}
		if value.coalescing {
			update := message.update
			value.pendingUpdate = &update
			return value, waitForUpdate(message.generation, message.channel)
		}
		value.applyCollectionUpdate(message.update)
		return value, tea.Batch(waitForUpdate(message.generation, message.channel), value.syncPreview(), value.syncFullPreview())
	case interactionIdleMsg:
		if message.generation != value.generation || message.sequence != value.interactionSeq || !value.coalescing {
			return value, nil
		}
		value.coalescing = false
		if value.pendingUpdate == nil {
			return value, nil
		}
		update := *value.pendingUpdate
		value.pendingUpdate = nil
		value.applyCollectionUpdate(update)
		return value, tea.Batch(value.syncPreview(), value.syncFullPreview())
	case previewMsg:
		return value.updatePreview(message)
	case previewTickMsg:
		return value.updatePreviewTick(message)
	case fullPreviewMsg:
		return value.updateFullPreview(message)
	case fullPreviewTickMsg:
		return value.updateFullPreviewTick(message)
	case splitFlashMsg:
		return value.updateSplitFlash(message)
	case savePreviewPctMsg:
		return value.updateSavePreviewPct(message)
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
			value.helpFromFullscreen = false
		}
		return value, tea.Batch(value.syncPreview(), value.syncFullPreview())
	case tea.KeyPressMsg:
		interaction := value.collecting && value.isRelevantInteraction(message)
		updated, command := value.updateKey(message)
		if interaction {
			updated.coalescing = true
			updated.interactionSeq++
			idle := interactionIdleTick(updated.generation, updated.interactionSeq)
			if command != nil {
				return updated, tea.Batch(command, idle)
			}
			return updated, tea.Batch(updated.syncPreview(), idle)
		}
		if command != nil {
			return updated, command
		}
		return updated, tea.Batch(updated.syncPreview(), updated.syncFullPreview())
	default:
		return value, nil
	}
}

func (value *model) applyCollectionUpdate(update Update) {
	value.result = update.Result
	value.loading = update.Loading
	if update.Done {
		value.collecting = false
	}
	value.refreshVisible()
	value.evictActivity()
	// A session dying or its host dropping out can leave restoreSelection on
	// a non-session row while fullscreen is still open; without this the
	// panel keeps rendering the empty frame PR #45 eliminated at the other
	// entry points. Mirrors the WindowSizeMsg exit in Update.
	if value.previewFullscreen {
		if _, ok := value.selectedSession(); !ok {
			value.previewFullscreen = false
			value.helpFromFullscreen = false
		}
	}
}

func (value model) isRelevantInteraction(message tea.KeyPressMsg) bool {
	key := message.Key()
	if value.searching {
		return key.Code == tea.KeyEnter ||
			key.Code == tea.KeyEscape ||
			key.Code == tea.KeyBackspace ||
			(key.Code == 'u' && key.Mod&tea.ModCtrl != 0) ||
			printable(key.Text)
	}
	if value.composing || value.showHelp || value.previewFullscreen {
		return false
	}
	switch key.Code {
	case tea.KeyUp, tea.KeyDown, tea.KeyHome, tea.KeyEnd, tea.KeyPgDown, tea.KeyPgUp,
		tea.KeyLeft, tea.KeyRight, tea.KeySpace:
		return true
	case 'j', 'k', 'g', 'G', 'h', 'l', '/', 'a':
		return true
	case 'd', 'u':
		return key.Mod&tea.ModCtrl != 0
	case tea.KeyEscape:
		return value.query != "" || value.filterActive()
	case tea.KeyEnter:
		row, ok := value.selectedRow()
		return ok && (row.kind == rowHeader || row.kind == rowMore)
	}
	if len(key.Text) != 1 {
		return false
	}
	return (key.Text[0] >= '1' && key.Text[0] <= '9') ||
		key.Text == "!" || key.Text == "@" || key.Text == "#" ||
		key.Text == "$" || key.Text == "P"
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
			value.helpFromFullscreen = false
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
	// two states consistent. j/k and the page keys scroll the captured
	// scrollback instead of moving the (hidden) list selection.
	if value.previewFullscreen {
		if value.previewSearching {
			return value.updateFullscreenSearchInput(key), nil
		}
		switch {
		case key.Text == "f":
			value.previewFullscreen = false
		case key.Code == tea.KeyEscape:
			// Esc hierarchy: an active search is cleared before it closes
			// fullscreen, so the user gets their scrollback view back first.
			if value.previewSearchActive {
				value = value.clearFullscreenSearch()
			} else {
				value.previewFullscreen = false
			}
		case key.Text == "p":
			value.previewFullscreen = false
			value.previewOn = false
			value = value.closePreview()
		case key.Text == "?":
			// The overlay features the f binding, so it has to stay reachable
			// from here. Leaving fullscreen means closing the overlay returns to
			// the split view rather than a mode the user can no longer see.
			// helpFromFullscreen remembers the context was fullscreen, purely
			// for the overlay's featured section — previewFullscreen itself
			// stays false so closing help lands in the split view.
			value.showHelp = true
			value.helpFromFullscreen = true
			value.previewFullscreen = false
		case key.Code == 'q':
			// Unlike the help overlay — a transient thing q dismisses —
			// fullscreen is a regular view, so q quits ars as it does in the
			// list rather than closing the view.
			if value.cancelCollect != nil {
				value.cancelCollect()
			}
			return value, tea.Quit
		case key.Text == "/":
			value.previewSearching = true
			value.previewSearchQuery = ""
		case key.Text == "n" && value.previewSearchActive:
			value.advanceFullscreenSearch(1)
		case key.Text == "N" && value.previewSearchActive:
			value.advanceFullscreenSearch(-1)
		case key.Code == tea.KeyUp, key.Code == 'k':
			value.scrollFullscreen(1)
		case key.Code == tea.KeyDown, key.Code == 'j':
			value.scrollFullscreen(-1)
		case key.Code == tea.KeyPgUp:
			value.scrollFullscreen(fullscreenPageLines(value.height))
		case key.Code == tea.KeyPgDown:
			value.scrollFullscreen(-fullscreenPageLines(value.height))
		case key.Code == 'u' && key.Mod&tea.ModCtrl != 0:
			value.scrollFullscreen(fullscreenPageLines(value.height))
		case key.Code == 'd' && key.Mod&tea.ModCtrl != 0:
			value.scrollFullscreen(-fullscreenPageLines(value.height))
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
		if row, ok := value.selectedRow(); ok && row.kind == rowSession && value.previewVisible() {
			value = value.enterFullscreen()
		}
	case '<', '>':
		if value.previewVisible() {
			return value.adjustSplit(key.Code == '>')
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
	value.pendingUpdate = nil
	value.coalescing = false
	value.loading = nil
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

func interactionIdleTick(generation, sequence uint64) tea.Cmd {
	return tea.Tick(interactionIdle, func(time.Time) tea.Msg {
		return interactionIdleMsg{generation: generation, sequence: sequence}
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
// filter it folds into the joined list as "needs-input". If sessions are also
// stale-hidden, esc to clear isn't the whole story, so a suffix names the a
// key as the other recovery path.
func (value model) emptyFilterMessage() string {
	var names []string
	for _, state := range []session.RuntimeState{session.RuntimeAttached, session.RuntimeRunning, session.RuntimeSaved} {
		if value.stateFilter[state] {
			names = append(names, string(state))
		}
	}
	var message string
	if len(names) == 0 && value.waitingFilter {
		message = "no sessions need input · esc to clear"
	} else {
		if value.waitingFilter {
			names = append(names, "needs-input")
		}
		message = "no " + strings.Join(names, " / ") + " sessions · esc to clear"
	}
	if value.staleHidden > 0 {
		message += " · a to show older"
	}
	return message
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
	if value.previewVisible() && previewLayoutOf(width) == previewStacked {
		if listRows, _, ok := value.stackedHeights(bodyHeight); ok {
			return max(1, listRows)
		}
	}
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
