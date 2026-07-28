package runtime

import (
	"context"
	"errors"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func TestAttachCommandCreatesBindsAndAttachesOnce(t *testing.T) {
	runner := &attachRunner{
		hasErrors: []error{attachExitError{code: 1}},
		outputs:   [][]byte{[]byte("none\n")},
	}
	item := attachedSession()
	command, err := NewAttachCommand(context.Background(), runner, item, claudeSpec())
	if err != nil {
		t.Fatal(err)
	}
	stdin := strings.NewReader("input")
	command.SetStdin(stdin)
	command.SetStdout(io.Discard)
	command.SetStderr(io.Discard)

	if err := command.Run(); err != nil {
		t.Fatal(err)
	}

	key := Key(string(item.Provider), item.NativeID)
	want := []Command{
		tmuxCommand("has-session", "-t", "="+key),
		externalResolveCommand(),
		tmuxCommand("new-session", "-d", "-s", key, "-c", item.CWD, "claude", "--resume", item.NativeID),
		tmuxCommand("bind-key", "-n", "C-q", "detach-client"),
		tmuxCommand("set-option", "-g", "status-right", DetachHint()),
		tmuxCommand("set-option", "-g", "status-interval", "5"),
		tmuxCommand("attach-session", "-d", "-t", "="+key),
	}
	if !reflect.DeepEqual(runner.commands, want) {
		t.Fatalf("commands = %#v, want %#v", runner.commands, want)
	}
	if runner.calls[len(runner.calls)-1].stdin != stdin ||
		runner.calls[len(runner.calls)-1].stdout != io.Discard ||
		runner.calls[len(runner.calls)-1].stderr != io.Discard {
		t.Fatal("attach did not receive configured standard streams")
	}
}

func TestAttachCommandShowsDetachHintOnStatusLine(t *testing.T) {
	runner := &attachRunner{hasErrors: []error{nil}}
	command, err := NewAttachCommand(context.Background(), runner, attachedSession(), claudeSpec())
	if err != nil {
		t.Fatal(err)
	}
	command.SetStdin(strings.NewReader(""))
	command.SetStdout(io.Discard)
	command.SetStderr(io.Discard)
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}

	var hint Command
	for _, c := range runner.commands {
		if c.Args[4] == "set-option" && c.Args[6] == "status-right" {
			hint = c
		}
	}
	if hint.Name == "" {
		t.Fatalf("attach did not set a status option: %v", runner.commandNames())
	}
	if want := []string{"set-option", "-g", "status-right", DetachHint()}; !slices.Equal(hint.Args[4:], want) {
		t.Fatalf("status option = %v, want %v", hint.Args[4:], want)
	}
	if !strings.Contains(DetachHint(), "ctrl-q") || !strings.Contains(DetachHint(), "detach") {
		t.Fatalf("detach hint does not name the key: %q", DetachHint())
	}
	if !strings.Contains(DetachHint(), "tmux -L "+SocketName()) {
		t.Fatalf("detach hint does not count the ars socket's own sessions: %q", DetachHint())
	}
	// The hint must be set before attach so it is visible from the first frame.
	names := runner.commandNames()
	if slices.Index(names, "set-option") > slices.Index(names, "attach-session") {
		t.Fatalf("status hint set after attach: %v", names)
	}
}

func TestAttachCommandSetsStatusIntervalBeforeAttach(t *testing.T) {
	runner := &attachRunner{hasErrors: []error{nil}}
	command, err := NewAttachCommand(context.Background(), runner, attachedSession(), claudeSpec())
	if err != nil {
		t.Fatal(err)
	}
	command.SetStdin(strings.NewReader(""))
	command.SetStdout(io.Discard)
	command.SetStderr(io.Discard)
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}

	want := tmuxCommand("set-option", "-g", "status-interval", "5")
	names := runner.commandNames()
	intervalIndex := -1
	for index, command := range runner.commands {
		if reflect.DeepEqual(command, want) {
			intervalIndex = index
		}
	}
	if intervalIndex == -1 {
		t.Fatalf("commands = %#v, want to contain %#v", runner.commands, want)
	}
	if intervalIndex > slices.Index(names, "attach-session") {
		t.Fatalf("status-interval not set before attach: %v", names)
	}
}

