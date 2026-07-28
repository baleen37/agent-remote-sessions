package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

type externalRunner struct {
	output  []byte
	err     error
	command Command
}

func (runner *externalRunner) Output(_ context.Context, command Command) ([]byte, error) {
	runner.command = command
	return runner.output, runner.err
}

func (*externalRunner) Run(context.Context, Command, io.Reader, io.Writer, io.Writer) error {
	return errors.New("unexpected Run")
}

func TestResolveExternalReturnsNoneForCompleteEmptyScan(t *testing.T) {
	runner := &externalRunner{output: []byte("none\n")}
	target, found, err := ResolveExternal(
		context.Background(),
		runner,
		session.Claude,
		"123e4567-e89b-42d3-a456-426614174000",
	)
	if err != nil || found || target != (ExternalTarget{}) {
		t.Fatalf("ResolveExternal() = (%#v, %t, %v), want zero, false, nil", target, found, err)
	}
	if runner.command.Name != "/bin/sh" ||
		!bytes.Equal([]byte(runner.command.Args[1]), []byte(ExternalResolverScript())) ||
		!bytes.Equal([]byte(runner.command.Args[2]), []byte("ars-external")) ||
		!bytes.Equal([]byte(runner.command.Args[3]), []byte("claude")) ||
		!bytes.Equal([]byte(runner.command.Args[4]), []byte("123e4567-e89b-42d3-a456-426614174000")) {
		t.Fatalf("resolver command = %#v", runner.command)
	}
}

func TestResolveExternalReturnsOneValidatedTarget(t *testing.T) {
	runner := &externalRunner{
		output: []byte("match\t/private/tmp/tmux-502/default\t%42\t1234\t1a\t2b\t502\tsocket\n"),
	}
	target, found, err := ResolveExternal(
		context.Background(),
		runner,
		session.Codex,
		"019fa13c-3a32-7922-b8c2-4b4adf8eadac",
	)
	want := ExternalTarget{
		Socket:      "/private/tmp/tmux-502/default",
		PaneID:      "%42",
		PanePID:     1234,
		SocketDev:   0x1a,
		SocketInode: 0x2b,
		SocketUID:   502,
	}
	if err != nil || !found || target != want {
		t.Fatalf("ResolveExternal() = (%#v, %t, %v), want %#v, true, nil", target, found, err, want)
	}
}

func TestResolveExternalRejectsInvalidInputsAndResults(t *testing.T) {
	validID := "123e4567-e89b-42d3-a456-426614174000"
	for _, test := range []struct {
		name   string
		runner Runner
		nameID session.Provider
		id     string
		output string
	}{
		{name: "nil runner", nameID: session.Claude, id: validID},
		{name: "unsupported provider", runner: &externalRunner{}, nameID: "other", id: validID},
		{name: "non canonical ID", runner: &externalRunner{}, nameID: session.Claude, id: "123E4567-e89b-42d3-a456-426614174000"},
		{name: "missing newline", runner: &externalRunner{output: []byte("none")}, nameID: session.Claude, id: validID},
		{name: "extra line", runner: &externalRunner{output: []byte("none\nnone\n")}, nameID: session.Claude, id: validID},
		{name: "extra fields", runner: &externalRunner{output: []byte("match\t/socket\t%1\t10\t1\t2\t3\tsocket\textra\n")}, nameID: session.Claude, id: validID},
		{name: "relative socket", runner: &externalRunner{output: []byte("match\tsocket\t%1\t10\t1\t2\t3\tsocket\n")}, nameID: session.Claude, id: validID},
		{name: "tab in socket", runner: &externalRunner{output: []byte("match\t/socket\tpart\t%1\t10\t1\t2\t3\tsocket\n")}, nameID: session.Claude, id: validID},
		{name: "NUL in socket", runner: &externalRunner{output: []byte("match\t/socket\x00part\t%1\t10\t1\t2\t3\tsocket\n")}, nameID: session.Claude, id: validID},
		{name: "invalid pane", runner: &externalRunner{output: []byte("match\t/socket\t$1:0.0\t10\t1\t2\t3\tsocket\n")}, nameID: session.Claude, id: validID},
		{name: "invalid pid", runner: &externalRunner{output: []byte("match\t/socket\t%1\t0\t1\t2\t3\tsocket\n")}, nameID: session.Claude, id: validID},
		{name: "invalid identity", runner: &externalRunner{output: []byte("match\t/socket\t%1\t10\tnot-hex\t2\t3\tsocket\n")}, nameID: session.Claude, id: validID},
		{name: "invalid type", runner: &externalRunner{output: []byte("match\t/socket\t%1\t10\t1\t2\t3\tother\n")}, nameID: session.Claude, id: validID},
		{name: "duplicate fields", runner: &externalRunner{output: []byte("match\t/socket\t%1\t10\t1\t2\t3\tsocket\nmatch\t/socket\t%1\t10\t1\t2\t3\tsocket\n")}, nameID: session.Claude, id: validID},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.output != "" {
				test.runner = &externalRunner{output: []byte(test.output)}
			}
			_, found, err := ResolveExternal(context.Background(), test.runner, test.nameID, test.id)
			if err == nil || found {
				t.Fatalf("ResolveExternal() = found %t, err %v, want validation error", found, err)
			}
		})
	}
}

func TestResolveExternalRejectsOversizedRunnerOutput(t *testing.T) {
	output := append([]byte("none\n"), bytes.Repeat([]byte("x"), maxInspectOutputBytes)...)
	_, found, err := ResolveExternal(context.Background(), &externalRunner{output: output}, session.Claude, "123e4567-e89b-42d3-a456-426614174000")
	if err == nil || found {
		t.Fatalf("ResolveExternal() = found %t, err %v, want output limit error", found, err)
	}
}

