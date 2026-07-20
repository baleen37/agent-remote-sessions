package runtime

import (
	"context"
	"fmt"
	"io"

	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

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
