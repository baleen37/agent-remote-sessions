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

	if statusRight != DetachHint() {
		t.Fatalf("status-right = %q, want %q", statusRight, DetachHint())
	}
	if statusInterval != "5" {
		t.Fatalf("status-interval = %q, want %q", statusInterval, "5")
	}
	fixture.cleanupARSServer(t)
	fixture.defaultTmux.assertUnchanged(t)
}

func TestDisposableTmuxReusesExternalProviderWithoutDetachingExistingClient(t *testing.T) {
	testDisposableTmuxReusesExternalProvider(t, 0, 5*time.Second)
}

func TestDisposableTmuxStartsClientWaitAfterExternalAttachStarts(t *testing.T) {
	testDisposableTmuxReusesExternalProvider(t, 750*time.Millisecond, 500*time.Millisecond)
}

func testDisposableTmuxReusesExternalProvider(t *testing.T, resolverDelay, clientWait time.Duration) {
	t.Helper()
	fixture := newDisposableTmuxFixture(t)
	external := newExternalTmuxFixture(t, fixture)

	firstMaster, firstTerminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = firstMaster.Close()
		_ = firstTerminal.Close()
	})
	go func() { _, _ = io.Copy(io.Discard, firstMaster) }()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- external.runner.Run(
			context.Background(), externalTmuxCommand("attach-session", "-t", "=external"),
			firstTerminal, firstTerminal, firstTerminal,
		)
	}()
	external.waitForAttachedClients(t, 1, 5*time.Second, nil, firstDone)
	before := external.snapshot(t)

	attachBase := Runner(fixture.runner)
	if resolverDelay > 0 {
		attachBase = resolverDelayRunner{
			Runner: attachBase,
			delay:  resolverDelay,
			output: []byte("match\t" + external.socket + "\t" + external.paneID(t) + "\n"),
		}
	}
	attachRunner := newExternalAttachStartRunner(attachBase)
	command, err := NewAttachCommand(context.Background(), attachRunner, fixture.item, provider.ResumeSpec{
		Executable: "claude",
		Args:       []string{"--resume", fixture.item.NativeID},
	})
	if err != nil {
		t.Fatal(err)
	}
	secondMaster, secondTerminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = secondMaster.Close()
		_ = secondTerminal.Close()
	})
	go func() { _, _ = io.Copy(io.Discard, secondMaster) }()
	command.SetStdin(secondTerminal)
	command.SetStdout(secondTerminal)
	command.SetStderr(secondTerminal)
	secondDone := make(chan error, 1)
	go func() { secondDone <- command.Run() }()

	waitForExternalAttachStart(t, attachRunner.started, secondDone, func() string {
		return external.attachDiagnostic(fixture)
	})
	external.waitForAttachedClients(t, 2, clientWait, func() string {
		return external.attachDiagnostic(fixture)
	}, secondDone)
	if pid := waitForProviderPID(t, external.pidPath); pid != external.providerPID {
		t.Fatalf("external provider restarted: %d -> %d", external.providerPID, pid)
	}
	if err := fixture.runner.Run(context.Background(), hasSession(Key(string(fixture.item.Provider), fixture.item.NativeID)), nil, io.Discard, io.Discard); err == nil {
		t.Fatal("ARS created a runtime for an external provider")
	}

	if _, err := secondMaster.Write([]byte{0x02, 'd'}); err != nil {
		t.Fatalf("detach external ARS-driven client: %v", err)
	}
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("external attach command after Ctrl+B d: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("external attach command did not return after Ctrl+B d")
	}
	external.waitForAttachedClients(t, 1, 5*time.Second, nil)
	if after := external.snapshot(t); after != before {
		t.Fatalf("external tmux changed:\nbefore: %#v\nafter:  %#v", before, after)
	}

	external.cleanup(t)
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("original external client did not exit during cleanup")
	}
	fixture.cleanupARSServer(t)
	fixture.defaultTmux.assertUnchanged(t)
}

