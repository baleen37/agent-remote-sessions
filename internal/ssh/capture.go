package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	arsruntime "github.com/baleen37/agent-remote-sessions/internal/runtime"
)

const (
	capturePaneOutputLimit = 2 << 20
	noLivePaneMarker       = "ars:no-live-pane"
)

// ErrNoLivePane reports that the target session has no live tmux pane on the
// remote host, so the preview should show a "no live pane" notice.
var ErrNoLivePane = errors.New("no live pane")

// CapturePane reads the live pane contents of an ars-managed session on a
// remote host. It is an on-demand query for the TUI preview and is never part
// of collection. A missing session yields ErrNoLivePane rather than an error.
func CapturePane(ctx context.Context, runner Runner, target, provider, nativeID string) ([]byte, error) {
	if runner == nil {
		return nil, fmt.Errorf("SSH runner is nil")
	}
	name := arsruntime.Key(provider, nativeID)
	output := newBoundedBuffer(capturePaneOutputLimit)
	stderr := newBoundedBuffer(stderrOutputLimit)
	runErr := runner.Run(ctx, "ssh", collectionSSHArgs(target, defaultConnectTimeout, remoteShellCommand(capturePaneCommand(name))), nil, output, stderr)
	if runErr != nil {
		return nil, commandError("SSH capture-pane", runErr, stderr)
	}
	if output.exceeded {
		return nil, fmt.Errorf("SSH capture-pane stdout exceeds limit")
	}
	if strings.HasPrefix(output.String(), noLivePaneMarker) {
		return nil, ErrNoLivePane
	}
	return output.Bytes(), nil
}

func capturePaneCommand(name string) string {
	tmux := tmuxShellPrefix()
	sessionTarget := singleQuote("=" + name)
	// The trailing colon resolves "=name:" as the session's active pane;
	// "=name" alone is read as a pane spec and fails to match.
	paneTarget := singleQuote("=" + name + ":")
	return "if " + tmux + " has-session -t " + sessionTarget + " >/dev/null 2>&1; then " +
		tmux + " capture-pane -p -t " + paneTarget + "; else " +
		"printf '%s\\n' '" + noLivePaneMarker + "'; fi"
}

// KillSession terminates an ars-managed tmux session on a remote host. It is
// an on-demand action for the TUI kill key and is never part of collection.
func KillSession(ctx context.Context, runner Runner, target, provider, nativeID string) error {
	if runner == nil {
		return fmt.Errorf("SSH runner is nil")
	}
	name := arsruntime.Key(provider, nativeID)
	stderr := newBoundedBuffer(stderrOutputLimit)
	runErr := runner.Run(ctx, "ssh", collectionSSHArgs(target, defaultConnectTimeout, remoteShellCommand(killSessionCommand(name))), nil, io.Discard, stderr)
	if runErr != nil {
		return commandError("SSH kill-session", runErr, stderr)
	}
	return nil
}

func killSessionCommand(name string) string {
	tmux := tmuxShellPrefix()
	// No trailing colon: kill-session targets the whole session, not a pane.
	sessionTarget := singleQuote("=" + name)
	return tmux + " kill-session -t " + sessionTarget
}