type externalFixture struct {
	processes            string
	argv                 map[int][]string
	cmdline              map[int][]byte
	panes                map[string]string
	failSocket           string
	symlinkDirectory     bool
	symlinkSocket        string
	badOwner             string
	sanitizePaneTab      bool
	unreadableDir        bool
	regularEntries       int
	fallbackEntries      int
	mutateSocket         string
	helperUnavailable    bool
	helperMalformed      bool
	assertNoHelperOrphan bool
	assertTmuxArgv       bool
	resolverTimeout      time.Duration
}

type externalSystemRunner struct{}

func (externalSystemRunner) Output(ctx context.Context, value Command) ([]byte, error) {
	command := systemCommand(ctx, value)
	var output boundedOutput
	var stderr bytes.Buffer
	command.Stdout = &output
	command.Stderr = &stderr
	err := command.Run()
	if output.exceeded || output.Len() > maxInspectOutputBytes {
		return nil, errInspectOutputLimit
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return output.Bytes(), nil
}

func (externalSystemRunner) Run(ctx context.Context, value Command, stdin io.Reader, stdout, stderr io.Writer) error {
	return SystemRunner{}.Run(ctx, value, stdin, stdout, stderr)
}

func TestExternalResolverScriptAcceptsZeroPaneID(t *testing.T) {
	const selected = "123e4567-e89b-42d3-a456-426614174000"
	target, found, err := runExternalFixture(t, externalFixture{
		processes: "100 1 zsh\n101 100 claude\n",
		argv:      map[int][]string{101: {"claude", "--resume", selected}},
		panes:     map[string]string{"default": "%0|100\n"},
	}, session.Claude, selected)
	if err != nil || !found || target.PaneID != "%0" {
		t.Fatalf("ResolveExternal() = (%#v, %t, %v), want pane %q, true, nil", target, found, err, "%0")
	}
}

func TestExternalResolverScriptUsesPrintablePaneSeparator(t *testing.T) {
	const selected = "123e4567-e89b-42d3-a456-426614174000"
	_, found, err := runExternalFixture(t, externalFixture{
		processes:       "100 1 zsh\n101 100 claude\n",
		argv:            map[int][]string{101: {"claude", "--resume", selected}},
		panes:           map[string]string{"default": "%1|100\n"},
		sanitizePaneTab: true,
	}, session.Claude, selected)
	if err != nil || !found {
		t.Fatalf("ResolveExternal() = found %t, err %v, want a printable tmux pane separator", found, err)
	}
}

func TestExternalResolverScriptUsesExactTmuxInspectionArgv(t *testing.T) {
	const selected = "123e4567-e89b-42d3-a456-426614174000"
	_, found, err := runExternalFixture(t, externalFixture{
		processes:      "100 1 zsh\n101 100 claude\n",
		argv:           map[int][]string{101: {"claude", "--resume", selected}},
		panes:          map[string]string{"default": "%1|100\n"},
		assertTmuxArgv: true,
	}, session.Claude, selected)
	if err != nil || !found {
		t.Fatalf("ResolveExternal() = found %t, err %v", found, err)
	}
	if strings.Contains(ExternalResolverScript(), "capture-pane") {
		t.Fatal("external resolver must not capture pane contents")
	}
}

func TestExternalResolverScriptMatchesHiddenSocket(t *testing.T) {
	const selected = "123e4567-e89b-42d3-a456-426614174000"
	target, found, err := runExternalFixture(t, externalFixture{
		processes: "100 1 zsh\n101 100 claude\n",
		argv:      map[int][]string{101: {"claude", "--resume", selected}},
		panes:     map[string]string{".hidden": "%1|100\n"},
	}, session.Claude, selected)
	if err != nil || !found || filepath.Base(target.Socket) != ".hidden" {
		t.Fatalf("ResolveExternal() = (%#v, %t, %v), want hidden socket match", target, found, err)
	}
}

func TestExternalResolverScriptFailsClosedOnSocketDirectoryTraversal(t *testing.T) {
	const selected = "123e4567-e89b-42d3-a456-426614174000"
	for _, test := range []struct {
		name    string
		fixture externalFixture
	}{
		{name: "global direct entry budget", fixture: externalFixture{
			processes: "100 1 claude\n", argv: map[int][]string{100: {"claude", "--resume", selected}},
			regularEntries: 2048, fallbackEntries: 2049,
		}},
		{name: "unreadable directory", fixture: externalFixture{
			processes: "100 1 claude\n", argv: map[int][]string{100: {"claude", "--resume", selected}},
			unreadableDir: true,
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, found, err := runExternalFixture(t, test.fixture, session.Claude, selected)
			if err == nil || found {
				t.Fatalf("ResolveExternal() = found %t, err %v, want fail-closed traversal error", found, err)
			}
		})
	}
}

func TestExternalResolverScriptBoundsRealDirectoryTraversalAtEntryLimit(t *testing.T) {
	const selected = "123e4567-e89b-42d3-a456-426614174000"
	_, found, err := runExternalFixture(t, externalFixture{
		processes:            "100 1 claude\n",
		argv:                 map[int][]string{100: {"claude", "--resume", selected}},
		regularEntries:       4097,
		assertNoHelperOrphan: true,
		resolverTimeout:      45 * time.Second,
	}, session.Claude, selected)
	if err == nil || found || !strings.Contains(err.Error(), "entries exceed limit") {
		t.Fatalf("ResolveExternal() = found %t, err %v, want bounded directory entry error", found, err)
	}
}

func TestExternalResolverScriptFailsClosedWhenInventoriedSocketIsReplacedAtSamePath(t *testing.T) {
	const selected = "123e4567-e89b-42d3-a456-426614174000"
	_, found, err := runExternalFixture(t, externalFixture{
		processes:    "100 1 claude\n",
		argv:         map[int][]string{100: {"claude", "--resume", selected}},
		panes:        map[string]string{"default": "%1|100\n"},
		mutateSocket: "default",
	}, session.Claude, selected)
	if err == nil || found || !strings.Contains(err.Error(), "invalid external tmux socket") {
		t.Fatalf("ResolveExternal() = found %t, err %v, want changed socket error", found, err)
	}
}

func TestExternalResolverScriptFailsClosedWhenDirectoryHelperIsUnavailable(t *testing.T) {
	const selected = "123e4567-e89b-42d3-a456-426614174000"
	_, found, err := runExternalFixture(t, externalFixture{
		processes:         "100 1 claude\n",
		argv:              map[int][]string{100: {"claude", "--resume", selected}},
		helperUnavailable: true,
	}, session.Claude, selected)
	if err == nil || found || !strings.Contains(err.Error(), "cannot inspect external tmux socket directory") {
		t.Fatalf("ResolveExternal() = found %t, err %v, want unavailable helper error", found, err)
	}
}

func TestExternalResolverScriptFailsClosedOnMalformedDirectoryHelperOutput(t *testing.T) {
	const selected = "123e4567-e89b-42d3-a456-426614174000"
	_, found, err := runExternalFixture(t, externalFixture{
		processes:       "100 1 claude\n",
		argv:            map[int][]string{100: {"claude", "--resume", selected}},
		helperMalformed: true,
	}, session.Claude, selected)
	if err == nil || found || !strings.Contains(err.Error(), "cannot inspect external tmux socket directory") {
		t.Fatalf("ResolveExternal() = found %t, err %v, want malformed helper error", found, err)
	}
}

func TestExternalResolverScriptUsesExactLinuxArgv(t *testing.T) {
	const selected = "123e4567-e89b-42d3-a456-426614174000"
	tests := []struct {
		name     string
		argv     []string
		cmdline  []byte
		wantFind bool
		wantErr  bool
	}{
		{name: "exact", argv: []string{"claude", "--resume", selected}, wantFind: true},
		{name: "absolute exact", argv: []string{"/Applications/Claude Code/claude", "--resume", selected}, wantFind: true},
		{name: "combined", argv: []string{"claude", "--resume " + selected}},
		{name: "extra", argv: []string{"claude", "--resume", selected, "--dangerously-skip-permissions"}},
		{name: "reordered", argv: []string{"claude", selected, "--resume"}},
		{name: "unavailable", wantErr: true},
		{name: "incomplete", cmdline: []byte("claude\x00--resume\x00" + selected), wantErr: true},
		{name: "oversized", cmdline: append(bytes.Repeat([]byte{'x'}, 65_536), 0), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := externalFixture{
				processes: "100 1 claude\n",
				panes:     map[string]string{"default": "%1|100\n"},
			}
			if test.argv != nil {
				fixture.argv = map[int][]string{100: test.argv}
			}
			if test.cmdline != nil {
				fixture.cmdline = map[int][]byte{100: test.cmdline}
			}
			_, found, err := runExternalFixture(t, fixture, session.Claude, selected)
			if test.wantErr {
				if err == nil || found {
					t.Fatalf("ResolveExternal() = found %t, err %v, want error", found, err)
				}
				return
			}
			if err != nil || found != test.wantFind {
				t.Fatalf("ResolveExternal() = found %t, err %v, want found %t", found, err, test.wantFind)
			}
		})
	}
}