func TestAttachCommandDoesNotRestartExistingRuntime(t *testing.T) {
	runner := &attachRunner{hasErrors: []error{nil}}
	command, err := NewAttachCommand(context.Background(), runner, attachedSession(), claudeSpec())
	if err != nil {
		t.Fatal(err)
	}
	command.SetStdin(strings.NewReader(""))
	command.SetStdout(io.Discard)
	command.SetStderr(io.Discard)

	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(runner.commandNames(), "new-session") {
		t.Fatal("runtime restarted")
	}
	if want := []string{"has-session", "bind-key", "set-option", "set-option", "attach-session"}; !slices.Equal(runner.commandNames(), want) {
		t.Fatalf("commands = %v, want %v", runner.commandNames(), want)
	}
}

func TestAttachCommandReusesUniqueExternalPane(t *testing.T) {
	runner := &attachRunner{
		hasErrors: []error{attachExitError{code: 1}},
		outputs: [][]byte{
			[]byte("match\t/private/tmp/tmux-502/default\t$19:0.1\n"),
		},
	}
	command, err := NewAttachCommand(context.Background(), runner, attachedSession(), claudeSpec())
	if err != nil {
		t.Fatal(err)
	}
	stdin := strings.NewReader("input")
	command.SetStdin(stdin)
	command.SetStdout(io.Discard)
	command.SetStderr(io.Discard)

	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(runner.commandNames(), "new-session") ||
		slices.Contains(runner.commandNames(), "bind-key") ||
		slices.Contains(runner.commandNames(), "set-option") {
		t.Fatalf("external attach mutated ARS server: %v", runner.commandNames())
	}
	want := Command{
		Name: "tmux",
		Args: []string{
			"-S", "/private/tmp/tmux-502/default", "-f", "/dev/null",
			"attach-session", "-t", "$19:0.1",
		},
		Env: []string{"TMUX=", "TMUX_PANE="},
	}
	got := runner.commands[len(runner.commands)-1]
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("external attach = %#v, want %#v", got, want)
	}
	if runner.calls[len(runner.calls)-1].stdin != stdin ||
		runner.calls[len(runner.calls)-1].stdout != io.Discard ||
		runner.calls[len(runner.calls)-1].stderr != io.Discard {
		t.Fatal("external attach lost terminal streams")
	}
	for _, command := range runner.commands {
		if command.Name != "tmux" || len(command.Args) < 5 || command.Args[0] != "-S" {
			continue
		}
		if slices.Contains(command.Args, "-d") || slices.Contains(command.Args, "C-q") {
			t.Fatalf("external attach forced detach or changed Ctrl+Q: %#v", command)
		}
	}
}

func TestAttachCommandCreatesARSRuntimeAfterCompleteExternalMiss(t *testing.T) {
	runAttachExternalCase(t, attachExternalCase{
		runner: &attachRunner{
			hasErrors: []error{attachExitError{code: 1}},
			outputs:   [][]byte{[]byte("none\n")},
		},
		wantOperations: []string{"has-session", "resolve-external", "new-session", "bind-key", "set-option", "set-option", "attach-session"},
	})
}

func TestAttachCommandKeepsExistingARSRuntimeAheadOfExternalProbe(t *testing.T) {
	runAttachExternalCase(t, attachExternalCase{
		runner:         &attachRunner{hasErrors: []error{nil}},
		wantOperations: []string{"has-session", "bind-key", "set-option", "set-option", "attach-session"},
	})
}

func TestAttachCommandDoesNotCreateAfterExternalResolverFailure(t *testing.T) {
	resolverErr := errors.New("resolver failed")
	runAttachExternalCase(t, attachExternalCase{
		runner: &attachRunner{
			hasErrors:    []error{attachExitError{code: 1}},
			outputErrors: []error{resolverErr},
		},
		wantOperations: []string{"has-session", "resolve-external"},
		wantErr:        resolverErr,
	})
}

