package ssh

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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
		`'\''claude'\'' '\''--resume'\'' '\''123e4567-e89b-42d3-a456-426614174000'\''`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "new-session -A") {
		t.Fatalf("script uses attach-on-create:\n%s", script)
	}
}

func TestRemoteAttachShowsDetachHintOnStatusLineBeforeAttach(t *testing.T) {
	command, err := NewAttachCommand(context.Background(), "devbox", remoteAttachedSession(), remoteClaudeSpec())
	if err != nil {
		t.Fatal(err)
	}
	script := command.command.Args[3]
	hint := "set-option -g status-right " + quotePOSIX(arsruntime.DetachHint())
	if !strings.Contains(script, hint) {
		t.Fatalf("script missing detach status hint %q:\n%s", hint, script)
	}
	if hintAt, attachAt := strings.Index(script, hint), strings.Index(script, "attach-session -d"); hintAt == -1 || hintAt > attachAt {
		t.Fatalf("status hint is not set before attach:\n%s", script)
	}
	if !strings.Contains(arsruntime.DetachHint(), "ctrl-q") {
		t.Fatalf("shared detach hint does not name the key: %q", arsruntime.DetachHint())
	}
	if !strings.Contains(arsruntime.DetachHint(), "tmux -L "+arsruntime.SocketName()) {
		t.Fatalf("shared detach hint does not count the ars socket's own sessions: %q", arsruntime.DetachHint())
	}
}