func TestExternalResolverScriptUsesExactDarwinArgv(t *testing.T) {
	if goruntime.GOOS != "darwin" {
		t.Skip("KERN_PROCARGS2 is Darwin-specific")
	}
	compiler, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("cc is required for the live argv boundary fixture")
	}
	var helperDirectory string
	for letter := 'a'; letter <= 'z'; letter++ {
		candidate := filepath.Join("/tmp", string(letter))
		if err := os.Mkdir(candidate, 0o700); err == nil {
			helperDirectory = candidate
			break
		}
	}
	if helperDirectory == "" {
		t.Skip("no short temporary path is available for a claude process")
	}
	t.Cleanup(func() { _ = os.RemoveAll(helperDirectory) })
	source := filepath.Join(helperDirectory, "h.c")
	executable := filepath.Join(helperDirectory, "claude")
	if err := os.WriteFile(source, []byte("#include <unistd.h>\nint main(void) { for (;;) pause(); }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(compiler, "-o", executable, source).CombinedOutput(); err != nil {
		t.Skipf("cannot compile live argv fixture: %v: %s", err, output)
	}

	const selected = "123e4567-e89b-42d3-a456-426614174000"
	for _, test := range []struct {
		name        string
		arguments   []string
		wantFound   bool
		unavailable bool
	}{
		{name: "exact", arguments: []string{"--resume", selected}, wantFound: true},
		{name: "combined", arguments: []string{"--resume " + selected}},
		{name: "extra", arguments: []string{"--resume", selected, "extra"}},
		{name: "reordered", arguments: []string{selected, "--resume"}},
		{name: "unavailable", arguments: []string{"--resume", selected}, unavailable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			child := exec.Command(executable, test.arguments...)
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = child.Process.Kill()
				_ = child.Wait()
			})
			if test.unavailable {
				if err := child.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				if err := child.Wait(); err == nil {
					t.Fatal("killed child exited successfully")
				}
			}

			root, err := os.MkdirTemp("/tmp", "ars-darwin-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			bin := filepath.Join(root, "bin")
			fakeUID := strconv.Itoa(os.Getpid())
			tmuxDirectory := filepath.Join(root, "tmux-"+fakeUID)
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(tmuxDirectory, 0o700); err != nil {
				t.Fatal(err)
			}
			socket := filepath.Join(tmuxDirectory, "default")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			writeExternalExecutable(t, filepath.Join(bin, "id"), "#!/bin/sh\nprintf '%s\\n' \"$ARS_FAKE_UID\"\n")
			ps := "#!/bin/sh\nexec /bin/ps -p \"$ARS_CHILD_PID\" -o pid=,ppid=,comm=\n"
			if test.unavailable {
				ps = "#!/bin/sh\nprintf '%s 1 claude\\n' \"$ARS_CHILD_PID\"\n"
			}
			writeExternalExecutable(t, filepath.Join(bin, "ps"), ps)
			writeExternalExecutable(t, filepath.Join(bin, "ls"), "#!/bin/sh\n/bin/ls \"$@\" | awk -v uid=\"$ARS_FAKE_UID\" '{$3 = uid; print}'\n")
			writeExternalExecutable(t, filepath.Join(bin, "tmux"), "#!/bin/sh\nprintf '%%1|%s\\n' \"$ARS_CHILD_PID\"\n")
			t.Setenv("ARS_CHILD_PID", strconv.Itoa(child.Process.Pid))
			t.Setenv("ARS_FAKE_UID", fakeUID)
			t.Setenv("TMUX_TMPDIR", root)
			t.Setenv("TMPDIR", root)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

			target, found, err := ResolveExternal(context.Background(), externalSystemRunner{}, session.Claude, selected)
			assertNoExternalResolverWorkdirs(t, root)
			if test.unavailable {
				if err == nil || found {
					t.Fatalf("ResolveExternal() = (%#v, %t, %v), want fail-closed argv error", target, found, err)
				}
				return
			}
			if err != nil || found != test.wantFound {
				t.Fatalf("ResolveExternal() = (%#v, %t, %v), want found %t", target, found, err, test.wantFound)
			}
		})
	}
}

