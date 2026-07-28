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
		output: []byte("match\t/private/tmp/tmux-502/default\t$19:0.1\n"),
	}
	target, found, err := ResolveExternal(
		context.Background(),
		runner,
		session.Codex,
		"019fa13c-3a32-7922-b8c2-4b4adf8eadac",
	)
	want := ExternalTarget{
		Socket: "/private/tmp/tmux-502/default",
		Pane:   "$19:0.1",
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
		{name: "extra fields", runner: &externalRunner{output: []byte("match\t/socket\t$1:0.0\textra\n")}, nameID: session.Claude, id: validID},
		{name: "relative socket", runner: &externalRunner{output: []byte("match\tsocket\t$1:0.0\n")}, nameID: session.Claude, id: validID},
		{name: "tab in socket", runner: &externalRunner{output: []byte("match\t/socket\tpart\t$1:0.0\n")}, nameID: session.Claude, id: validID},
		{name: "NUL in socket", runner: &externalRunner{output: []byte("match\t/socket\x00part\t$1:0.0\n")}, nameID: session.Claude, id: validID},
		{name: "invalid pane", runner: &externalRunner{output: []byte("match\t/socket\t$:0.0\n")}, nameID: session.Claude, id: validID},
		{name: "duplicate fields", runner: &externalRunner{output: []byte("match\t/socket\t$1:0.0\nmatch\t/socket\t$1:0.0\n")}, nameID: session.Claude, id: validID},
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

func TestExternalResolverScriptAcceptsZeroSessionID(t *testing.T) {
	const selected = "123e4567-e89b-42d3-a456-426614174000"
	target, found, err := runExternalFixture(t, externalFixture{
		processes: "100 1 zsh\n101 100 claude\n",
		argv:      map[int][]string{101: {"claude", "--resume", selected}},
		panes:     map[string]string{"default": "$0:0.0|100\n"},
	}, session.Claude, selected)
	if err != nil || !found || target.Pane != "$0:0.0" {
		t.Fatalf("ResolveExternal() = (%#v, %t, %v), want pane %q, true, nil", target, found, err, "$0:0.0")
	}
}

func TestExternalResolverScriptUsesPrintablePaneSeparator(t *testing.T) {
	const selected = "123e4567-e89b-42d3-a456-426614174000"
	_, found, err := runExternalFixture(t, externalFixture{
		processes:       "100 1 zsh\n101 100 claude\n",
		argv:            map[int][]string{101: {"claude", "--resume", selected}},
		panes:           map[string]string{"default": "$1:0.0|100\n"},
		sanitizePaneTab: true,
	}, session.Claude, selected)
	if err != nil || !found {
		t.Fatalf("ResolveExternal() = found %t, err %v, want a printable tmux pane separator", found, err)
	}
	if !strings.Contains(ExternalResolverScript(), "#{session_id}:#{window_index}.#{pane_index}|#{pane_pid}") {
		t.Fatal("external resolver does not request pipe-delimited pane rows")
	}
}

func TestExternalResolverScriptUsesExactTmuxInspectionArgv(t *testing.T) {
	const selected = "123e4567-e89b-42d3-a456-426614174000"
	_, found, err := runExternalFixture(t, externalFixture{
		processes:      "100 1 zsh\n101 100 claude\n",
		argv:           map[int][]string{101: {"claude", "--resume", selected}},
		panes:          map[string]string{"default": "$1:0.0|100\n"},
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
		panes:     map[string]string{".hidden": "$1:0.0|100\n"},
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
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, found, err := runExternalFixtureContext(t, ctx, externalFixture{
		processes:            "100 1 claude\n",
		argv:                 map[int][]string{100: {"claude", "--resume", selected}},
		regularEntries:       4097,
		assertNoHelperOrphan: true,
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
		panes:        map[string]string{"default": "$1:0.0|100\n"},
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
				panes:     map[string]string{"default": "$1:0.0|100\n"},
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
			writeExternalExecutable(t, filepath.Join(bin, "tmux"), "#!/bin/sh\nprintf '$1:0.0|%s\\n' \"$ARS_CHILD_PID\"\n")
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
				panes:     map[string]string{"default": "$1:0.0|100\n"},
			},
			wantPane: "$1:0.0", wantSocket: "default", wantFound: true,
		},
		{
			name:     "Claude absolute comm with spaces",
			provider: session.Claude,
			fixture: externalFixture{
				processes: "110 1 zsh\n111 110 /Applications/Claude Code/claude\n",
				argv:      map[int][]string{111: {"/Applications/Claude Code/claude", "--resume", selected}},
				panes:     map[string]string{"default": "$11:0.0|110\n"},
			},
			wantPane: "$11:0.0", wantSocket: "default", wantFound: true,
		},
		{
			name:     "exact Codex through wrapper",
			provider: session.Codex,
			fixture: externalFixture{
				processes: "200 1 zsh\n201 200 env\n202 201 codex\n",
				argv:      map[int][]string{202: {"codex", "resume", selected}},
				panes:     map[string]string{"default": "$2:1.0|200\n"},
			},
			wantPane: "$2:1.0", wantSocket: "default", wantFound: true,
		},
		{
			name: "bare provider", provider: session.Claude,
			fixture: externalFixture{
				processes: "300 1 claude\n", argv: map[int][]string{300: {"claude"}},
				panes: map[string]string{"default": "$3:0.0|300\n"},
			},
		},
		{
			name: "shell text is not provider process", provider: session.Claude,
			fixture: externalFixture{
				processes: "400 1 sh\n", argv: map[int][]string{400: {"sh", "-c", "echo claude --resume " + selected}},
				panes: map[string]string{"default": "$4:0.0|400\n"},
			},
		},
		{
			name: "multiple exact panes", provider: session.Claude,
			fixture: externalFixture{
				processes: "500 1 claude\n600 1 claude\n",
				argv:      map[int][]string{500: {"claude", "--resume", selected}, 600: {"claude", "--resume", selected}},
				panes:     map[string]string{"default": "$5:0.0|500\n", "other": "$6:0.0|600\n"},
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
				panes: map[string]string{"default": "$7:0.0|700\n"}, symlinkSocket: "default",
			},
			wantError: "resolve external tmux",
		},
		{
			name: "rejects symlinked socket directory", provider: session.Claude,
			fixture: externalFixture{
				processes: "800 1 claude\n", argv: map[int][]string{800: {"claude", "--resume", selected}},
				panes: map[string]string{"default": "$8:0.0|800\n"}, symlinkDirectory: true,
			},
			wantError: "resolve external tmux",
		},
		{
			name: "rejects socket owned by another user", provider: session.Claude,
			fixture: externalFixture{
				processes: "900 1 claude\n", argv: map[int][]string{900: {"claude", "--resume", selected}},
				panes: map[string]string{"default": "$9:0.0|900\n"}, badOwner: "default",
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
			if err != nil || found != test.wantFound || target.Pane != test.wantPane ||
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
		{name: "65 sockets", fixture: externalFixture{processes: "1 0 claude\n", argv: map[int][]string{1: {"claude", "--resume", selected}}, panes: externalPanes(65, "$1:0.0|1\n")}},
		{name: "16385 panes", fixture: externalFixture{processes: "1 0 claude\n", argv: map[int][]string{1: {"claude", "--resume", selected}}, panes: map[string]string{"default": externalRows(16_385, "$1:0.", "|1\n")}}},
		{name: "16385 panes across sockets", fixture: externalFixture{panes: map[string]string{
			"first":  externalRows(8_192, "$1:0.", "|1\n"),
			"second": externalRows(8_193, "$1:0.", "|1\n"),
		}, processes: "1 0 claude\n", argv: map[int][]string{1: {"claude", "--resume", selected}}}},
		{name: "65537 processes", fixture: externalFixture{processes: externalProcesses(65_537)}},
		{name: "257 candidates", fixture: externalFixture{processes: externalCandidateProcesses(257)}},
		{name: "257 deep chain", fixture: externalFixture{processes: externalChain(258, selected), argv: map[int][]string{258: {"claude", "--resume", selected}}, panes: map[string]string{"default": "$1:0.0|1\n"}}},
		{name: "malformed process", fixture: externalFixture{processes: "bad row\n"}},
		{name: "malformed pane", fixture: externalFixture{processes: "1 0 claude\n", argv: map[int][]string{1: {"claude", "--resume", selected}}, panes: map[string]string{"default": "$1:0.0 bad\n"}}},
		{name: "oversized pane output", fixture: externalFixture{processes: "1 0 claude\n", argv: map[int][]string{1: {"claude", "--resume", selected}}, panes: map[string]string{"default": strings.Repeat("$1:0.0|1\n", maxInspectOutputBytes/9+1)}}},
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
		panes:     map[string]string{"default": "$1:0.0|1\n"},
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
	tmux := "#!/bin/sh\nsocket=$2\nname=$(basename \"$socket\")\nprintf '%s\\n' \"$@\" >>\"$ARS_EXTERNAL_ROOT/tmux-args\"\nif [ \"$name\" = \"$ARS_FAIL_SOCKET\" ]; then exit 1; fi\nif [ \"${ARS_SANITIZE_PANE_TAB:-}\" = 1 ]; then\n  previous=\n  format=\n  for value in \"$@\"; do\n    if [ \"$previous\" = -F ]; then format=$value; fi\n    previous=$value\n  done\n  case \"$format\" in *'|'*) printf '$1:0.0|100\\n' ;; *) printf '$1:0.0_100\\n' ;; esac\n  exit 0\nfi\ncat \"$ARS_EXTERNAL_PANES/$name\"\n"
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
			"#{session_id}:#{window_index}.#{pane_index}|#{pane_pid}",
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