func TestAttachCommandDoesNotFallbackAfterExternalAttachFailure(t *testing.T) {
	attachErr := errors.New("external attach failed")
	runAttachExternalCase(t, attachExternalCase{
		runner: &attachRunner{
			hasErrors: []error{attachExitError{code: 1}},
			outputs: [][]byte{
				[]byte("match\t/private/tmp/tmux-502/default\t$19:0.1\n"),
			},
			nameErrors: map[string][]error{"attach-session": {attachErr}},
		},
		wantOperations: []string{"has-session", "resolve-external", "attach-session"},
		wantErr:        attachErr,
	})
}

type attachExternalCase struct {
	runner         *attachRunner
	wantOperations []string
	wantErr        error
}

func runAttachExternalCase(t *testing.T, test attachExternalCase) {
	t.Helper()
	command, err := NewAttachCommand(context.Background(), test.runner, attachedSession(), claudeSpec())
	if err != nil {
		t.Fatal(err)
	}
	command.SetStdin(strings.NewReader(""))
	command.SetStdout(io.Discard)
	command.SetStderr(io.Discard)

	err = command.Run()
	if test.wantErr == nil && err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if test.wantErr != nil && !errors.Is(err, test.wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, test.wantErr)
	}
	if got := test.runner.commandNames(); !slices.Equal(got, test.wantOperations) {
		t.Fatalf("operations = %v, want %v", got, test.wantOperations)
	}
}