func TestDarwinArgvWatchdogTimesOutAndReapsOSAScript(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for the Darwin argv watchdog fixture")
	}
	root := t.TempDir()
	runner := filepath.Join(root, "runner.py")
	if err := os.WriteFile(runner, []byte(darwinArgvWatchdogScript), 0o600); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(root, "osascript.pid")
	osascript := filepath.Join(root, "osascript")
	writeExternalExecutable(t, osascript, "#!/bin/sh\nprintf '%s\\n' \"$$\" >\"$1\"\ntrap '' TERM\nwhile :; do :; done\n")
	candidates := filepath.Join(root, "candidates")
	if err := os.WriteFile(candidates, []byte("100\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	started := time.Now()
	command := exec.CommandContext(
		ctx,
		python,
		runner,
		"1",
		filepath.Join(root, "valid-candidates"),
		filepath.Join(root, "argv-error"),
		candidates,
		osascript,
		pidFile,
	)
	if err := command.Run(); err == nil {
		t.Fatal("Darwin argv watchdog succeeded for a hanging osascript")
	}
	if ctx.Err() != nil {
		t.Fatalf("Darwin argv watchdog exceeded test deadline: %v", ctx.Err())
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("Darwin argv watchdog took %v, want at most 5s", elapsed)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(pid)
	if err == nil && process.Signal(os.Signal(syscall.Signal(0))) == nil {
		t.Fatalf("timed-out osascript PID %d is still alive", pid)
	}
}

func TestExternalTmuxInspectorTimesOutAndReapsClientForStoppedServer(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for the external tmux watchdog fixture")
	}
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is required for the external tmux watchdog fixture")
	}
	root, err := os.MkdirTemp("/tmp", "ars-tmux-watchdog-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socket := filepath.Join(root, "socket")
	start := exec.Command(tmux, "-S", socket, "-f", "/dev/null", "new-session", "-d", "-s", "watchdog")
	if output, err := start.CombinedOutput(); err != nil {
		t.Fatalf("start disposable tmux: %v: %s", err, output)
	}
	serverOutput, err := exec.Command(tmux, "-S", socket, "-f", "/dev/null", "display-message", "-p", "#{pid}").Output()
	if err != nil {
		t.Fatal(err)
	}
	serverPID, err := strconv.Atoi(strings.TrimSpace(string(serverOutput)))
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	serverRunning := true
	t.Cleanup(func() {
		if stopped {
			_ = syscall.Kill(serverPID, syscall.SIGCONT)
		}
		if serverRunning {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = exec.CommandContext(ctx, tmux, "-S", socket, "-f", "/dev/null", "kill-server").Run()
		}
	})

	runner := filepath.Join(root, "tmux-inspector.py")
	if err := os.WriteFile(runner, []byte(externalTmuxPythonScript), 0o600); err != nil {
		t.Fatal(err)
	}
	clientPIDFile := filepath.Join(root, "client.pid")
	wrapper := filepath.Join(root, "tmux")
	writeExternalExecutable(t, wrapper, "#!/bin/sh\nprintf '%s\\n' \"$$\" >\"$ARS_TMUX_CLIENT_PID\"\nexec \"$ARS_REAL_TMUX\" \"$@\"\n")
	if err := syscall.Kill(serverPID, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	stopped = true

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, python, runner, "inspect", "1", socket, wrapper)
	command.Env = append(os.Environ(), "ARS_TMUX_CLIENT_PID="+clientPIDFile, "ARS_REAL_TMUX="+tmux)
	if err := command.Run(); err == nil {
		t.Fatal("external tmux inspector succeeded for a stopped server")
	}
	if ctx.Err() != nil {
		t.Fatalf("external tmux inspector exceeded test deadline: %v", ctx.Err())
	}
	data, err := os.ReadFile(clientPIDFile)
	if err != nil {
		t.Fatal(err)
	}
	clientPID, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(clientPID)
	if err == nil && process.Signal(os.Signal(syscall.Signal(0))) == nil {
		t.Fatalf("timed-out tmux client PID %d is still alive", clientPID)
	}
	if err := syscall.Kill(serverPID, syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	stopped = false
	if output, err := exec.Command(tmux, "-S", socket, "-f", "/dev/null", "kill-server").CombinedOutput(); err != nil {
		t.Fatalf("stop disposable tmux: %v: %s", err, output)
	}
	serverRunning = false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && syscall.Kill(serverPID, 0) == nil {
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(serverPID, 0); err == nil {
		t.Fatalf("disposable tmux server PID %d is still alive", serverPID)
	}
}

func TestExternalAttachRejectsReusedPaneIdentity(t *testing.T) {
	target, listener := externalSocketTarget(t)
	defer listener.Close()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	attached := filepath.Join(root, "attached")
	writeExternalExecutable(t, filepath.Join(bin, "tmux"), "#!/bin/sh\ncase \"$5\" in\nlist-panes) printf '%%2|200\\n' ;;\nattach-session) : >\"$ARS_ATTACHED\" ;;\nesac\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ARS_ATTACHED", attached)

	var stderr bytes.Buffer
	if err := (SystemRunner{}).Run(context.Background(), externalAttach(target), nil, io.Discard, &stderr); err == nil {
		t.Fatal("external attach accepted a replaced pane identity")
	}
	if _, err := os.Stat(attached); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("external attach reached tmux attach-session: %v", err)
	}
	if !strings.Contains(stderr.String(), "invalid external tmux target") {
		t.Fatalf("external attach error = %q", stderr.String())
	}
}

func TestExternalAttachRejectsSocketReplacedAfterResolve(t *testing.T) {
	target, listener := externalSocketTarget(t)
	path := target.Socket
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	called := filepath.Join(root, "tmux-called")
	writeExternalExecutable(t, filepath.Join(bin, "tmux"), "#!/bin/sh\n: >\"$ARS_TMUX_CALLED\"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ARS_TMUX_CALLED", called)

	var stderr bytes.Buffer
	if err := (SystemRunner{}).Run(context.Background(), externalAttach(target), nil, io.Discard, &stderr); err == nil {
		t.Fatal("external attach accepted a replaced socket")
	}
	if _, err := os.Stat(called); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("external attach invoked tmux for replaced socket: %v", err)
	}
	if !strings.Contains(stderr.String(), "invalid external tmux target") {
		t.Fatalf("external attach error = %q", stderr.String())
	}
}

func externalSocketTarget(t *testing.T) (ExternalTarget, net.Listener) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "ars-external-attach-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	path := filepath.Join(root, "socket")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	value, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		listener.Close()
		t.Fatal("Unix socket stat did not return syscall.Stat_t")
	}
	return ExternalTarget{
		Socket:      path,
		PaneID:      "%1",
		PanePID:     100,
		SocketDev:   uint64(value.Dev),
		SocketInode: uint64(value.Ino),
		SocketUID:   uint64(value.Uid),
	}, listener
}

