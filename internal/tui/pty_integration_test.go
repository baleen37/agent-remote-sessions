package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/output"
	"github.com/baleen37/agent-remote-sessions/internal/provider"
	arsruntime "github.com/baleen37/agent-remote-sessions/internal/runtime"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
)

func TestPTYAttachDetachRestoresTUI(t *testing.T) {
	result := runPTYAttachDetachFixture(t)

	if result.beforePID != result.afterDetachPID {
		t.Fatalf("provider restarted: %d -> %d", result.beforePID, result.afterDetachPID)
	}
	if result.attachedClients != 0 {
		t.Fatalf("clients after Ctrl+Q = %d", result.attachedClients)
	}
	if result.headerCount < 2 {
		t.Fatalf("ARS header count = %d, want at least 2", result.headerCount)
	}
	if !result.rawModeRestored || !result.cursorRestored || !result.alternateScreenRestored {
		t.Fatalf("terminal restoration = raw:%v cursor:%v alternate:%v", result.rawModeRestored, result.cursorRestored, result.alternateScreenRestored)
	}
}

func TestPTYProgressiveUpdatesStayStableDuringNavigation(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "1")

	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	// 150 columns (not the usual 120) keeps "initial session NN" and "final
	// session NN" titles surviving the preview split's truncation: these
	// fixture sessions are all on a remote host, so their [server] location
	// badge claims two more columns from the shared title/location budget
	// than an unbracketed host did.
	if err := pty.Setsize(master, &pty.Winsize{Rows: 24, Cols: 150}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = master.Close()
		_ = terminal.Close()
	})

	var capture ptyCapture
	readDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&capture, master)
		close(readDone)
	}()

	collection := make(chan Update)
	attached := make(chan session.Session, 1)
	previewed := make(chan session.Session, 8)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dependencies := Dependencies{
		Collect: func(context.Context) <-chan Update { return collection },
		Attach: func(_ context.Context, item session.Session) (ExecCommand, error) {
			attached <- item
			return nil, errors.New("fixture attach stopped")
		},
		Preview: func(_ context.Context, item session.Session) ([]byte, error) {
			previewed <- item
			return []byte("fixture preview"), nil
		},
		LocalTarget: "localhost",
		Now:         func() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) },
		NoColor:     true,
	}
	runDone := make(chan error, 1)
	go func() { runDone <- Run(ctx, dependencies, terminal, terminal) }()

	cache := progressivePTYResult("initial")
	collection <- Update{Result: cache, Loading: []string{"cached"}}
	waitForPTYOutput(t, &capture, runDone, func(value string) bool {
		// The default 65%-preview split still narrows the list column enough
		// to truncate the full "initial session NN" title even at 150
		// columns, so this only asserts the surviving prefix.
		return strings.Contains(value, "initial s") &&
			strings.Contains(value, "refreshing")
	}, "initial progressive snapshot")
	waitForPTYPreview(t, previewed, cache.Sessions[0].NativeID, "initial selected session")
	for _, forbidden := range []string{"cached", "recent-first", "complete", "loading server"} {
		if strings.Contains(capture.String(), forbidden) {
			t.Fatalf("initial PTY output exposed phase %q: %q", forbidden, capture.String())
		}
	}

	if _, err := master.Write([]byte{'j'}); err != nil {
		t.Fatalf("write first navigation: %v", err)
	}
	waitForPTYPreview(t, previewed, cache.Sessions[1].NativeID, "navigated second session")
	navigationStart := len(capture.String())
	collection <- Update{Result: progressivePTYResult("early"), Loading: []string{"recent-first"}}
	collection <- Update{Result: progressivePTYResult("final"), Loading: []string{"complete"}, Done: true}
	close(collection)
	if _, err := master.Write([]byte{'k', 'j'}); err != nil {
		t.Fatalf("write navigation reset: %v", err)
	}
	seen := make(map[string]bool)
	for range 2 {
		item := waitForAnyPTYPreview(t, previewed, "navigation while updates are pending")
		if !strings.HasPrefix(item.Title, "initial") {
			t.Fatalf("previewed staged title during navigation: %#v", item)
		}
		seen[item.NativeID] = true
	}
	for _, want := range []string{cache.Sessions[0].NativeID, cache.Sessions[1].NativeID} {
		if !seen[want] {
			t.Fatalf("navigation previews = %v, missing %s", seen, want)
		}
	}
	beforeIdle := capture.String()[navigationStart:]
	if strings.Contains(beforeIdle, "early") || strings.Contains(beforeIdle, "final") {
		t.Fatalf("PTY applied staged snapshot during navigation: %q", beforeIdle)
	}

	finalStart := len(capture.String())
	waitForPTYOutput(t, &capture, runDone, func(value string) bool {
		// See the initial-snapshot assertion above: the narrowed list column
		// truncates the full title, so only the surviving prefix is checked.
		return strings.Contains(value[finalStart:], "final se")
	}, "final snapshot after interaction idle")
	finalDelta := capture.String()[finalStart:]
	for _, forbidden := range []string{"recent-first", "complete", "loading server"} {
		if strings.Contains(finalDelta, forbidden) {
			t.Fatalf("final PTY output exposed phase %q: %q", forbidden, finalDelta)
		}
	}

	if _, err := master.Write([]byte{'\r'}); err != nil {
		t.Fatalf("write Enter after final snapshot: %v", err)
	}
	select {
	case item := <-attached:
		if item.NativeID != cache.Sessions[1].NativeID || item.Title != "final session 01" {
			t.Fatalf("attached selection = %#v, want final canonical second session", item)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for selected attach; output: %q", capture.String())
	}

	if _, err := master.Write([]byte{'q'}); err != nil {
		t.Fatalf("write q: %v", err)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("TUI exit: %v; output: %q", err, capture.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("TUI did not exit after q; output: %q", capture.String())
	}
	_ = terminal.Close()
	_ = master.Close()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("PTY reader did not terminate")
	}
}

