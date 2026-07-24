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