func TestExternalResolverScript(t *testing.T) {
	const selected = "123e4567-e89b-42d3-a456-426614174000"
	tests := []struct {
		name       string
		provider   session.Provider
		fixture    externalFixture
		wantPane   string
		wantSocket string
		wantFound  bool
		wantError  string
	}{
		{
			name:     "exact Claude descendant",
			provider: session.Claude,
			fixture: externalFixture{
				processes: "100 1 zsh\n101 100 claude\n",
				argv:      map[int][]string{101: {"claude", "--resume", selected}},
				panes:     map[string]string{"default": "%1|100\n"},
			},
			wantPane: "%1", wantSocket: "default", wantFound: true,
		},
		{
			name:     "Claude absolute comm with spaces",
			provider: session.Claude,
			fixture: externalFixture{
				processes: "110 1 zsh\n111 110 /Applications/Claude Code/claude\n",
				argv:      map[int][]string{111: {"/Applications/Claude Code/claude", "--resume", selected}},
				panes:     map[string]string{"default": "%11|110\n"},
			},
			wantPane: "%11", wantSocket: "default", wantFound: true,
		},
		{
			name:     "exact Codex through wrapper",
			provider: session.Codex,
			fixture: externalFixture{
				processes: "200 1 zsh\n201 200 env\n202 201 codex\n",
				argv:      map[int][]string{202: {"codex", "resume", selected}},
				panes:     map[string]string{"default": "%2|200\n"},
			},
			wantPane: "%2", wantSocket: "default", wantFound: true,
		},
		{
			name: "bare provider", provider: session.Claude,
			fixture: externalFixture{
				processes: "300 1 claude\n", argv: map[int][]string{300: {"claude"}},
				panes: map[string]string{"default": "%3|300\n"},
			},
		},
		{
			name: "shell text is not provider process", provider: session.Claude,
			fixture: externalFixture{
				processes: "400 1 sh\n", argv: map[int][]string{400: {"sh", "-c", "echo claude --resume " + selected}},
				panes: map[string]string{"default": "%4|400\n"},
			},
		},
		{
			name: "multiple exact panes", provider: session.Claude,
			fixture: externalFixture{
				processes: "500 1 claude\n600 1 claude\n",
				argv:      map[int][]string{500: {"claude", "--resume", selected}, 600: {"claude", "--resume", selected}},
				panes:     map[string]string{"default": "%5|500\n", "other": "%6|600\n"},
			},
			wantError: "external tmux conflict",
		},
		{
			name: "eligible socket inspection failure", provider: session.Claude,
			fixture: externalFixture{
				processes: "650 1 claude\n", argv: map[int][]string{650: {"claude", "--resume", selected}},
				panes: map[string]string{"default": ""}, failSocket: "default",
			},
			wantError: "resolve external tmux",
		},
		{
			name: "rejects symlinked socket", provider: session.Claude,
			fixture: externalFixture{
				processes: "700 1 claude\n", argv: map[int][]string{700: {"claude", "--resume", selected}},
				panes: map[string]string{"default": "%7|700\n"}, symlinkSocket: "default",
			},
			wantError: "resolve external tmux",
		},
		{
			name: "rejects symlinked socket directory", provider: session.Claude,
			fixture: externalFixture{
				processes: "800 1 claude\n", argv: map[int][]string{800: {"claude", "--resume", selected}},
				panes: map[string]string{"default": "%8|800\n"}, symlinkDirectory: true,
			},
			wantError: "resolve external tmux",
		},
		{
			name: "rejects socket owned by another user", provider: session.Claude,
			fixture: externalFixture{
				processes: "900 1 claude\n", argv: map[int][]string{900: {"claude", "--resume", selected}},
				panes: map[string]string{"default": "%9|900\n"}, badOwner: "default",
			},
			wantError: "resolve external tmux",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target, found, err := runExternalFixture(t, test.fixture, test.provider, selected)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil || found != test.wantFound || target.PaneID != test.wantPane ||
				(found && filepath.Base(target.Socket) != test.wantSocket) {
				t.Fatalf("ResolveExternal() = (%#v, %t, %v), want socket %q pane %q found %t", target, found, err, test.wantSocket, test.wantPane, test.wantFound)
			}
		})
	}
}