func waitForPTYPreview(t *testing.T, previewed <-chan session.Session, nativeID, label string) session.Session {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case item := <-previewed:
			if item.NativeID == nativeID {
				return item
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s (%s)", label, nativeID)
		}
	}
}

func waitForAnyPTYPreview(t *testing.T, previewed <-chan session.Session, label string) session.Session {
	t.Helper()
	select {
	case item := <-previewed:
		return item
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return session.Session{}
	}
}

func progressivePTYResult(stage string) Result {
	items := manySessions(3)
	for index := range items {
		items[index].Title = fmt.Sprintf("%s session %02d", stage, index)
	}
	return Result{
		Hosts:    []output.HostResult{{Target: "server", Status: output.HostOK}},
		Sessions: items,
	}
}

func TestPTYTmuxCleanupReportsKillError(t *testing.T) {
	want := errors.New("kill failed")
	err := cleanupPTYTmux(context.Background(), func(context.Context) error { return want }, "unused", 0)
	if !errors.Is(err, want) {
		t.Fatalf("cleanup error = %v, want wrapped kill error", err)
	}
}

func TestPTYTmuxCleanupReportsLeaks(t *testing.T) {
	socket := filepath.Join(t.TempDir(), arsruntime.SocketName())
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := cleanupPTYTmux(ctx, func(context.Context) error { return nil }, socket, os.Getpid())
	if err == nil || !strings.Contains(err.Error(), "provider PID") || !strings.Contains(err.Error(), socket) {
		t.Fatalf("cleanup error = %v, want exact socket and provider PID leak", err)
	}
}

type ptyAttachDetachResult struct {
	beforePID               int
	afterDetachPID          int
	attachedClients         int
	headerCount             int
	rawModeRestored         bool
	cursorRestored          bool
	alternateScreenRestored bool
}