func TestRemoteAttachSetsStatusIntervalBeforeAttach(t *testing.T) {
	command, err := NewAttachCommand(context.Background(), "devbox", remoteAttachedSession(), remoteClaudeSpec())
	if err != nil {
		t.Fatal(err)
	}
	script := command.command.Args[3]
	interval := "set-option -g status-interval 5"
	if !strings.Contains(script, interval) {
		t.Fatalf("script missing status-interval %q:\n%s", interval, script)
	}
	if intervalAt, attachAt := strings.Index(script, interval), strings.Index(script, "attach-session -d"); intervalAt == -1 || intervalAt > attachAt {
		t.Fatalf("status-interval is not set before attach:\n%s", script)
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

func TestRemoteAttachEmbedsSharedExternalResolverBeforeCreate(t *testing.T) {
	item := remoteAttachedSession()
	command, err := NewAttachCommand(
		context.Background(), item.Host, item, remoteClaudeSpec(),
	)
	if err != nil {
		t.Fatal(err)
	}
	script := command.command.Args[3]
	if strings.Contains(script, "\t") {
		t.Fatalf("remote attach command contains a literal tab:\n%s", script)
	}
	has := strings.Index(script, "has-session -t '="+remoteRuntimeKey(item)+"'")
	resolve := strings.Index(
		script,
		quotePOSIX(arsruntime.ExternalResolverScript()),
	)
	create := strings.Index(script, "new-session -d")
	if has == -1 || resolve <= has || create <= resolve {
		t.Fatalf("attach order has=%d resolve=%d create=%d:\n%s", has, resolve, create, script)
	}
	if !strings.Contains(script, "ars-external 'claude' '"+item.NativeID+"'") {
		t.Fatalf("resolver does not pass provider and ID as separate quoted words:\n%s", script)
	}
}

func TestRemoteExternalAttachPreservesClientsAndServerOptions(t *testing.T) {
	calls, output, err := runRemoteAttachScriptFixture(t, "match")
	if err != nil {
		t.Fatalf("remote external attach: %v; output: %s; calls: %s", err, output, calls)
	}
	if !strings.Contains(calls, "list-panes -a") ||
		!strings.Contains(calls, "-S ") ||
		!strings.Contains(calls, "attach-session -t $1:0.0") {
		t.Fatalf("external target was not listed then attached:\n%s", calls)
	}
	for _, forbidden := range []string{"new-session", "bind-key", "set-option", "attach-session -d"} {
		if strings.Contains(calls, forbidden) {
			t.Fatalf("external attach changed ARS or detached clients with %q:\n%s", forbidden, calls)
		}
	}

	t.Run("none reaches ARS create race", func(t *testing.T) {
		calls, output, err := runRemoteAttachScriptFixture(t, "none")
		if err != nil {
			t.Fatalf("remote no-match attach: %v; output: %s; calls: %s", err, output, calls)
		}
		if !strings.Contains(calls, "new-session -d") || !strings.Contains(calls, "attach-session -d") {
			t.Fatalf("no-match did not reach the ARS create path:\n%s", calls)
		}
	})
}

func TestRemoteExternalConflictCannotReachNewSession(t *testing.T) {
	calls, output, err := runRemoteAttachScriptFixture(t, "conflict")
	if err == nil {
		t.Fatalf("remote external conflict succeeded; output: %s; calls: %s", output, calls)
	}
	if !strings.Contains(calls, "list-panes -a") {
		t.Fatalf("remote external conflict did not inspect panes:\n%s", calls)
	}
	for _, forbidden := range []string{"new-session", "bind-key", "set-option", "attach-session"} {
		if strings.Contains(calls, forbidden) {
			t.Fatalf("remote external conflict reached a later tmux action %q:\n%s", forbidden, calls)
		}
	}
}

func TestRemoteVanishedExternalTargetCannotReachNewSession(t *testing.T) {
	calls, output, err := runRemoteAttachScriptFixture(t, "vanished")
	if err == nil {
		t.Fatalf("remote vanished external target succeeded; output: %s; calls: %s", output, calls)
	}
	for _, want := range []string{"list-panes -a", "attach-session -t $1:0.0"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("remote vanished external target did not call %q:\n%s", want, calls)
		}
	}
	for _, forbidden := range []string{"new-session", "bind-key", "set-option", "attach-session -d"} {
		if strings.Contains(calls, forbidden) {
			t.Fatalf("remote vanished external target reached ARS action %q:\n%s", forbidden, calls)
		}
	}
}

func runRemoteAttachScriptFixture(t *testing.T, mode string) (string, string, error) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "ars-ssh-attach-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	uid := os.Getuid()
	tmuxDir := filepath.Join(root, "tmux-"+strconv.Itoa(uid))
	if mode != "none" {
		if err := os.Mkdir(tmuxDir, 0o700); err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("unix", filepath.Join(tmuxDir, "external"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
	}
	calls := filepath.Join(root, "tmux-calls")
	writeRemoteAttachExecutable(t, filepath.Join(bin, "id"), "#!/bin/sh\necho "+strconv.Itoa(uid)+"\n")
	writeRemoteAttachExecutable(t, filepath.Join(bin, "ps"), "#!/bin/sh\nif [ \"$1\" = -U ]; then printf '100 1 sh\\n101 100 claude\\n'; exit 0; fi\nif [ \"$1\" = -p ]; then printf 'claude --resume 123e4567-e89b-42d3-a456-426614174000\\n'; fi\n")
	writeRemoteAttachExecutable(t, filepath.Join(bin, "tmux"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >>\"$ARS_REMOTE_ATTACH_CALLS\"\nif [ \"$1\" = -L ]; then\n  case \"$5\" in has-session) exit 1 ;; *) exit 0 ;; esac\nfi\nif [ \"$1\" = -S ]; then\n  case \"$5\" in\n    list-panes)\n      [ \"$2\" = \"$ARS_REMOTE_ATTACH_SOCKET\" ] || exit 0\n      case \"$ARS_REMOTE_ATTACH_MODE\" in conflict) printf '$1:0.0\\t100\\n$2:0.0\\t100\\n' ;; *) printf '$1:0.0\\t100\\n' ;; esac\n      exit 0 ;;\n    attach-session) [ \"$ARS_REMOTE_ATTACH_MODE\" = vanished ] && exit 1; exit 0 ;;\n  esac\nfi\nexit 1\n")
	item := remoteAttachedSession()
	attach, err := NewAttachCommand(context.Background(), item.Host, item, remoteClaudeSpec())
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/sh", "-c", attach.command.Args[3])
	command.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TMPDIR="+root,
		"TMUX_TMPDIR="+root,
		"ARS_REMOTE_ATTACH_CALLS="+calls,
		"ARS_REMOTE_ATTACH_MODE="+mode,
		"ARS_REMOTE_ATTACH_SOCKET="+filepath.Join(tmuxDir, "external"),
	)
	output, err := command.CombinedOutput()
	data, readErr := os.ReadFile(calls)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	return string(data), string(output), err
}

func writeRemoteAttachExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteAttachClaudeUnlocksLockedDarwinKeychainBeforeLaunch(t *testing.T) {
	command, err := NewAttachCommand(context.Background(), "devbox", remoteAttachedSession(), remoteClaudeSpec())
	if err != nil {
		t.Fatal(err)
	}
	script := command.command.Args[3]
	for _, want := range []string{
		`[ "$(uname)" = Darwin ]`,
		"Claude Code-credentials",
		"show-keychain-info",
		// A mistyped password must re-prompt instead of dropping the user
		// into a logged-out claude; only interrupts and repeated failures
		// fall through.
		"until security unlock-keychain",
		"try again",
		// The guard and launcher must be one shell-command word: tmux runs
		// multi-word pane commands via exec, without a shell.
		`fi; exec '\''claude'\'' '\''--resume'\'' '\''123e4567-e89b-42d3-a456-426614174000'\'''`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
	if guard, create := strings.Index(script, "unlock-keychain"), strings.Index(script, "new-session"); guard < create {
		t.Fatalf("keychain guard is outside the created pane:\n%s", script)
	}
}

func TestRemoteAttachCodexUsesLoginShellWithoutKeychainGuard(t *testing.T) {
	item := remoteAttachedSession()
	item.Provider = session.Codex
	spec := provider.ResumeSpec{Executable: "codex", Args: []string{"resume", item.NativeID}}
	command, err := NewAttachCommand(context.Background(), item.Host, item, spec)
	if err != nil {
		t.Fatal(err)
	}
	script := command.command.Args[3]
	if strings.Contains(script, "unlock-keychain") || strings.Contains(script, "Claude Code-credentials") {
		t.Fatalf("codex script must not carry the claude keychain guard:\n%s", script)
	}
	want := `exec "${SHELL:-/bin/sh}" -l -i -c '\''exec '\''\'\'''\''codex'\''\'\'''\'' '\''\'\'''\''resume'\''\'\'''\'' '\''\'\'''\''` +
		item.NativeID + `'\''\'\'''\'''\'''`
	if !strings.Contains(script, want) {
		t.Fatalf("codex script missing login-shell launcher %q:\n%s", want, script)
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
