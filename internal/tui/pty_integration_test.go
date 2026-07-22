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

func TestPTYTmuxCleanupReportsKillError(t *testing.T) {
	want := errors.New("kill failed")
	err := cleanupPTYTmux(context.Background(), func(context.Context) error { return want }, "unused", 0)
	if !errors.Is(err, want) {
		t.Fatalf("cleanup error = %v, want wrapped kill error", err)
	}
}

func TestPTYTmuxCleanupReportsLeaks(t *testing.T) {
	socket := filepath.Join(t.TempDir(), arsruntime.SocketName)
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
	runner := ptyTempTmuxRunner{tempDir: tmuxTemp}
	socket := filepath.Join(tmuxTemp, "tmux-"+strconv.Itoa(os.Getuid()), arsruntime.SocketName)
	providerPID := 0
	cleaned := false
	cleanup := func() {
		if cleaned {
			return
		}
		if err := cleanupPTYFixture(runner, socket, providerPID); err != nil {
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
		Collect: func(ctx context.Context) Result {
			runtimes, report := arsruntime.Inspect(ctx, runner, []session.Candidate{candidate})
			state := runtimes[arsruntime.Key(string(candidate.Provider), candidate.NativeID)]
			item, bindErr := session.BindDiscovered("local-node", session.Discovered{Candidate: candidate, Runtime: state})
			if bindErr != nil {
				return Result{Errors: []output.HostError{{Host: "local-node", Code: "protocol_error", Message: bindErr.Error()}}}
			}
			result := Result{Hosts: []output.HostResult{{Target: "local-node", Status: output.HostOK}}, Sessions: []session.Session{item}}
			if report.Status == arsruntime.StatusUnavailable {
				result.Warnings = []output.HostError{{Host: "local-node", Code: report.ErrorCode, Message: "Runtime inspection unavailable"}}
			}
			return result
		},
		Attach: func(ctx context.Context, item session.Session) (ExecCommand, error) {
			return arsruntime.NewAttachCommand(ctx, runner, item, provider.ResumeSpec{
				Executable: "claude",
				Args:       []string{"--resume", item.NativeID},
			})
		},
		LocalTarget: "local-node",
		Now:         func() time.Time { return time.Date(2026, 7, 20, 2, 2, 3, 0, time.UTC) },
		NoColor:     true,
	}
	runDone := make(chan error, 1)
	go func() { runDone <- Run(ctx, dependencies, terminal, terminal) }()

	waitForPTYOutput(t, &capture, runDone, func(value string) bool {
		return strings.Contains(value, "ars  0 active") && strings.Contains(value, "PTY fixture provider")
	}, "initial ARS TUI")
	if _, err := master.Write([]byte{'\r'}); err != nil {
		t.Fatalf("write Enter: %v", err)
	}
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
		Args: append([]string{"-L", arsruntime.SocketName, "-f", "/dev/null"}, args...),
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