func runPTYAttachDetachFixture(t *testing.T) ptyAttachDetachResult {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("PTY tmux integration unavailable: tmux was not found")
	}
	t.Setenv("TMPDIR", "/tmp")
	// Defense-in-depth alongside the TMUX_TMPDIR isolation below: give this
	// fixture's ars server its own socket name too, distinct from the shared
	// default. Kept as short as the default "ars-v1" (6 bytes) since the
	// socket path already sits close to the platform's sun_path limit once
	// combined with t.TempDir()'s test-name-derived prefix.
	t.Setenv("ARS_TMUX_SOCKET", "a"+strconv.Itoa(os.Getpid()%100000))
	root := t.TempDir()
	tmuxTemp := filepath.Join(root, "tmux")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(tmuxTemp, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(root, "provider.pid")
	providerPath := filepath.Join(bin, "claude")
	providerScript := "#!/bin/sh\n" +
		"printf '%s\\n' \"$$\" > \"$ARS_TEST_PROVIDER_PID\"\n" +
		"printf 'ARS_FAKE_PROVIDER_ATTACHED\\n'\n" +
		"trap 'exit 0' TERM INT HUP\n" +
		"while :; do sleep 1; done\n"
	if err := os.WriteFile(providerPath, []byte(providerScript), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ARS_TEST_PROVIDER_PID", pidPath)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "1")

	candidate := session.Candidate{
		Provider:  session.Claude,
		NativeID:  "11111111-1111-1111-1111-111111111111",
		UpdatedAt: time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC),
		CWD:       root,
		Title:     "PTY fixture provider",
	}
	tmuxRunner := ptyTempTmuxRunner{tempDir: tmuxTemp}
	runner := newPTYARSCreateRunner(tmuxRunner)
	socket := filepath.Join(tmuxTemp, "tmux-"+strconv.Itoa(os.Getuid()), arsruntime.SocketName())
	providerPID := 0
	cleaned := false
	cleanup := func() {
		if cleaned {
			return
		}
		if err := cleanupPTYFixture(tmuxRunner, socket, providerPID); err != nil {
			t.Errorf("cleanup PTY ARS tmux: %v", err)
			return
		}
		cleaned = true
	}
	t.Cleanup(cleanup)

	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := pty.Setsize(master, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = master.Close()
		_ = terminal.Close()
	})
	initialState, err := term.GetState(terminal.Fd())
	if err != nil {
		t.Fatalf("read initial terminal state: %v", err)
	}

	var capture ptyCapture
	readDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&capture, master)
		close(readDone)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dependencies := Dependencies{
		Collect: func(ctx context.Context) <-chan Update {
			collect := func() Result {
				runtimes, report := arsruntime.Inspect(ctx, runner, []session.Candidate{candidate})
				state := runtimes[arsruntime.Key(string(candidate.Provider), candidate.NativeID)]
				item, bindErr := session.BindDiscovered("localhost", session.Discovered{Candidate: candidate, Runtime: state})
				if bindErr != nil {
					return Result{Errors: []output.HostError{{Host: "localhost", Code: "protocol_error", Message: bindErr.Error()}}}
				}
				result := Result{Hosts: []output.HostResult{{Target: "localhost", Status: output.HostOK}}, Sessions: []session.Session{item}}
				if report.Status == arsruntime.StatusUnavailable {
					result.Warnings = []output.HostError{{Host: "localhost", Code: report.ErrorCode, Message: "Runtime inspection unavailable"}}
				}
				return result
			}
			channel := make(chan Update, 1)
			channel <- Update{Result: collect(), Done: true}
			close(channel)
			return channel
		},
		Attach: func(ctx context.Context, item session.Session) (ExecCommand, error) {
			return arsruntime.NewAttachCommand(ctx, runner, item, provider.ResumeSpec{
				Executable: "claude",
				Args:       []string{"--resume", item.NativeID},
			})
		},
		LocalTarget: "localhost",
		Now:         func() time.Time { return time.Date(2026, 7, 20, 2, 2, 3, 0, time.UTC) },
		NoColor:     true,
	}
	runDone := make(chan error, 1)
	go func() { runDone <- Run(ctx, dependencies, terminal, terminal) }()

	waitForPTYOutput(t, &capture, runDone, func(value string) bool {
		return strings.Contains(value, "ars  ○ 1 idle") && strings.Contains(value, "▸")
	}, "initial ARS TUI with collapsed saved group")
	if _, err := master.Write([]byte{'\r'}); err != nil {
		t.Fatalf("write Enter to expand group: %v", err)
	}
	waitForPTYOutput(t, &capture, runDone, func(value string) bool {
		return strings.Contains(value, "PTY fixture provider")
	}, "expanded saved group")
	if _, err := master.Write([]byte{'j'}); err != nil {
		t.Fatalf("write j: %v", err)
	}
	waitForPTYOutput(t, &capture, runDone, func(value string) bool {
		return strings.Contains(value, "PTY fixture provider") && strings.Contains(value, "\b\b> ")
	}, "selected fixture session")
	if _, err := master.Write([]byte{'\r'}); err != nil {
		t.Fatalf("write Enter to attach: %v", err)
	}
	waitForPTYARSCreate(t, runner.created, runDone, &capture)
	beforePID := waitForPTYPID(t, pidPath, runDone, &capture)
	providerPID = beforePID
	waitForPTYOutput(t, &capture, runDone, func(value string) bool {
		return strings.Contains(value, "ARS_FAKE_PROVIDER_ATTACHED")
	}, "fake provider attach")
	waitForPTYClients(t, runner, candidate, 1)

	if _, err := master.Write([]byte{0x11}); err != nil {
		t.Fatalf("write Ctrl+Q: %v", err)
	}
	waitForPTYOutput(t, &capture, runDone, func(value string) bool {
		return strings.Contains(value, "attach finished") && strings.Contains(value, "running  1h")
	}, "restored and refreshed ARS TUI")
	afterDetachPID := waitForPTYPID(t, pidPath, runDone, &capture)
	runtimes, report := arsruntime.Inspect(context.Background(), runner, []session.Candidate{candidate})
	if report.Status != arsruntime.StatusOK {
		t.Fatalf("runtime report after detach = %#v", report)
	}
	state := runtimes[arsruntime.Key(string(candidate.Provider), candidate.NativeID)]
	if _, err := master.Write([]byte{'q'}); err != nil {
		t.Fatalf("write q: %v", err)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("TUI exit: %v; output: %q", err, capture.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("TUI did not exit after q; output: %q", capture.String())
	}
	finalState, err := term.GetState(terminal.Fd())
	if err != nil {
		t.Fatalf("read final terminal state: %v", err)
	}
	waitForTerminalRestoreOutput(t, &capture)
	outputText := capture.String()
	enterAlternate := strings.LastIndex(outputText, "\x1b[?1049h")
	exitAlternate := strings.LastIndex(outputText, "\x1b[?1049l")
	hideCursor := strings.LastIndex(outputText, "\x1b[?25l")
	showCursor := strings.LastIndex(outputText, "\x1b[?25h")

	_ = terminal.Close()
	_ = master.Close()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("PTY reader did not terminate")
	}
	cleanup()
	return ptyAttachDetachResult{
		beforePID:               beforePID,
		afterDetachPID:          afterDetachPID,
		attachedClients:         state.AttachedClients,
		headerCount:             strings.Count(outputText, "ars  "),
		rawModeRestored:         reflect.DeepEqual(initialState, finalState),
		cursorRestored:          hideCursor >= 0 && showCursor > hideCursor,
		alternateScreenRestored: enterAlternate >= 0 && exitAlternate > enterAlternate,
	}
}

