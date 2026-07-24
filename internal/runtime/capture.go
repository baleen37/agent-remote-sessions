package runtime

import (
	"context"
	"fmt"
	"io"
)

// CapturePane reads the live pane contents of a local ars-managed session. It
// is an on-demand query for the TUI preview and is never part of collection.
func CapturePane(ctx context.Context, runner Runner, provider, nativeID string) ([]byte, error) {
	if runner == nil {
		return nil, fmt.Errorf("tmux runner is nil")
	}
	name := Key(provider, nativeID)
	// The trailing colon resolves "=name:" as the session's active pane;
	// "=name" alone is read as a pane spec and fails to match.
	return runner.Output(ctx, arsTMUXCommand("capture-pane", "-p", "-t", "="+name+":"))
}

// KillSession terminates a local ars-managed tmux session.
func KillSession(ctx context.Context, runner Runner, provider, nativeID string) error {
	if runner == nil {
		return fmt.Errorf("tmux runner is nil")
	}
	name := Key(provider, nativeID)
	return runner.Run(ctx, arsTMUXCommand("kill-session", "-t", "="+name), nil, io.Discard, io.Discard)
}

// SendKeys sends a single line of text to a local ars-managed session's pane
// without attaching to it: the text is sent literally (so it cannot be
// misread as key names), followed by Enter to submit it.
func SendKeys(ctx context.Context, runner Runner, provider, nativeID, text string) error {
	if runner == nil {
		return fmt.Errorf("tmux runner is nil")
	}
	name := Key(provider, nativeID)
	target := "=" + name + ":"
	if err := runner.Run(ctx, arsTMUXCommand("send-keys", "-t", target, "-l", "--", text), nil, io.Discard, io.Discard); err != nil {
		return err
	}
	return runner.Run(ctx, arsTMUXCommand("send-keys", "-t", target, "Enter"), nil, io.Discard, io.Discard)
}
