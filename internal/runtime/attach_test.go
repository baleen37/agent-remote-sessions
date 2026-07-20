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
	runner := &attachRunner{hasErrors: []error{attachExitError{code: 1}}}
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
		tmuxCommand("new-session", "-d", "-s", key, "-c", item.CWD, "claude", "--resume", item.NativeID),
		tmuxCommand("bind-key", "-n", "C-q", "detach-client"),
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
	if want := []string{"has-session", "bind-key", "attach-session"}; !slices.Equal(runner.commandNames(), want) {
		t.Fatalf("commands = %v, want %v", runner.commandNames(), want)
	}
}

func TestAttachCommandRechecksAfterConcurrentCreate(t *testing.T) {
	createErr := attachExitError{code: 1}
	runner := &attachRunner{
		hasErrors: []error{attachExitError{code: 1}, nil},
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
	want := []string{"has-session", "new-session", "has-session", "bind-key", "attach-session"}
	if !slices.Equal(runner.commandNames(), want) {
		t.Fatalf("commands = %v, want %v", runner.commandNames(), want)
	}
}

func TestAttachCommandReturnsCreateErrorWhenRaceCheckFails(t *testing.T) {
	createErr := attachExitError{code: 23}
	runner := &attachRunner{
		hasErrors: []error{attachExitError{code: 1}, attachExitError{code: 1}},
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
		Args: append([]string{"-L", SocketName, "-f", "/dev/null"}, args...),
		Env:  []string{"TMUX=", "TMUX_PANE=", "TMUX_TMPDIR=/tmp"},
	}
}

type attachCall struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

type attachRunner struct {
	commands   []Command
	calls      []attachCall
	hasErrors  []error
	nameErrors map[string][]error
}

func (*attachRunner) Output(context.Context, Command) ([]byte, error) {
	return nil, errors.New("unexpected Output call")
}

func (runner *attachRunner) Run(_ context.Context, command Command, stdin io.Reader, stdout, stderr io.Writer) error {
	runner.commands = append(runner.commands, command)
	runner.calls = append(runner.calls, attachCall{stdin: stdin, stdout: stdout, stderr: stderr})
	name := command.Args[4]
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
		names = append(names, command.Args[4])
	}
	return names
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
