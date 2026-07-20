package ssh

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/baleen37/agent-remote-sessions/internal/provider"
	arsruntime "github.com/baleen37/agent-remote-sessions/internal/runtime"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

type AttachCommand struct {
	command *exec.Cmd
}

func (command *AttachCommand) Run() error {
	return command.command.Run()
}

func (command *AttachCommand) SetStdin(stdin io.Reader) {
	command.command.Stdin = stdin
}

func (command *AttachCommand) SetStdout(stdout io.Writer) {
	command.command.Stdout = stdout
}

func (command *AttachCommand) SetStderr(stderr io.Writer) {
	command.command.Stderr = stderr
}

func NewAttachCommand(
	ctx context.Context,
	target string,
	item session.Session,
	spec provider.ResumeSpec,
) (*AttachCommand, error) {
	if item.Host != target {
		return nil, fmt.Errorf("session host does not match SSH target")
	}
	if _, err := session.BindDiscovered(target, session.Discovered{
		Candidate: item.Candidate,
		Runtime:   item.Runtime,
	}); err != nil {
		return nil, fmt.Errorf("invalid attach session: %w", err)
	}
	if !provider.ValidResumeSpec(item.Provider, item.NativeID, spec) {
		return nil, fmt.Errorf("provider returned an invalid resume command")
	}

	script := remoteAttachScript(
		arsruntime.Key(string(item.Provider), item.NativeID),
		item.CWD,
		spec,
	)
	return &AttachCommand{
		command: exec.CommandContext(ctx, "ssh", "-tt", target, script),
	}, nil
}

func remoteAttachScript(name, cwd string, spec provider.ResumeSpec) string {
	command := tmuxShellPrefix()
	execCommand := "TMUX= TMUX_PANE= TMUX_TMPDIR=/tmp exec tmux -L " + arsruntime.SocketName + " -f /dev/null"
	target := quotePOSIX("=" + name)
	createArgs := []string{
		"new-session", "-d", "-s", quotePOSIX(name), "-c", quotePOSIX(cwd), quotePOSIX(spec.Executable),
	}
	for _, arg := range spec.Args {
		createArgs = append(createArgs, quotePOSIX(arg))
	}

	return strings.Join([]string{
		"set -eu",
		"if " + command + " has-session -t " + target + " >/dev/null 2>&1; then",
		"  :",
		"else",
		"  if " + command + " " + strings.Join(createArgs, " ") + "; then",
		"    :",
		"  else",
		"    create_status=$?",
		"    if " + command + " has-session -t " + target + " >/dev/null 2>&1; then",
		"      :",
		"    else",
		"      exit \"$create_status\"",
		"    fi",
		"  fi",
		"fi",
		command + " bind-key -n C-q detach-client",
		execCommand + " attach-session -d -t " + target,
	}, "\n")
}

func tmuxShellPrefix() string {
	return "TMUX= TMUX_PANE= TMUX_TMPDIR=/tmp tmux -L " + arsruntime.SocketName + " -f /dev/null"
}

func quotePOSIX(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