func waitForTerminalRestoreOutput(t *testing.T, capture *ptyCapture) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		output := capture.String()
		if strings.LastIndex(output, "\x1b[?1049l") > strings.LastIndex(output, "\x1b[?1049h") &&
			strings.LastIndex(output, "\x1b[?25h") > strings.LastIndex(output, "\x1b[?25l") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("terminal restoration output missing: %q", capture.String())
}

type ptyCapture struct {
	mu sync.Mutex
	b  strings.Builder
}

func (capture *ptyCapture) Write(value []byte) (int, error) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.b.Write(value)
}

func (capture *ptyCapture) String() string {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.b.String()
}

type ptyTempTmuxRunner struct{ tempDir string }

type ptyARSCreateRunner struct {
	arsruntime.Runner
	created chan struct{}
	once    sync.Once
}

func newPTYARSCreateRunner(runner arsruntime.Runner) *ptyARSCreateRunner {
	return &ptyARSCreateRunner{Runner: runner, created: make(chan struct{})}
}

func (runner *ptyARSCreateRunner) Run(
	ctx context.Context,
	command arsruntime.Command,
	stdin io.Reader,
	stdout, stderr io.Writer,
) error {
	err := runner.Runner.Run(ctx, command, stdin, stdout, stderr)
	if command.Name == "tmux" && len(command.Args) > 4 && command.Args[4] == "new-session" {
		runner.once.Do(func() { close(runner.created) })
	}
	return err
}

