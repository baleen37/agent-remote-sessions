package runtime

import (
	"context"
	"errors"
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
