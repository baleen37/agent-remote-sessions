package ssh

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	arsruntime "github.com/baleen37/agent-remote-sessions/internal/runtime"
)

func TestCapturePaneReadsRemotePane(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{run: func(_ context.Context, _ int, _ runnerCall, stdout, _ io.Writer) error {
		_, _ = io.WriteString(stdout, "remote line\n")
		return nil
	}}
	output, err := CapturePane(context.Background(), runner, "host", "claude", "123e4567-e89b-42d3-a456-426614174000")
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "remote line\n" {
		t.Fatalf("CapturePane() = %q", output)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.calls))
	}
	call := runner.calls[0]
	if call.name != "ssh" {
		t.Fatalf("runner name = %q, want ssh", call.name)
	}
	name := arsruntime.Key("claude", "123e4567-e89b-42d3-a456-426614174000")
	if got, want := call.args[len(call.args)-1], remoteShellCommand(capturePaneCommand(name)); got != want {
		t.Fatalf("capture command = %q, want %q", got, want)
	}
	script := capturePaneCommand(name)
	if !strings.Contains(script, "has-session -t '="+name+"'") {
		t.Fatalf("script missing has-session gate:\n%s", script)
	}
	if !strings.Contains(script, "capture-pane -p -t '="+name+":'") {
		t.Fatalf("script missing capture-pane:\n%s", script)
	}
}

func TestCapturePaneReportsNoLivePane(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{run: func(_ context.Context, _ int, _ runnerCall, stdout, _ io.Writer) error {
		_, _ = io.WriteString(stdout, noLivePaneMarker+"\n")
		return nil
	}}
	_, err := CapturePane(context.Background(), runner, "host", "claude", "123e4567-e89b-42d3-a456-426614174000")
	if !errors.Is(err, ErrNoLivePane) {
		t.Fatalf("CapturePane() error = %v, want ErrNoLivePane", err)
	}
}

func TestCapturePaneRequiresRunner(t *testing.T) {
	t.Parallel()

	if _, err := CapturePane(context.Background(), nil, "host", "claude", "id"); err == nil {
		t.Fatal("CapturePane() with nil runner did not error")
	}
}

func TestCapturePaneUsesSafeSSHOptions(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	_, _ = CapturePane(context.Background(), runner, "host", "claude", "id")
	call := runner.calls[0]
	joined := strings.Join(call.args, " ")
	for _, option := range []string{"BatchMode=yes", "ForwardAgent=no", "StrictHostKeyChecking=yes"} {
		if !strings.Contains(joined, option) {
			t.Fatalf("capture args missing %q: %v", option, call.args)
		}
	}
}

func TestKillSessionKillsRemoteSession(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	err := KillSession(context.Background(), runner, "host", "claude", "123e4567-e89b-42d3-a456-426614174000")
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.calls))
	}
	call := runner.calls[0]
	if call.name != "ssh" {
		t.Fatalf("runner name = %q, want ssh", call.name)
	}
	name := arsruntime.Key("claude", "123e4567-e89b-42d3-a456-426614174000")
	if got, want := call.args[len(call.args)-1], remoteShellCommand(killSessionCommand(name)); got != want {
		t.Fatalf("kill command = %q, want %q", got, want)
	}
	script := killSessionCommand(name)
	if !strings.Contains(script, "kill-session -t '="+name+"'") {
		t.Fatalf("script missing kill-session:\n%s", script)
	}
}

func TestKillSessionRequiresRunner(t *testing.T) {
	t.Parallel()

	if err := KillSession(context.Background(), nil, "host", "claude", "id"); err == nil {
		t.Fatal("KillSession() with nil runner did not error")
	}
}

func TestKillSessionReturnsErrorWithStderr(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{run: func(_ context.Context, _ int, _ runnerCall, _, stderr io.Writer) error {
		_, _ = io.WriteString(stderr, "no such session")
		return errors.New("exit status 1")
	}}
	err := KillSession(context.Background(), runner, "host", "claude", "id")
	if err == nil {
		t.Fatal("KillSession() did not propagate runner error")
	}
	if !strings.Contains(err.Error(), "no such session") {
		t.Fatalf("KillSession() error = %v, want stderr included", err)
	}
}
