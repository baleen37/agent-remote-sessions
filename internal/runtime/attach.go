package runtime

import (
	"context"
	"fmt"
	"io"

	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

// DetachHint is the status-right override shown while attached: a live count
// of ars sessions on this host's ars socket, the detach key, and the usual
// tmux clock. The count is a tmux `#()` shell interpolation, refreshed every
// status-interval seconds by the tmux server rendering the status line, so it
// always reports that host's own ars sessions with no daemon and no
// local/remote branching. Local and remote attach share it so the status line
// reads the same everywhere.
const DetachHint = "#(TMUX= TMUX_PANE= TMUX_TMPDIR=/tmp tmux -L " + SocketName + " -f /dev/null list-sessions 2>/dev/null | wc -l | tr -d ' ') ars · ctrl-q detach  %H:%M "

type AttachCommand struct {
	ctx    context.Context
	runner Runner
	item   session.Session
	spec   provider.ResumeSpec
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func NewAttachCommand(
	ctx context.Context,
	runner Runner,
	item session.Session,
	spec provider.ResumeSpec,
) (*AttachCommand, error) {
	if runner == nil {
		return nil, fmt.Errorf("tmux runner is nil")
	}
	if _, err := session.BindDiscovered(item.Host, session.Discovered{
		Candidate: item.Candidate,
		Runtime:   item.Runtime,
	}); err != nil {
		return nil, fmt.Errorf("invalid attach session: %w", err)
	}
	if !provider.ValidResumeSpec(item.Provider, item.NativeID, spec) {
		return nil, fmt.Errorf("provider returned an invalid resume command")
	}
	spec.Args = append([]string(nil), spec.Args...)
	return &AttachCommand{ctx: ctx, runner: runner, item: item, spec: spec}, nil
}

func (command *AttachCommand) SetStdin(stdin io.Reader)   { command.stdin = stdin }
func (command *AttachCommand) SetStdout(stdout io.Writer) { command.stdout = stdout }
func (command *AttachCommand) SetStderr(stderr io.Writer) { command.stderr = stderr }

func (command *AttachCommand) Run() error {
	name := Key(string(command.item.Provider), command.item.NativeID)
	if err := command.runner.Run(command.ctx, hasSession(name), nil, io.Discard, io.Discard); err != nil {
		if createErr := command.runner.Run(
			command.ctx,
			newSession(name, command.item.CWD, command.spec),
			nil,
			io.Discard,
			command.stderr,
		); createErr != nil {
			if checkErr := command.runner.Run(command.ctx, hasSession(name), nil, io.Discard, io.Discard); checkErr != nil {
				return fmt.Errorf("create runtime: %w", createErr)
			}
		}
	}
	if err := command.runner.Run(command.ctx, bindDetach(), nil, io.Discard, command.stderr); err != nil {
		return fmt.Errorf("bind detach key: %w", err)
	}
	if err := command.runner.Run(command.ctx, showDetachHint(), nil, io.Discard, command.stderr); err != nil {
		return fmt.Errorf("show detach hint: %w", err)
	}
	if err := command.runner.Run(command.ctx, setStatusInterval(), nil, io.Discard, command.stderr); err != nil {
		return fmt.Errorf("set status interval: %w", err)
	}
	return command.runner.Run(
		command.ctx,
		attachSession(name),
		command.stdin,
		command.stdout,
		command.stderr,
	)
}

func hasSession(name string) Command {
	return arsTMUXCommand("has-session", "-t", "="+name)
}

func newSession(name, cwd string, spec provider.ResumeSpec) Command {
	args := []string{"new-session", "-d", "-s", name, "-c", cwd, spec.Executable}
	args = append(args, spec.Args...)
	return arsTMUXCommand(args...)
}

func bindDetach() Command {
	return arsTMUXCommand("bind-key", "-n", "C-q", "detach-client")
}

// showDetachHint keeps a persistent "ctrl-q detach" note and the live ars
// session count on the tmux status line, since the detach binding lives in
// tmux rather than ars and is otherwise invisible. The default status line is
// already on, so overriding status-right reuses space the user can see the
// whole session.
func showDetachHint() Command {
	return arsTMUXCommand("set-option", "-g", "status-right", DetachHint)
}

// setStatusInterval speeds up status-right refreshes so the live session
// count in DetachHint stays current, rather than the tmux default of 15s.
func setStatusInterval() Command {
	return arsTMUXCommand("set-option", "-g", "status-interval", "5")
}

func attachSession(name string) Command {
	return arsTMUXCommand("attach-session", "-d", "-t", "="+name)
}

func arsTMUXCommand(args ...string) Command {
	return Command{
		Name: "tmux",
		Args: append([]string{"-L", SocketName, "-f", "/dev/null"}, args...),
		Env:  []string{"TMUX=", "TMUX_PANE=", "TMUX_TMPDIR=/tmp"},
	}
}
