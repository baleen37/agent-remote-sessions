package runtime

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
)

func TestCapturePaneCapturesTargetPane(t *testing.T) {
	runner := &fakeRunner{output: []byte("line one\nline two\n")}
	output, err := CapturePane(context.Background(), runner, "claude", "123e4567-e89b-42d3-a456-426614174000")
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "line one\nline two\n" {
		t.Fatalf("CapturePane() = %q", output)
	}
	name := Key("claude", "123e4567-e89b-42d3-a456-426614174000")
	want := Command{
		Name: "tmux",
		Args: []string{"-L", SocketName, "-f", "/dev/null", "capture-pane", "-p", "-t", "=" + name + ":"},
		Env:  []string{"TMUX=", "TMUX_PANE=", "TMUX_TMPDIR=/tmp"},
	}
	if !reflect.DeepEqual(runner.command, want) {
		t.Fatalf("command = %#v, want %#v", runner.command, want)
	}
}

func TestCapturePaneRequiresRunner(t *testing.T) {
	if _, err := CapturePane(context.Background(), nil, "claude", "id"); err == nil {
		t.Fatal("CapturePane() with nil runner did not error")
	}
}

func TestCapturePanePropagatesError(t *testing.T) {
	runner := &fakeRunner{err: errors.New("no server")}
	if _, err := CapturePane(context.Background(), runner, "claude", "id"); err == nil {
		t.Fatal("CapturePane() did not propagate runner error")
	}
}

func TestKillSessionKillsTargetSession(t *testing.T) {
	runner := &fakeRunRunner{}
	err := KillSession(context.Background(), runner, "claude", "123e4567-e89b-42d3-a456-426614174000")
	if err != nil {
		t.Fatal(err)
	}
	name := Key("claude", "123e4567-e89b-42d3-a456-426614174000")
	want := Command{
		Name: "tmux",
		Args: []string{"-L", SocketName, "-f", "/dev/null", "kill-session", "-t", "=" + name},
		Env:  []string{"TMUX=", "TMUX_PANE=", "TMUX_TMPDIR=/tmp"},
	}
	if !reflect.DeepEqual(runner.command, want) {
		t.Fatalf("command = %#v, want %#v", runner.command, want)
	}
}

func TestKillSessionRequiresRunner(t *testing.T) {
	if err := KillSession(context.Background(), nil, "claude", "id"); err == nil {
		t.Fatal("KillSession() with nil runner did not error")
	}
}

func TestKillSessionPropagatesError(t *testing.T) {
	runner := &fakeRunRunner{err: errors.New("no server")}
	if err := KillSession(context.Background(), runner, "claude", "id"); err == nil {
		t.Fatal("KillSession() did not propagate runner error")
	}
}

type fakeRunRunner struct {
	command Command
	err     error
}

func (runner *fakeRunRunner) Output(context.Context, Command) ([]byte, error) {
	return nil, errors.New("unexpected Output call")
}

func (runner *fakeRunRunner) Run(_ context.Context, command Command, _ io.Reader, _ io.Writer, _ io.Writer) error {
	runner.command = command
	return runner.err
}

func TestSendKeysSendsLiteralTextThenEnter(t *testing.T) {
	runner := &fakeMultiRunRunner{}
	err := SendKeys(context.Background(), runner, "claude", "123e4567-e89b-42d3-a456-426614174000", "hello world")
	if err != nil {
		t.Fatal(err)
	}
	name := Key("claude", "123e4567-e89b-42d3-a456-426614174000")
	wantCommands := []Command{
		{
			Name: "tmux",
			Args: []string{"-L", SocketName, "-f", "/dev/null", "send-keys", "-t", "=" + name + ":", "-l", "--", "hello world"},
			Env:  []string{"TMUX=", "TMUX_PANE=", "TMUX_TMPDIR=/tmp"},
		},
		{
			Name: "tmux",
			Args: []string{"-L", SocketName, "-f", "/dev/null", "send-keys", "-t", "=" + name + ":", "Enter"},
			Env:  []string{"TMUX=", "TMUX_PANE=", "TMUX_TMPDIR=/tmp"},
		},
	}
	if !reflect.DeepEqual(runner.commands, wantCommands) {
		t.Fatalf("commands = %#v, want %#v", runner.commands, wantCommands)
	}
}

func TestSendKeysRequiresRunner(t *testing.T) {
	if err := SendKeys(context.Background(), nil, "claude", "id", "hi"); err == nil {
		t.Fatal("SendKeys() with nil runner did not error")
	}
}

func TestSendKeysPropagatesErrorFromLiteralSend(t *testing.T) {
	runner := &fakeMultiRunRunner{errs: []error{errors.New("no server")}}
	err := SendKeys(context.Background(), runner, "claude", "id", "hi")
	if err == nil {
		t.Fatal("SendKeys() did not propagate literal-send error")
	}
	if len(runner.commands) != 1 {
		t.Fatalf("commands issued = %d, want 1 (Enter must not be sent after a failed literal send)", len(runner.commands))
	}
}

func TestSendKeysPropagatesErrorFromEnterSend(t *testing.T) {
	runner := &fakeMultiRunRunner{errs: []error{nil, errors.New("no server")}}
	err := SendKeys(context.Background(), runner, "claude", "id", "hi")
	if err == nil {
		t.Fatal("SendKeys() did not propagate Enter-send error")
	}
	if len(runner.commands) != 2 {
		t.Fatalf("commands issued = %d, want 2", len(runner.commands))
	}
}

type fakeMultiRunRunner struct {
	commands []Command
	errs     []error
}

func (runner *fakeMultiRunRunner) Output(context.Context, Command) ([]byte, error) {
	return nil, errors.New("unexpected Output call")
}

func (runner *fakeMultiRunRunner) Run(_ context.Context, command Command, _ io.Reader, _ io.Writer, _ io.Writer) error {
	index := len(runner.commands)
	runner.commands = append(runner.commands, command)
	if index < len(runner.errs) {
		return runner.errs[index]
	}
	return nil
}
