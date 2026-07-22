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
		item.Provider,
		spec,
	)
	return &AttachCommand{
		command: exec.CommandContext(ctx, "ssh", "-tt", target, script),
	}, nil
}

func remoteAttachScript(name, cwd string, prov session.Provider, spec provider.ResumeSpec) string {
	command := tmuxShellPrefix()
	execCommand := "TMUX= TMUX_PANE= TMUX_TMPDIR=/tmp exec tmux -L " + arsruntime.SocketName + " -f /dev/null"
	target := quotePOSIX("=" + name)
	createArgs := []string{
		"new-session", "-d", "-s", quotePOSIX(name), "-c", quotePOSIX(cwd),
	}
	if prov == session.Claude {
		createArgs = append(createArgs, quotePOSIX(guardedPaneCommand(spec)))
	} else {
		createArgs = append(createArgs, quotePOSIX(spec.Executable))
		for _, arg := range spec.Args {
			createArgs = append(createArgs, quotePOSIX(arg))
		}
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

// guardedPaneCommand wraps the claude launcher in a keychain unlock guard.
// tmux executes a multi-word pane command directly via exec, so the guard and
// the launcher must travel as one shell-command word.
func guardedPaneCommand(spec provider.ResumeSpec) string {
	words := []string{quotePOSIX(spec.Executable)}
	for _, arg := range spec.Args {
		words = append(words, quotePOSIX(arg))
	}
	return darwinKeychainGuard() + " exec " + strings.Join(words, " ")
}

// darwinKeychainGuard returns a pane-command fragment that unlocks the macOS
// login keychain before launching claude. Claude Code stores credentials in
// the login keychain, which SSH sessions leave locked. Keychain lock state is
// per security session, so unlocking from the SSH shell would not reach a
// pre-existing tmux server; the unlock must run inside the pane itself.
func darwinKeychainGuard() string {
	return `if [ "$(uname)" = Darwin ] && security find-generic-password -s 'Claude Code-credentials' >/dev/null 2>&1 && ! security show-keychain-info >/dev/null 2>&1; then` +
		` echo 'ars: the macOS login keychain is locked, so Claude Code cannot read its credentials.';` +
		` echo 'ars: enter the Mac user password to unlock it (Ctrl-C to skip).';` +
		` trap : INT;` +
		` ars_tries=0;` +
		` until security unlock-keychain; do` +
		` ars_status=$?;` +
		` ars_tries=$((ars_tries+1));` +
		` if [ "$ars_status" -ge 128 ] || [ "$ars_tries" -ge 3 ]; then` +
		` echo 'ars: keychain still locked; claude will ask you to log in.';` +
		` break;` +
		` fi;` +
		` echo 'ars: unlock failed; try again.';` +
		` done;` +
		` trap - INT;` +
		` fi;`
}

func tmuxShellPrefix() string {
	return "TMUX= TMUX_PANE= TMUX_TMPDIR=/tmp tmux -L " + arsruntime.SocketName + " -f /dev/null"
}

func quotePOSIX(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