func TestExternalResolverScriptRejectsBoundaries(t *testing.T) {
	const selected = "123e4567-e89b-42d3-a456-426614174000"
	tests := []struct {
		name    string
		fixture externalFixture
	}{
		{name: "65 sockets", fixture: externalFixture{processes: "1 0 claude\n", argv: map[int][]string{1: {"claude", "--resume", selected}}, panes: externalPanes(65, "%1|1\n")}},
		{name: "16385 panes", fixture: externalFixture{processes: "1 0 claude\n", argv: map[int][]string{1: {"claude", "--resume", selected}}, panes: map[string]string{"default": externalRows(16_385, "%", "|1\n")}}},
		{name: "16385 panes across sockets", fixture: externalFixture{panes: map[string]string{
			"first":  externalRows(8_192, "%", "|1\n"),
			"second": externalRows(8_193, "%", "|1\n"),
		}, processes: "1 0 claude\n", argv: map[int][]string{1: {"claude", "--resume", selected}}}},
		{name: "65537 processes", fixture: externalFixture{processes: externalProcesses(65_537)}},
		{name: "257 candidates", fixture: externalFixture{processes: externalCandidateProcesses(257)}},
		{name: "257 deep chain", fixture: externalFixture{processes: externalChain(258, selected), argv: map[int][]string{258: {"claude", "--resume", selected}}, panes: map[string]string{"default": "%1|1\n"}}},
		{name: "malformed process", fixture: externalFixture{processes: "bad row\n"}},
		{name: "malformed pane", fixture: externalFixture{processes: "1 0 claude\n", argv: map[int][]string{1: {"claude", "--resume", selected}}, panes: map[string]string{"default": "$1:0.0 bad\n"}}},
		{name: "oversized pane output", fixture: externalFixture{processes: "1 0 claude\n", argv: map[int][]string{1: {"claude", "--resume", selected}}, panes: map[string]string{"default": strings.Repeat("%1|1\n", maxInspectOutputBytes/5+1)}}},
		{name: "oversized process output", fixture: externalFixture{processes: strings.Repeat("1 0 sh\n", maxInspectOutputBytes/7+1)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, found, err := runExternalFixture(t, test.fixture, session.Claude, selected)
			if err == nil || found {
				t.Fatalf("ResolveExternal() = found %t, err %v, want bounded resolver error", found, err)
			}
		})
	}
}

func TestExternalResolverScriptBoundsCandidateAncestryWork(t *testing.T) {
	const selected = "123e4567-e89b-42d3-a456-426614174000"
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_, found, err := runExternalFixtureContext(t, ctx, externalFixture{
		processes: externalCandidateChains(17, 256),
		argv:      externalCandidateChainArgs(17, 256, selected),
		panes:     map[string]string{"default": "%1|1\n"},
	}, session.Claude, selected)
	if err == nil || found || !strings.Contains(err.Error(), "external tmux resolver work exceeds limit") {
		t.Fatalf("ResolveExternal() = found %t, err %v, want bounded work error", found, err)
	}
}

func runExternalFixture(t *testing.T, fixture externalFixture, name session.Provider, nativeID string) (ExternalTarget, bool, error) {
	return runExternalFixtureContext(t, context.Background(), fixture, name, nativeID)
}