func waitForPTYARSCreate(
	t *testing.T,
	created <-chan struct{},
	runDone <-chan error,
	capture *ptyCapture,
) {
	t.Helper()
	deadline, ok := t.Deadline()
	if !ok {
		t.Fatal("test deadline is required to wait for ARS creation")
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-created:
	case err := <-runDone:
		t.Fatalf("TUI exited before ARS creation: %v; output: %q", err, capture.String())
	case <-timer.C:
		t.Fatalf("ARS creation did not finish before test deadline; output: %q", capture.String())
	}
}

func cleanupPTYFixture(runner ptyTempTmuxRunner, socket string, providerPID int) error {
	if !ptyPathExists(socket) && providerPID == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return cleanupPTYTmux(ctx, func(ctx context.Context) error {
		var stderr strings.Builder
		if err := runner.Run(ctx, ptyTmuxCommand("kill-server"), nil, io.Discard, &stderr); err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil
	}, socket, providerPID)
}

func cleanupPTYTmux(ctx context.Context, kill func(context.Context) error, socket string, providerPID int) error {
	if err := kill(ctx); err != nil {
		return fmt.Errorf("kill owned tmux server: %w", err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		providerAlive := providerPID > 0 && ptyProcessExists(providerPID)
		if !providerAlive {
			if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove owned tmux socket %s: %w", socket, err)
			}
		}
		socketAlive := ptyPathExists(socket)
		if !providerAlive && !socketAlive {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("owned tmux cleanup deadline: socket %s exists=%v; provider PID %d alive=%v: %w", socket, socketAlive, providerPID, providerAlive, ctx.Err())
		case <-ticker.C:
		}
	}
}

func ptyProcessExists(pid int) bool {
	process, err := os.FindProcess(pid)
	return err == nil && process.Signal(syscall.Signal(0)) == nil
}

func ptyPathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func (runner ptyTempTmuxRunner) Output(ctx context.Context, command arsruntime.Command) ([]byte, error) {
	return arsruntime.SystemRunner{}.Output(ctx, runner.command(command))
}

func (runner ptyTempTmuxRunner) Run(ctx context.Context, command arsruntime.Command, stdin io.Reader, stdout, stderr io.Writer) error {
	return arsruntime.SystemRunner{}.Run(ctx, runner.command(command), stdin, stdout, stderr)
}

func (runner ptyTempTmuxRunner) command(command arsruntime.Command) arsruntime.Command {
	command.Env = append([]string(nil), command.Env...)
	for index, value := range command.Env {
		if strings.HasPrefix(value, "TMUX_TMPDIR=") {
			command.Env[index] = "TMUX_TMPDIR=" + runner.tempDir
		}
	}
	return command
}

func ptyTmuxCommand(args ...string) arsruntime.Command {
	return arsruntime.Command{
		Name: "tmux",
		Args: append([]string{"-L", arsruntime.SocketName(), "-f", "/dev/null"}, args...),
		Env:  []string{"TMUX=", "TMUX_PANE=", "TMUX_TMPDIR=/tmp"},
	}
}

func waitForPTYOutput(t *testing.T, capture *ptyCapture, runDone <-chan error, ready func(string) bool, label string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-runDone:
			t.Fatalf("TUI exited before %s: %v; output: %q", label, err, capture.String())
		default:
		}
		if ready(capture.String()) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; output: %q", label, capture.String())
}

func waitForPTYPID(t *testing.T, path string, runDone <-chan error, capture *ptyCapture) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-runDone:
			t.Fatalf("TUI exited before provider PID appeared: %v; output: %q", err, capture.String())
		default:
		}
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && pid > 0 {
				return pid
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("provider PID did not appear at %s; output: %q", path, capture.String())
	return 0
}

func waitForPTYClients(t *testing.T, runner arsruntime.Runner, candidate session.Candidate, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtimes, report := arsruntime.Inspect(context.Background(), runner, []session.Candidate{candidate})
		state := runtimes[arsruntime.Key(string(candidate.Provider), candidate.NativeID)]
		if report.Status == arsruntime.StatusOK && state.AttachedClients == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("attached clients did not become %d", want)
}
