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
	fixture.cleanupARSServer(t)
	fixture.defaultTmux.assertUnchanged(t)
}

func TestDisposableTmuxSetsStatusOptionsWhileAttached(t *testing.T) {
	fixture := newDisposableTmuxFixture(t)

	statusRight, statusInterval := fixture.attachAndReadStatusOptions(t)

	if statusRight != DetachHint {
		t.Fatalf("status-right = %q, want %q", statusRight, DetachHint)
	}
	if statusInterval != "5" {
		t.Fatalf("status-interval = %q, want %q", statusInterval, "5")
	}
	fixture.cleanupARSServer(t)
	fixture.defaultTmux.assertUnchanged(t)
}

func TestOwnedTmuxCleanupReportsKillError(t *testing.T) {
	want := errors.New("kill failed")
	err := cleanupOwnedTmux(context.Background(), func(context.Context) error { return want }, "unused", 0)
	if !errors.Is(err, want) {
		t.Fatalf("cleanup error = %v, want wrapped kill error", err)
	}
}

func TestOwnedTmuxCleanupReportsLeaks(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "ars-v1")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := cleanupOwnedTmux(ctx, func(context.Context) error { return nil }, socket, os.Getpid())
	if err == nil || !strings.Contains(err.Error(), "provider PID") || !strings.Contains(err.Error(), socket) {
		t.Fatalf("cleanup error = %v, want exact socket and provider PID leak", err)
	}
}