func TestExternalTmuxSnapshotDetectsWindowPaneAndGlobalOptionChanges(t *testing.T) {
	fixture := newDisposableTmuxFixture(t)
	external := newExternalTmuxFixture(t, fixture)

	before := external.snapshot(t)
	if err := external.runner.Run(
		context.Background(),
		externalTmuxCommand("new-window", "-d", "-n", "review-window"),
		nil, io.Discard, io.Discard,
	); err != nil {
		t.Fatalf("create external review window: %v", err)
	}
	afterWindow := external.snapshot(t)
	if afterWindow.windows == before.windows {
		t.Fatal("external snapshot ignored window changes")
	}
	if err := external.runner.Run(
		context.Background(),
		externalTmuxCommand("split-window", "-d", "-t", "review-window"),
		nil, io.Discard, io.Discard,
	); err != nil {
		t.Fatalf("split external review window: %v", err)
	}
	afterPane := external.snapshot(t)
	if afterPane.panes == afterWindow.panes {
		t.Fatal("external snapshot ignored pane-only changes")
	}
	if err := external.runner.Run(
		context.Background(),
		externalTmuxCommand("set-option", "-g", "status-left", "review-left"),
		nil, io.Discard, io.Discard,
	); err != nil {
		t.Fatalf("set external review option: %v", err)
	}
	if afterOptions := external.snapshot(t); afterOptions.globalOptions == afterPane.globalOptions {
		t.Fatal("external snapshot ignored global option changes")
	}
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
	// Defense-in-depth alongside the TMUX_TMPDIR isolation below: give this
	// fixture's ars server its own socket name too, distinct from the shared
	// default. Kept as short as the default "ars-v1" (6 bytes) since the
	// socket path already sits close to the platform's sun_path limit once
	// combined with t.TempDir()'s test-name-derived prefix.
	t.Setenv("ARS_TMUX_SOCKET", "a"+strconv.Itoa(os.Getpid()%100000))
	root := t.TempDir()
	tmuxTemp, err := os.MkdirTemp("", "ars-tmux-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmuxTemp) })
	bin := filepath.Join(root, "bin")
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
	tmuxWrapper := "#!/bin/sh\nif [ \"$1\" = -S ]; then\n  case \"$2\" in /tmp/tmux-" + strconv.Itoa(os.Getuid()) + "/*) exit 0 ;; esac\nfi\nexec \"$ARS_TEST_REAL_TMUX\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(tmuxWrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ARS_TEST_REAL_TMUX", tmux)
	t.Setenv("TMUX_TMPDIR", tmuxTemp)
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
		arsSocket:   filepath.Join(tmuxTemp, "tmux-"+strconv.Itoa(os.Getuid()), SocketName()),
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

type externalTmuxFixture struct {
	runner      tempTmuxRunner
	pidPath     string
	providerPID int
	socket      string
	cleaned     bool
}

type externalTmuxSnapshot struct {
	sessions      string
	windows       string
	panes         string
	keys          string
	globalOptions string
}

func newExternalTmuxFixture(t *testing.T, fixture *disposableTmuxFixture) *externalTmuxFixture {
	t.Helper()
	providerPath := filepath.Join(filepath.Dir(fixture.pidPath), "bin", "claude")
	if err := os.Remove(providerPath); err != nil {
		t.Fatal(err)
	}
	buildExternalProvider(t, providerPath)
	pidPath := filepath.Join(filepath.Dir(fixture.pidPath), "external-provider.pid")
	t.Setenv("ARS_TEST_EXTERNAL_PROVIDER_PID", pidPath)
	external := &externalTmuxFixture{
		runner:  fixture.runner,
		pidPath: pidPath,
		socket: filepath.Join(
			fixture.runner.tempDir,
			"tmux-"+strconv.Itoa(os.Getuid()),
			"external",
		),
	}
	if err := external.runner.Run(
		context.Background(),
		externalTmuxCommand("new-session", "-d", "-s", "external", "-c", filepath.Dir(fixture.pidPath), providerPath, "--resume", fixture.item.NativeID),
		nil, io.Discard, io.Discard,
	); err != nil {
		t.Fatalf("start external tmux provider: %v", err)
	}
	external.providerPID = waitForProviderPID(t, pidPath)
	t.Cleanup(func() { external.cleanup(t) })
	return external
}

func buildExternalProvider(t *testing.T, path string) {
	t.Helper()
	source := path + ".go"
	program := `package main
import (
	"os"
	"strconv"
	"time"
)
func main() {
	if path := os.Getenv("ARS_TEST_EXTERNAL_PROVIDER_PID"); path != "" {
		if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil { panic(err) }
	}
	for { time.Sleep(time.Hour) }
}
`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "build", "-o", path, source)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build external provider: %v: %s", err, output)
	}
}