func TestAttachCommandRechecksAfterConcurrentCreate(t *testing.T) {
	createErr := attachExitError{code: 1}
	runner := &attachRunner{
		hasErrors: []error{attachExitError{code: 1}, nil},
		outputs:   [][]byte{[]byte("none\n")},
		nameErrors: map[string][]error{
			"new-session": {createErr},
		},
	}
	command, err := NewAttachCommand(context.Background(), runner, attachedSession(), claudeSpec())
	if err != nil {
		t.Fatal(err)
	}
	command.SetStdin(strings.NewReader(""))
	command.SetStdout(io.Discard)
	command.SetStderr(io.Discard)

	if err := command.Run(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{"has-session", "resolve-external", "new-session", "has-session", "bind-key", "set-option", "set-option", "attach-session"}
	if !slices.Equal(runner.commandNames(), want) {
		t.Fatalf("commands = %v, want %v", runner.commandNames(), want)
	}
}

func TestAttachCommandReturnsCreateErrorWhenRaceCheckFails(t *testing.T) {
	createErr := attachExitError{code: 23}
	runner := &attachRunner{
		hasErrors: []error{attachExitError{code: 1}, attachExitError{code: 1}},
		outputs:   [][]byte{[]byte("none\n")},
		nameErrors: map[string][]error{
			"new-session": {createErr},
		},
	}
	command, err := NewAttachCommand(context.Background(), runner, attachedSession(), claudeSpec())
	if err != nil {
		t.Fatal(err)
	}

	err = command.Run()
	if !errors.Is(err, createErr) || err.Error() != "create runtime: tmux exited 23" {
		t.Fatalf("Run() error = %v", err)
	}
	if slices.Contains(runner.commandNames(), "bind-key") || slices.Contains(runner.commandNames(), "attach-session") {
		t.Fatalf("continued after failed creation: %v", runner.commandNames())
	}
}

func TestAttachCommandPreservesFinalAttachExitError(t *testing.T) {
	attachErr := attachExitError{code: 42}
	runner := &attachRunner{
		hasErrors: []error{nil},
		nameErrors: map[string][]error{
			"attach-session": {attachErr},
		},
	}
	command, err := NewAttachCommand(context.Background(), runner, attachedSession(), claudeSpec())
	if err != nil {
		t.Fatal(err)
	}

	err = command.Run()
	if !errors.Is(err, attachErr) {
		t.Fatalf("Run() error = %v, want original attach error", err)
	}
	exitError, ok := err.(interface{ ExitCode() int })
	if !ok || exitError.ExitCode() != 42 {
		t.Fatalf("Run() exit error = (%v, %v), want code 42", exitError, ok)
	}
}

func TestNewAttachCommandRejectsInvalidInputBeforeTmux(t *testing.T) {
	valid := attachedSession()
	tests := []struct {
		name   string
		runner Runner
		item   session.Session
		spec   provider.ResumeSpec
	}{
		{name: "nil runner", item: valid, spec: claudeSpec()},
		{name: "invalid session", runner: &attachRunner{}, item: withAttachCWD(valid, "relative"), spec: claudeSpec()},
		{name: "invalid runtime", runner: &attachRunner{}, item: withAttachRuntime(valid, session.Runtime{}), spec: claudeSpec()},
		{name: "invalid resume spec", runner: &attachRunner{}, item: valid, spec: provider.ResumeSpec{Executable: "sh", Args: []string{"-c", "evil"}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, err := NewAttachCommand(context.Background(), test.runner, test.item, test.spec)
			if err == nil || command != nil {
				t.Fatalf("NewAttachCommand() = (%#v, %v), want nil command and error", command, err)
			}
			if runner, ok := test.runner.(*attachRunner); ok && len(runner.commands) != 0 {
				t.Fatalf("invalid input invoked tmux: %#v", runner.commands)
			}
		})
	}
}

func tmuxCommand(args ...string) Command {
	return Command{
		Name: "tmux",
		Args: append([]string{"-L", SocketName(), "-f", "/dev/null"}, args...),
		Env:  []string{"TMUX=", "TMUX_PANE=", "TMUX_TMPDIR=/tmp"},
	}
}

func externalResolveCommand() Command {
	return Command{
		Name: "/bin/sh",
		Args: []string{"-c", ExternalResolverScript(), "ars-external", "claude", attachedSession().NativeID},
		Env:  []string{"TMUX=", "TMUX_PANE="},
	}
}

type attachCall struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

type attachRunner struct {
	commands     []Command
	calls        []attachCall
	hasErrors    []error
	nameErrors   map[string][]error
	outputs      [][]byte
	outputErrors []error
}

func (runner *attachRunner) Output(_ context.Context, command Command) ([]byte, error) {
	runner.commands = append(runner.commands, command)
	if len(runner.outputs) == 0 && len(runner.outputErrors) == 0 {
		return nil, errors.New("unexpected Output call")
	}
	var output []byte
	if len(runner.outputs) > 0 {
		output = runner.outputs[0]
		runner.outputs = runner.outputs[1:]
	}
	var err error
	if len(runner.outputErrors) > 0 {
		err = runner.outputErrors[0]
		runner.outputErrors = runner.outputErrors[1:]
	}
	return output, err
}

func (runner *attachRunner) Run(_ context.Context, command Command, stdin io.Reader, stdout, stderr io.Writer) error {
	runner.commands = append(runner.commands, command)
	runner.calls = append(runner.calls, attachCall{stdin: stdin, stdout: stdout, stderr: stderr})
	name := commandOperation(command)
	if name == "has-session" && len(runner.hasErrors) > 0 {
		err := runner.hasErrors[0]
		runner.hasErrors = runner.hasErrors[1:]
		return err
	}
	if errors := runner.nameErrors[name]; len(errors) > 0 {
		err := errors[0]
		runner.nameErrors[name] = errors[1:]
		return err
	}
	return nil
}

func (runner *attachRunner) commandNames() []string {
	names := make([]string, 0, len(runner.commands))
	for _, command := range runner.commands {
		names = append(names, commandOperation(command))
	}
	return names
}

func commandOperation(command Command) string {
	if command.Name == "/bin/sh" {
		return "resolve-external"
	}
	if command.Name == "tmux" && len(command.Args) > 4 {
		return command.Args[4]
	}
	return command.Name
}

type attachExitError struct{ code int }

func (err attachExitError) Error() string { return "tmux exited " + strconv.Itoa(err.code) }
func (err attachExitError) ExitCode() int { return err.code }

func attachedSession() session.Session {
	return session.Session{Host: "local", Candidate: session.Candidate{
		Provider:  session.Claude,
		NativeID:  "123e4567-e89b-42d3-a456-426614174000",
		UpdatedAt: time.Unix(1, 0),
		CWD:       "/work/it's app",
		Title:     "title",
	}, Runtime: session.Runtime{State: session.RuntimeSaved}}
}

func claudeSpec() provider.ResumeSpec {
	return provider.ResumeSpec{Executable: "claude", Args: []string{"--resume", attachedSession().NativeID}}
}

func withAttachCWD(item session.Session, cwd string) session.Session {
	item.CWD = cwd
	return item
}

func withAttachRuntime(item session.Session, value session.Runtime) session.Session {
	item.Runtime = value
	return item
}