type disposableTmuxFixture struct {
	runner      tempTmuxRunner
	item        session.Session
	pidPath     string
	arsSocket   string
	defaultTmux *defaultTmuxSentinel
	arsCleaned  bool
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
		runner:      tempTmuxRunner{tempDir: tmuxTemp},
		item:        item,
		pidPath:     pidPath,
		arsSocket:   filepath.Join(tmuxTemp, "tmux-"+strconv.Itoa(os.Getuid()), SocketName),
		defaultTmux: newDefaultTmuxSentinel(t, tmux, tmuxTemp),
	}
	if fixture.arsSocket == fixture.defaultTmux.socket {
		t.Fatal("ARS and default tmux sentinel resolved to the same socket")
	}
	t.Cleanup(func() {
		fixture.cleanupARSServer(t)
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

// attachAndReadStatusOptions attaches, reads the live status-right and
// status-interval options off the ars tmux server, then detaches so the
// caller can still run cleanup.
func (fixture *disposableTmuxFixture) attachAndReadStatusOptions(t *testing.T) (statusRight, statusInterval string) {
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

	waitForProviderPIDOrAttachExit(t, fixture.pidPath, done, &output)
	waitForAttachedClients(t, fixture.runner, fixture.item, 1)

	statusRight = fixture.showOption(t, "status-right")
	statusInterval = fixture.showOption(t, "status-interval")

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
	return statusRight, statusInterval
}

func (fixture *disposableTmuxFixture) showOption(t *testing.T, name string) string {
	t.Helper()
	output, err := fixture.runner.Output(context.Background(), arsTMUXCommand("show-options", "-g", "-v", name))
	if err != nil {
		t.Fatalf("show-options -g %s: %v", name, err)
	}
	return strings.TrimSuffix(string(output), "\n")
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

func (fixture *disposableTmuxFixture) cleanupARSServer(t *testing.T) {
	t.Helper()
	if fixture.arsCleaned {
		return
	}
	providerPID := readProviderPIDIfPresent(t, fixture.pidPath)
	if !pathExists(fixture.arsSocket) && providerPID == 0 {
		fixture.arsCleaned = true
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := cleanupOwnedTmux(ctx, func(ctx context.Context) error {
		var stderr strings.Builder
		if err := fixture.runner.Run(ctx, arsTMUXCommand("kill-server"), nil, io.Discard, &stderr); err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil
	}, fixture.arsSocket, providerPID)
	if err != nil {
		t.Fatalf("cleanup disposable ARS tmux: %v", err)
	}
	fixture.arsCleaned = true
}

type defaultTmuxSentinel struct {
	executable string
	tempDir    string
	socket     string
	pid        int
	before     defaultTmuxSnapshot
	cleaned    bool
}

type defaultTmuxSnapshot struct {
	sessions string
	ctrlQ    string
}

func newDefaultTmuxSentinel(t *testing.T, tmux, tempDir string) *defaultTmuxSentinel {
	t.Helper()
	sentinel := &defaultTmuxSentinel{
		executable: tmux,
		tempDir:    tempDir,
		socket:     filepath.Join(tempDir, "tmux-"+strconv.Itoa(os.Getuid()), "default"),
	}
	sentinel.run(t, "new-session", "-d", "-s", "default-sentinel")
	t.Cleanup(func() { sentinel.cleanup(t) })
	pid, err := strconv.Atoi(strings.TrimSpace(sentinel.output(t, "list-panes", "-t", "=default-sentinel", "-F", "#{pane_pid}")))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid default tmux sentinel PID: %d (%v)", pid, err)
	}
	sentinel.pid = pid
	sentinel.run(t, "bind-key", "-n", "C-q", "display-message", "default-sentinel")
	sentinel.before = sentinel.snapshot(t)
	return sentinel
}

func (sentinel *defaultTmuxSentinel) assertUnchanged(t *testing.T) {
	t.Helper()
	after := sentinel.snapshot(t)
	if after != sentinel.before {
		t.Fatalf("test-owned default tmux changed:\nbefore: %#v\nafter:  %#v", sentinel.before, after)
	}
}

func (sentinel *defaultTmuxSentinel) snapshot(t *testing.T) defaultTmuxSnapshot {
	t.Helper()
	sessions := sentinel.output(t, "list-sessions", "-F", "#{session_id}\\t#{session_name}\\t#{session_created}")
	keys := sentinel.output(t, "list-keys", "-T", "root")
	var ctrlQ []string
	for _, line := range strings.Split(strings.TrimSpace(keys), "\n") {
		if strings.Contains(line, " C-q ") {
			ctrlQ = append(ctrlQ, line)
		}
	}
	if len(ctrlQ) == 0 {
		t.Fatal("test-owned default tmux has no C-q list-keys state")
	}
	return defaultTmuxSnapshot{sessions: sessions, ctrlQ: strings.Join(ctrlQ, "\n")}
}

func (sentinel *defaultTmuxSentinel) cleanup(t *testing.T) {
	t.Helper()
	if sentinel.cleaned {
		return
	}
	if !pathExists(sentinel.socket) {
		sentinel.cleaned = true
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := cleanupOwnedTmux(ctx, func(ctx context.Context) error {
		command := defaultTmuxCommand(ctx, sentinel.executable, sentinel.tempDir, "kill-server")
		output, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}, sentinel.socket, sentinel.pid)
	if err != nil {
		t.Fatalf("cleanup test-owned default tmux: %v", err)
	}
	sentinel.cleaned = true
}

func (sentinel *defaultTmuxSentinel) run(t *testing.T, args ...string) {
	t.Helper()
	command := defaultTmuxCommand(context.Background(), sentinel.executable, sentinel.tempDir, args...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run test-owned default tmux %q: %v: %s", args, err, output)
	}
}

func (sentinel *defaultTmuxSentinel) output(t *testing.T, args ...string) string {
	t.Helper()
	command := defaultTmuxCommand(context.Background(), sentinel.executable, sentinel.tempDir, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("query test-owned default tmux %q: %v: %s", args, err, output)
	}
	return string(output)
}

func defaultTmuxCommand(ctx context.Context, tmux, tempDir string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, tmux, append([]string{"-f", "/dev/null"}, args...)...)
	command.Env = isolatedTmuxEnv(tempDir)
	return command
}

func isolatedTmuxEnv(tempDir string) []string {
	environment := make([]string, 0, len(os.Environ())+3)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "TMUX=") || strings.HasPrefix(value, "TMUX_PANE=") || strings.HasPrefix(value, "TMUX_TMPDIR=") {
			continue
		}
		environment = append(environment, value)
	}
	return append(environment, "TMUX=", "TMUX_PANE=", "TMUX_TMPDIR="+tempDir)
}

func cleanupOwnedTmux(ctx context.Context, kill func(context.Context) error, socket string, providerPID int) error {
	if err := kill(ctx); err != nil {
		return fmt.Errorf("kill owned tmux server: %w", err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		providerAlive := providerPID > 0 && processExists(providerPID) == nil
		if !providerAlive {
			if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove owned tmux socket %s: %w", socket, err)
			}
		}
		socketAlive := pathExists(socket)
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

func readProviderPIDIfPresent(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid provider PID in %s: %q", path, data)
	}
	return pid
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
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