func externalTmuxCommand(args ...string) Command {
	return Command{
		Name: "tmux",
		Args: append([]string{"-L", "external", "-f", "/dev/null"}, args...),
		Env:  []string{"TMUX=", "TMUX_PANE="},
	}
}

type resolverDelayRunner struct {
	Runner
	delay  time.Duration
	output []byte
}

func (runner resolverDelayRunner) Output(ctx context.Context, command Command) ([]byte, error) {
	if command.Name == "/bin/sh" {
		select {
		case <-time.After(runner.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return append([]byte(nil), runner.output...), nil
	}
	return runner.Runner.Output(ctx, command)
}

type arsCreateRunner struct {
	Runner
	created chan struct{}
	once    sync.Once
}

func newARSCreateRunner(runner Runner) *arsCreateRunner {
	return &arsCreateRunner{Runner: runner, created: make(chan struct{})}
}

func (runner *arsCreateRunner) Run(ctx context.Context, command Command, stdin io.Reader, stdout, stderr io.Writer) error {
	err := runner.Runner.Run(ctx, command, stdin, stdout, stderr)
	if command.Name == "tmux" && len(command.Args) > 4 && command.Args[4] == "new-session" {
		runner.once.Do(func() { close(runner.created) })
	}
	return err
}

func TestARSCreateWaitDeadlineUsesShorterLimit(t *testing.T) {
	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name         string
		testDeadline time.Time
		want         time.Time
	}{
		{name: "fixed timeout", testDeadline: now.Add(time.Minute), want: now.Add(5 * time.Second)},
		{name: "test deadline", testDeadline: now.Add(time.Second), want: now.Add(time.Second)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := arsCreateWaitDeadline(now, test.testDeadline); !got.Equal(test.want) {
				t.Fatalf("arsCreateWaitDeadline() = %v, want %v", got, test.want)
			}
		})
	}
}

func arsCreateWaitDeadline(now, testDeadline time.Time) time.Time {
	fixedDeadline := now.Add(5 * time.Second)
	if testDeadline.Before(fixedDeadline) {
		return testDeadline
	}
	return fixedDeadline
}

func waitForARSCreate(t *testing.T, created <-chan struct{}, done <-chan error) {
	t.Helper()
	testDeadline, ok := t.Deadline()
	if !ok {
		t.Fatal("test deadline is required to wait for ARS creation")
	}
	deadline := arsCreateWaitDeadline(time.Now(), testDeadline)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-created:
	case err := <-done:
		t.Fatalf("attach exited before ARS creation: %v", err)
	case <-timer.C:
		t.Fatal("ARS creation did not finish before wait deadline")
	}
}

type externalAttachStartRunner struct {
	Runner
	started chan struct{}
	once    sync.Once
}

func newExternalAttachStartRunner(runner Runner) *externalAttachStartRunner {
	return &externalAttachStartRunner{Runner: runner, started: make(chan struct{})}
}

func (runner *externalAttachStartRunner) Run(ctx context.Context, command Command, stdin io.Reader, stdout, stderr io.Writer) error {
	if command.Name == "tmux" && len(command.Args) > 4 && command.Args[0] == "-S" && command.Args[4] == "attach-session" {
		runner.once.Do(func() { close(runner.started) })
	}
	return runner.Runner.Run(ctx, command, stdin, stdout, stderr)
}

func waitForExternalAttachStart(t *testing.T, started <-chan struct{}, done <-chan error, diagnostic func() string) {
	t.Helper()
	deadline, ok := t.Deadline()
	if !ok {
		t.Fatal("test deadline is required to wait for external attach start")
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-started:
		return
	case err := <-done:
		t.Fatalf("external attach exited before starting: %v\n%s", err, diagnosticText(diagnostic))
	case <-timer.C:
		t.Fatalf("external attach did not start before test deadline\n%s", diagnosticText(diagnostic))
	}
}