func runExternalFixtureContext(t *testing.T, ctx context.Context, fixture externalFixture, name session.Provider, nativeID string) (ExternalTarget, bool, error) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "ars-external-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	bin := filepath.Join(root, "bin")
	panes := filepath.Join(root, "panes")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(panes, 0o755); err != nil {
		t.Fatal(err)
	}
	uid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(filepath.Join(root, "processes"), []byte(fixture.processes), 0o600); err != nil {
		t.Fatal(err)
	}
	argvDirectory := filepath.Join(root, "argv")
	if err := os.Mkdir(argvDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	for pid, values := range fixture.argv {
		var cmdline []byte
		for _, value := range values {
			cmdline = append(cmdline, value...)
			cmdline = append(cmdline, 0)
		}
		if err := os.WriteFile(filepath.Join(argvDirectory, strconv.Itoa(pid)), cmdline, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for pid, cmdline := range fixture.cmdline {
		if err := os.WriteFile(filepath.Join(argvDirectory, strconv.Itoa(pid)), cmdline, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tmuxDirectory := filepath.Join(root, "tmux-"+uid)
	if fixture.symlinkDirectory {
		tmuxDirectory = filepath.Join(root, "real-tmux-"+uid)
	}
	if err := os.MkdirAll(tmuxDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	for socket, rows := range fixture.panes {
		if err := os.WriteFile(filepath.Join(panes, socket), []byte(rows), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(tmuxDirectory, socket)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		listenPath := path
		if fixture.symlinkSocket == socket {
			listenPath = filepath.Join(root, "socket-"+socket)
		}
		listener, err := net.Listen("unix", listenPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		if fixture.symlinkSocket == socket {
			if err := os.Symlink(listenPath, path); err != nil {
				t.Fatal(err)
			}
		}
	}
	if fixture.symlinkDirectory {
		if err := os.Symlink(tmuxDirectory, filepath.Join(root, "tmux-"+uid)); err != nil {
			t.Fatal(err)
		}
	}
	for index := range fixture.regularEntries {
		path := filepath.Join(tmuxDirectory, fmt.Sprintf("regular-%04d", index))
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if fixture.fallbackEntries > 0 {
		fallbackDirectory := filepath.Join("/tmp", "tmux-"+uid)
		if err := os.Mkdir(fallbackDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(fallbackDirectory) })
		for index := range fixture.fallbackEntries {
			path := filepath.Join(fallbackDirectory, fmt.Sprintf("regular-%04d", index))
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if fixture.unreadableDir {
		if err := os.Chmod(tmuxDirectory, 0o100); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(tmuxDirectory, 0o700) })
	}
	writeExternalExecutable(t, filepath.Join(bin, "id"), "#!/bin/sh\necho "+uid+"\n")
	writeExternalExecutable(t, filepath.Join(bin, "ps"), "#!/bin/sh\ncat \"$ARS_EXTERNAL_ROOT/processes\"\n")
	writeExternalExecutable(t, filepath.Join(bin, "uname"), "#!/bin/sh\nprintf 'Linux\\n'\n")
	writeExternalExecutable(t, filepath.Join(bin, "head"), "#!/bin/sh\npath=$3\ncase \"$path\" in /proc/*/cmdline) pid=${path#/proc/}; pid=${pid%/cmdline}; exec /usr/bin/head -c \"$2\" \"$ARS_EXTERNAL_ROOT/argv/$pid\" ;; *) exec /usr/bin/head \"$@\" ;; esac\n")
	writeExternalExecutable(t, filepath.Join(bin, "ls"), "#!/bin/sh\nexit 97\n")
	writeExternalExecutable(t, filepath.Join(bin, "find"), "#!/bin/sh\nexit 97\n")
	realPython, pythonErr := exec.LookPath("python3")
	if pythonErr != nil && !fixture.helperUnavailable {
		t.Skip("python3 is required for the Linux directory-helper fixture")
	}
	transformer := `import os
import sys

fake_uid, bad_owner, mode = sys.argv[1:4]
directory = sys.argv[4] if len(sys.argv) > 4 else ""
paths = sys.argv[5] if len(sys.argv) > 5 else ""
for raw in sys.stdin:
    fields = raw.rstrip("\n").split("\t")
    path = b""
    if mode == "inventory" and len(fields) == 6:
        with open(os.path.join(paths, fields[0]), "rb") as value:
            path = value.read()
        if path == os.fsencode(bad_owner):
            fields[3] = "999"
    elif mode == "stat-entry" and len(fields) == 4:
        with open(directory, "rb") as value:
            path = value.read()
        if path == os.fsencode(bad_owner):
            fields[2] = "999"
    print("\t".join(fields))
`
	if err := os.WriteFile(filepath.Join(root, "transform-helper.py"), []byte(transformer), 0o600); err != nil {
		t.Fatal(err)
	}
	python := "#!/bin/sh\n"
	if fixture.helperUnavailable {
		python += "exit 127\n"
	} else if fixture.helperMalformed {
		python += "printf 'malformed\\n'\n"
	} else {
		python += "if [ \"${ARS_TRACK_HELPER:-}\" = 1 ]; then printf '%s\\n' \"$$\" >\"$ARS_EXTERNAL_ROOT/helper.pid\"; fi\n" +
			"if [ -n \"${ARS_MUTATE_SOCKET:-}\" ] && [ \"${2:-}\" = stat-entry ] && [ ! -e \"$ARS_EXTERNAL_ROOT/socket-mutated\" ]; then\n" +
			"  \"$ARS_REAL_PYTHON\" -c 'import os,socket,sys; p=sys.argv[1]; os.unlink(p); s=socket.socket(socket.AF_UNIX); s.bind(p); s.close()' \"$ARS_MUTATE_SOCKET\"\n" +
			"  : >\"$ARS_EXTERNAL_ROOT/socket-mutated\"\n" +
			"fi\n" +
			"output=\"$ARS_EXTERNAL_ROOT/helper-output.$$\"\n" +
			"trap 'rm -f -- \"$output\"' 0 HUP INT TERM\n" +
			"\"$ARS_REAL_PYTHON\" \"$@\" >\"$output\" || exit $?\n" +
			"\"$ARS_REAL_PYTHON\" \"$ARS_EXTERNAL_ROOT/transform-helper.py\" \"$ARS_FAKE_UID\" \"$ARS_BAD_OWNER\" \"${2:-}\" \"${3:-}\" \"${4:-}\" <\"$output\"\n"
	}
	writeExternalExecutable(t, filepath.Join(bin, "python3"), python)
	tmux := "#!/bin/sh\nsocket=$2\nname=$(basename \"$socket\")\nprintf '%s\\n' \"$@\" >>\"$ARS_EXTERNAL_ROOT/tmux-args\"\nif [ \"$name\" = \"$ARS_FAIL_SOCKET\" ]; then exit 1; fi\nif [ \"${ARS_SANITIZE_PANE_TAB:-}\" = 1 ]; then\n  previous=\n  format=\n  for value in \"$@\"; do\n    if [ \"$previous\" = -F ]; then format=$value; fi\n    previous=$value\n  done\n  case \"$format\" in *'|'*) printf '%%1|100\\n' ;; *) printf '%%1_100\\n' ;; esac\n  exit 0\nfi\ncat \"$ARS_EXTERNAL_PANES/$name\"\n"
	writeExternalExecutable(t, filepath.Join(bin, "tmux"), tmux)
	t.Setenv("ARS_EXTERNAL_ROOT", root)
	t.Setenv("ARS_EXTERNAL_PANES", panes)
	t.Setenv("ARS_FAIL_SOCKET", fixture.failSocket)
	if fixture.sanitizePaneTab {
		t.Setenv("ARS_SANITIZE_PANE_TAB", "1")
	}
	badOwner := filepath.Join(root, "no-bad-owner")
	if fixture.badOwner != "" {
		badOwner = filepath.Join(tmuxDirectory, fixture.badOwner)
	}
	t.Setenv("ARS_BAD_OWNER", badOwner)
	t.Setenv("ARS_FAKE_UID", uid)
	t.Setenv("ARS_REAL_PYTHON", realPython)
	if fixture.assertNoHelperOrphan {
		t.Setenv("ARS_TRACK_HELPER", "1")
	}
	t.Setenv("ARS_MUTATE_DIRECTORY", tmuxDirectory)
	mutateSocket := ""
	if fixture.mutateSocket != "" {
		mutateSocket = filepath.Join(tmuxDirectory, fixture.mutateSocket)
	}
	t.Setenv("ARS_MUTATE_SOCKET", mutateSocket)
	t.Setenv("TMUX_TMPDIR", root)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if fixture.resolverTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, fixture.resolverTimeout)
		defer cancel()
	}
	target, found, err := ResolveExternal(ctx, externalSystemRunner{}, name, nativeID)
	assertNoExternalResolverWorkdirs(t, root)
	if fixture.mutateSocket != "" {
		if _, statErr := os.Stat(filepath.Join(root, "socket-mutated")); statErr != nil {
			t.Fatalf("same-path socket replacement did not run: %v", statErr)
		}
	}
	if fixture.assertNoHelperOrphan {
		data, readErr := os.ReadFile(filepath.Join(root, "helper.pid"))
		if readErr != nil {
			t.Fatalf("read directory helper PID: %v", readErr)
		}
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if parseErr != nil {
			t.Fatalf("parse directory helper PID: %v", parseErr)
		}
		process, findErr := os.FindProcess(pid)
		if findErr == nil && process.Signal(os.Signal(syscall.Signal(0))) == nil {
			t.Fatalf("directory helper PID %d is still alive", pid)
		}
	}
	if fixture.assertTmuxArgv {
		got, readErr := os.ReadFile(filepath.Join(root, "tmux-args"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		socket := filepath.Join(tmuxDirectory, "default")
		want := strings.Join([]string{
			"-S", socket, "-f", "/dev/null", "list-panes", "-a", "-F",
			"#{pane_id}|#{pane_pid}",
		}, "\n") + "\n"
		if string(got) != want {
			t.Fatalf("tmux argv:\n%s\nwant:\n%s", got, want)
		}
	}
	return target, found, err
}

func assertNoExternalResolverWorkdirs(t *testing.T, root string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, "ars-external.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("external resolver left temporary workdirs: %v", matches)
	}
}

func writeExternalExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func externalPanes(count int, rows string) map[string]string {
	panes := make(map[string]string, count)
	for index := range count {
		panes["socket"+strconv.Itoa(index)] = rows
	}
	return panes
}

func externalRows(count int, prefix, suffix string) string {
	var rows strings.Builder
	for index := range count {
		rows.WriteString(prefix + strconv.Itoa(index) + suffix)
	}
	return rows.String()
}

func externalProcesses(count int) string {
	var rows strings.Builder
	for index := 1; index <= count; index++ {
		rows.WriteString(strconv.Itoa(index) + " 0 sh\n")
	}
	return rows.String()
}

func externalCandidateProcesses(count int) string {
	var rows strings.Builder
	for pid := 1; pid <= count; pid++ {
		rows.WriteString(strconv.Itoa(pid) + " 0 claude\n")
	}
	return rows.String()
}

func externalChain(depth int, selected string) string {
	var rows strings.Builder
	for pid := 1; pid <= depth; pid++ {
		parent := pid - 1
		name := "sh"
		if pid == depth {
			name = "claude"
		}
		rows.WriteString(strconv.Itoa(pid) + " " + strconv.Itoa(parent) + " " + name + "\n")
	}
	return rows.String()
}

func externalCandidateChains(count, depth int) string {
	var rows strings.Builder
	for chain := 0; chain < count; chain++ {
		start := chain*depth + 1
		for offset := 0; offset < depth; offset++ {
			pid := start + offset
			parent := 0
			if offset > 0 {
				parent = pid - 1
			}
			name := "sh"
			if offset == depth-1 {
				name = "claude"
			}
			rows.WriteString(strconv.Itoa(pid) + " " + strconv.Itoa(parent) + " " + name + "\n")
		}
	}
	return rows.String()
}

func externalCandidateChainArgs(count, depth int, selected string) map[int][]string {
	args := make(map[int][]string, count)
	for chain := 0; chain < count; chain++ {
		args[(chain+1)*depth] = []string{"claude", "--resume", selected}
	}
	return args
}
