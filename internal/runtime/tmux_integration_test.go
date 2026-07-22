package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/creack/pty"
)

func TestDisposableTmuxPreservesProviderAfterDetach(t *testing.T) {
	fixture := newDisposableTmuxFixture(t)

	beforePID := fixture.attachAndDetach(t)
	afterDetachPID, attachedClients := fixture.runtimeState(t)

	if beforePID != afterDetachPID {
		t.Fatalf("provider restarted: %d -> %d", beforePID, afterDetachPID)
	}
	if attachedClients != 0 {
		t.Fatalf("clients after Ctrl+Q = %d", attachedClients)
	}
	fixture.assertUserTmuxUntouched(t)
}

type disposableTmuxFixture struct {
	runner             tempTmuxRunner
	item               session.Session
	pidPath            string
	userTmuxBefore     string
	userTmuxExecutable string
}

func newDisposableTmuxFixture(t *testing.T) *disposableTmuxFixture {
	t.Helper()
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux integration unavailable: tmux was not found")
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

	item, err := session.BindDiscovered("local-node", session.Discovered{Candidate: session.Candidate{
		Provider:  session.Claude,
		NativeID:  "11111111-1111-1111-1111-111111111111",
		UpdatedAt: time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC),
		CWD:       root,
		Title:     "Disposable tmux provider",
	}, Runtime: session.Runtime{State: session.RuntimeSaved}})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &disposableTmuxFixture{
		runner:             tempTmuxRunner{tempDir: tmuxTemp},
		item:               item,
		pidPath:            pidPath,
		userTmuxBefore:     snapshotUserTmux(t, tmux),
		userTmuxExecutable: tmux,
	}
	t.Cleanup(func() {
		_ = fixture.runner.Run(context.Background(), arsTMUXCommand("kill-server"), nil, io.Discard, io.Discard)
	})
	return fixture
}

func (fixture *disposableTmuxFixture) attachAndDetach(t *testing.T) int {
	t.Helper()
	command, err := NewAttachCommand(context.Background(), fixture.runner, fixture.item, provider.ResumeSpec{
		Executable: "claude",
		Args:       []string{"--resume", fixture.item.NativeID},
	})
	if err != nil {
		t.Fatal(err)
	}
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = master.Close()
		_ = terminal.Close()
	})
	command.SetStdin(terminal)
	command.SetStdout(terminal)
	command.SetStderr(terminal)
	done := make(chan error, 1)
	go func() { done <- command.Run() }()
	var output synchronizedBuffer
	go func() { _, _ = io.Copy(&output, master) }()

	beforePID := waitForProviderPIDOrAttachExit(t, fixture.pidPath, done, &output)
	waitForAttachedClients(t, fixture.runner, fixture.item, 1)
	if _, err := master.Write([]byte{0x11}); err != nil {
		t.Fatalf("write Ctrl+Q: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("attach command after Ctrl+Q: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("attach command did not return after Ctrl+Q")
	}
	return beforePID
}

type synchronizedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (buffer *synchronizedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.b.Write(value)
}

func (buffer *synchronizedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.b.String()
}

func (fixture *disposableTmuxFixture) runtimeState(t *testing.T) (int, int) {
	t.Helper()
	afterPID := waitForProviderPID(t, fixture.pidPath)
	runtimes, report := Inspect(context.Background(), fixture.runner, []session.Candidate{fixture.item.Candidate})
	if report.Status != StatusOK {
		t.Fatalf("runtime report = %#v", report)
	}
	state := runtimes[Key(string(fixture.item.Provider), fixture.item.NativeID)]
	if state.State != session.RuntimeRunning {
		t.Fatalf("runtime after detach = %#v, want running", state)
	}
	return afterPID, state.AttachedClients
}

func (fixture *disposableTmuxFixture) assertUserTmuxUntouched(t *testing.T) {
	t.Helper()
	after := snapshotUserTmux(t, fixture.userTmuxExecutable)
	if after != fixture.userTmuxBefore {
		t.Fatalf("default user tmux changed:\nbefore: %s\nafter:  %s", fixture.userTmuxBefore, after)
	}
}

type tempTmuxRunner struct{ tempDir string }

func (runner tempTmuxRunner) Output(ctx context.Context, command Command) ([]byte, error) {
	return SystemRunner{}.Output(ctx, runner.command(command))
}

func (runner tempTmuxRunner) Run(ctx context.Context, command Command, stdin io.Reader, stdout, stderr io.Writer) error {
	return SystemRunner{}.Run(ctx, runner.command(command), stdin, stdout, stderr)
}

func (runner tempTmuxRunner) command(command Command) Command {
	command.Env = append([]string(nil), command.Env...)
	for index, value := range command.Env {
		if strings.HasPrefix(value, "TMUX_TMPDIR=") {
			command.Env[index] = "TMUX_TMPDIR=" + runner.tempDir
		}
	}
	return command
}

func waitForProviderPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && pid > 0 {
				if processErr := processExists(pid); processErr == nil {
					return pid
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("provider PID did not appear at %s", path)
	return 0
}

func waitForProviderPIDOrAttachExit(t *testing.T, path string, done <-chan error, output *synchronizedBuffer) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("attach exited before provider started: %v; terminal output: %q", err, output.String())
		default:
		}
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && pid > 0 && processExists(pid) == nil {
				return pid
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("provider PID did not appear at %s; terminal output: %q", path, output.String())
	return 0
}

func processExists(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(syscall.Signal(0))
}

func waitForAttachedClients(t *testing.T, runner Runner, item session.Session, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtimes, report := Inspect(context.Background(), runner, []session.Candidate{item.Candidate})
		state := runtimes[Key(string(item.Provider), item.NativeID)]
		if report.Status == StatusOK && state.AttachedClients == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("attached clients did not become %d", want)
}

func snapshotUserTmux(t *testing.T, tmux string) string {
	t.Helper()
	command := exec.Command(tmux, "list-sessions", "-F", "#{session_id}\\t#{session_name}\\t#{session_created}")
	command.Env = append(os.Environ(), "TMUX=", "TMUX_PANE=")
	output, err := command.CombinedOutput()
	if err == nil {
		return string(output)
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return "no-server:" + string(output)
	}
	t.Fatalf("snapshot default tmux: %v: %s", err, output)
	return fmt.Sprint(err)
}