func (external *externalTmuxFixture) waitForAttachedClients(t *testing.T, want int, deadline time.Duration, diagnostic func() string, dones ...<-chan error) {
	t.Helper()
	until := time.Now().Add(deadline)
	var lastOutput []byte
	var lastErr error
	for time.Now().Before(until) {
		for _, done := range dones {
			select {
			case err := <-done:
				t.Fatalf("external client exited before attached clients became %d: %v\n%s", want, err, diagnosticText(diagnostic))
			default:
			}
		}
		output, err := external.runner.Output(context.Background(), externalTmuxCommand("list-sessions", "-F", "#{session_attached}"))
		if err == nil && strings.TrimSpace(string(output)) == strconv.Itoa(want) {
			return
		}
		lastOutput, lastErr = output, err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("external attached clients did not become %d: output %q, error %v\n%s", want, lastOutput, lastErr, diagnosticText(diagnostic))
}

func diagnosticText(diagnostic func() string) string {
	if diagnostic == nil {
		return ""
	}
	return diagnostic()
}

func (external *externalTmuxFixture) attachDiagnostic(fixture *disposableTmuxFixture) string {
	query := func(args ...string) string {
		output, err := external.runner.Output(context.Background(), externalTmuxCommand(args...))
		return fmt.Sprintf("output=%q error=%v", output, err)
	}
	target, found, resolveErr := ResolveExternal(context.Background(), fixture.runner, fixture.item.Provider, fixture.item.NativeID)
	arsErr := fixture.runner.Run(context.Background(), hasSession(Key(string(fixture.item.Provider), fixture.item.NativeID)), nil, io.Discard, io.Discard)
	providerAlive := processExists(external.providerPID) == nil
	return fmt.Sprintf(
		"external socket=%t provider pid=%d alive=%t\nresolver target=%#v found=%t error=%v\nARS has-session error=%v\nexternal clients: %s\nexternal sessions: %s",
		pathExists(external.socket), external.providerPID, providerAlive,
		target, found, resolveErr, arsErr,
		query("list-clients", "-F", "#{client_pid}\t#{client_tty}\t#{client_session}"),
		query("list-sessions", "-F", "#{session_id}\t#{session_name}\t#{session_attached}"),
	)
}

func (external *externalTmuxFixture) paneID(t *testing.T) string {
	t.Helper()
	output, err := external.runner.Output(context.Background(), externalTmuxCommand("list-panes", "-t", "=external", "-F", "#{session_id}:#{window_index}.#{pane_index}"))
	if err != nil {
		t.Fatalf("read external pane id: %v", err)
	}
	pane := strings.TrimSpace(string(output))
	if !validExternalPane(pane) {
		t.Fatalf("invalid external pane id: %q", pane)
	}
	return pane
}

func (external *externalTmuxFixture) snapshot(t *testing.T) externalTmuxSnapshot {
	t.Helper()
	output := func(args ...string) string {
		value, err := external.runner.Output(context.Background(), externalTmuxCommand(args...))
		if err != nil {
			t.Fatalf("external tmux %q: %v", args, err)
		}
		return string(value)
	}
	return externalTmuxSnapshot{
		sessions:      output("list-sessions", "-F", "#{session_id}\t#{session_name}\t#{session_attached}\t#{session_created}"),
		windows:       output("list-windows", "-a", "-F", "#{session_id}\t#{window_id}\t#{window_index}\t#{window_name}\t#{window_panes}"),
		panes:         output("list-panes", "-a", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{window_index}\t#{pane_index}\t#{pane_pid}"),
		keys:          output("list-keys", "-T", "root"),
		globalOptions: output("show-options", "-g"),
	}
}

func (external *externalTmuxFixture) cleanup(t *testing.T) {
	t.Helper()
	if external.cleaned {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := cleanupOwnedTmux(ctx, func(ctx context.Context) error {
		return external.runner.Run(ctx, externalTmuxCommand("kill-server"), nil, io.Discard, io.Discard)
	}, external.socket, external.providerPID)
	if err != nil {
		t.Fatalf("cleanup external tmux: %v", err)
	}
	external.cleaned = true
}

func (fixture *disposableTmuxFixture) attachAndDetach(t *testing.T) int {
	t.Helper()
	runner := newARSCreateRunner(resolverDelayRunner{Runner: fixture.runner, output: []byte("none\n")})
	command, err := NewAttachCommand(context.Background(), runner, fixture.item, provider.ResumeSpec{
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

	waitForARSCreate(t, runner.created, done)
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
	runner := newARSCreateRunner(resolverDelayRunner{Runner: fixture.runner, output: []byte("none\n")})
	command, err := NewAttachCommand(context.Background(), runner, fixture.item, provider.ResumeSpec{
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

	waitForARSCreate(t, runner.created, done)
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
	return externalSystemRunner{}.Output(ctx, runner.command(command))
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
