package ssh

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/provider"
	arsruntime "github.com/baleen37/agent-remote-sessions/internal/runtime"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

type terminalCommand interface {
	Run() error
	SetStdin(io.Reader)
	SetStdout(io.Writer)
	SetStderr(io.Writer)
}

func TestAttachCommandImplementsTerminalCommandContract(t *testing.T) {
	command, err := NewAttachCommand(context.Background(), "devbox", remoteAttachedSession(), remoteClaudeSpec())
	if err != nil {
		t.Fatal(err)
	}
	var _ terminalCommand = command
	stdin := strings.NewReader("input")
	command.SetStdin(stdin)
	command.SetStdout(io.Discard)
	command.SetStderr(io.Discard)
	if command.command.Stdin != stdin || command.command.Stdout != io.Discard || command.command.Stderr != io.Discard {
		t.Fatal("terminal stream setters did not configure SSH command")
	}
}

func TestRemoteAttachUsesOneTargetAndFixedLauncher(t *testing.T) {
	item := remoteAttachedSession()
	item.Host = "user@host;$literal"
	command, err := NewAttachCommand(context.Background(), item.Host, item, remoteClaudeSpec())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(command.command.Path) != "ssh" || len(command.command.Args) != 4 ||
		!slices.Equal(command.command.Args[:3], []string{"ssh", "-tt", item.Host}) {
		t.Fatalf("argv = %#v", command.command.Args)
	}
	script := command.command.Args[3]
	for _, want := range []string{
		"set -eu",
		"TMUX= TMUX_PANE= TMUX_TMPDIR=/tmp",
		"tmux -L ars-v1 -f /dev/null",
		"bind-key -n C-q detach-client",
		"attach-session -d",
		"'/work/it'\\''s app'",
		"'claude' '--resume' '123e4567-e89b-42d3-a456-426614174000'",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "new-session -A") {
		t.Fatalf("script uses attach-on-create:\n%s", script)
	}
}

func TestRemoteAttachScriptRechecksCreateRaceAndUsesExactTargets(t *testing.T) {
	item := remoteAttachedSession()
	command, err := NewAttachCommand(context.Background(), item.Host, item, remoteClaudeSpec())
	if err != nil {
		t.Fatal(err)
	}
	script := command.command.Args[3]
	key := remoteRuntimeKey(item)
	if count := strings.Count(script, "has-session -t '="+key+"'"); count != 2 {
		t.Fatalf("has-session exact target count = %d, want 2:\n%s", count, script)
	}
	if !strings.Contains(script, "new-session -d -s '"+key+"'") ||
		!strings.Contains(script, "attach-session -d -t '="+key+"'") {
		t.Fatalf("script does not use exact runtime key:\n%s", script)
	}
}

func TestRemoteAttachRejectsInvalidInputBeforeSSH(t *testing.T) {
	valid := remoteAttachedSession()
	tests := []struct {
		name   string
		target string
		item   session.Session
		spec   provider.ResumeSpec
	}{
		{name: "host mismatch", target: "other", item: valid, spec: remoteClaudeSpec()},
		{name: "invalid target", target: "-option", item: withRemoteHost(valid, "-option"), spec: remoteClaudeSpec()},
		{name: "invalid session", target: valid.Host, item: withRemoteCWD(valid, "relative"), spec: remoteClaudeSpec()},
		{name: "invalid runtime", target: valid.Host, item: withRemoteRuntime(valid, session.Runtime{}), spec: remoteClaudeSpec()},
		{name: "invalid spec", target: valid.Host, item: valid, spec: provider.ResumeSpec{Executable: "sh", Args: []string{"-c", "evil"}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, err := NewAttachCommand(context.Background(), test.target, test.item, test.spec)
			if err == nil || command != nil {
				t.Fatalf("NewAttachCommand() = (%#v, %v), want nil command and error", command, err)
			}
		})
	}
}

func TestRemoteAttachCommandPreservesSSHExitCode(t *testing.T) {
	command, err := NewAttachCommand(context.Background(), "devbox", remoteAttachedSession(), remoteClaudeSpec())
	if err != nil {
		t.Fatal(err)
	}
	command.command.Path = os.Args[0]
	command.command.Args = []string{os.Args[0], "-test.run=TestRemoteAttachHelperProcess", "--"}
	command.command.Env = append(os.Environ(), "GO_WANT_REMOTE_ATTACH_HELPER=1")
	command.SetStdin(strings.NewReader(""))
	command.SetStdout(io.Discard)
	command.SetStderr(io.Discard)

	err = command.Run()
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 42 {
		t.Fatalf("Run() error = (%T, %v), want exit code 42", err, err)
	}
}

func TestRemoteAttachHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_REMOTE_ATTACH_HELPER") == "1" {
		os.Exit(42)
	}
}

func remoteAttachedSession() session.Session {
	return session.Session{Host: "devbox", Candidate: session.Candidate{
		Provider:  session.Claude,
		NativeID:  "123e4567-e89b-42d3-a456-426614174000",
		UpdatedAt: time.Unix(1, 0),
		CWD:       "/work/it's app",
		Title:     "title",
	}, Runtime: session.Runtime{State: session.RuntimeSaved}}
}

func remoteClaudeSpec() provider.ResumeSpec {
	item := remoteAttachedSession()
	return provider.ResumeSpec{Executable: "claude", Args: []string{"--resume", item.NativeID}}
}

func remoteRuntimeKey(item session.Session) string {
	return arsruntime.Key(string(item.Provider), item.NativeID)
}

func withRemoteHost(item session.Session, host string) session.Session {
	item.Host = host
	return item
}

func withRemoteCWD(item session.Session, cwd string) session.Session {
	item.CWD = cwd
	return item
}

func withRemoteRuntime(item session.Session, value session.Runtime) session.Session {
	item.Runtime = value
	return item
}
