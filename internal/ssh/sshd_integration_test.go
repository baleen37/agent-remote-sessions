package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/provider"
	arsruntime "github.com/baleen37/agent-remote-sessions/internal/runtime"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/creack/pty"
)

const runSSHDIntegration = "ARS_RUN_SSHD_INTEGRATION"

func TestEphemeralSSHDCollectsAndAttaches(t *testing.T) {
	if os.Getenv(runSSHDIntegration) != "1" {
		t.Skip("set ARS_RUN_SSHD_INTEGRATION=1 to run the disposable loopback sshd integration")
	}
	t.Setenv("TERM", "xterm-256color")
	sshd := integrationExecutable(t, "sshd")
	ssh := integrationExecutable(t, "ssh")
	sshKeygen := integrationExecutable(t, "ssh-keygen")
	tmux := integrationExecutable(t, "tmux")
	server := startEphemeralSSHD(t, sshd, ssh, sshKeygen, tmux)
	defaultTmux := newIntegrationDefaultTmuxSentinel(t, tmux, server.tmuxTemp)
	if server.tmuxSocket == defaultTmux.socket {
		t.Fatal("ARS and default tmux sentinel resolved to the same socket")
	}
	runner := configuredSSHRunner{ssh: ssh, config: server.clientConfig}

	collector := []byte("#!/bin/sh\n" +
		"nonce=$1\n" +
		"printf 'ARS/2 BEGIN %s\\n' \"$nonce\"\n" +
		`printf '%s\n' '{"type":"session","provider":"claude","native_id":"123e4567-e89b-42d3-a456-426614174000","updated_at":"2026-07-19T01:02:03Z","cwd":"/work/app","title":"Ephemeral SSH","runtime_state":"saved","attached_clients":0}'` + "\n" +
		`printf '%s\n' '{"type":"summary","provider":"claude","status":"ok","seen":1,"skipped":0}'` + "\n" +
		`printf '%s\n' '{"type":"summary","provider":"codex","status":"absent","seen":0,"skipped":0}'` + "\n" +
		`printf '%s\n' '{"type":"runtime","status":"ok"}'` + "\n" +
		"printf 'ARS/2 END %s 1\\n' \"$nonce\"\n")

	discovered, results, _, err := Collect(
		context.Background(),
		runner,
		integrationAssets{data: collector},
		server.target,
		CollectOptions{},
	)
	if err != nil {
		t.Fatalf("Collect() through ephemeral sshd: %v", err)
	}
	if len(discovered) != 1 || discovered[0].Candidate.NativeID != "123e4567-e89b-42d3-a456-426614174000" || len(results) != 2 {
		t.Fatalf("decoded collector result = (%#v, %#v), want one Claude session and two summaries", discovered, results)
	}
	leftovers, err := filepath.Glob(filepath.Join(server.remoteTemp, "ars-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("collector cleanup left nonce directories: %#v", leftovers)
	}
	verifyUnknownHostKeyRejected(t, ssh, server)
	t.Setenv("PATH", server.clientBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	exerciseRemoteAttachHandoff(t, server)
	server.cleanupTmux(t)
	defaultTmux.assertUnchanged(t)
}

func TestIntegrationTmuxCleanupReportsKillError(t *testing.T) {
	want := errors.New("kill failed")
	err := cleanupIntegrationTmux(context.Background(), func(context.Context) error { return want }, "unused", 0)
	if !errors.Is(err, want) {
		t.Fatalf("cleanup error = %v, want wrapped kill error", err)
	}
}

func TestIntegrationTmuxCleanupReportsLeaks(t *testing.T) {
	socket := filepath.Join(t.TempDir(), arsruntime.SocketName)
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := cleanupIntegrationTmux(ctx, func(context.Context) error { return nil }, socket, os.Getpid())
	if err == nil || !strings.Contains(err.Error(), "provider PID") || !strings.Contains(err.Error(), socket) {
		t.Fatalf("cleanup error = %v, want exact socket and provider PID leak", err)
	}
}

type ephemeralSSHD struct {
	target       string
	clientConfig string
	clientBin    string
	remoteTemp   string
	tmuxTemp     string
	providerPID  string
	root         string
	tmux         string
	tmuxSocket   string
	tmuxCleaned  bool
	command      *exec.Cmd
	done         <-chan error
	logFile      *os.File
	sshdKillSent bool
	sshdCleaned  bool
}

func startEphemeralSSHD(t *testing.T, sshd, ssh, sshKeygen, tmux string) *ephemeralSSHD {
	t.Helper()
	t.Setenv("TMPDIR", "/tmp")
	root := t.TempDir()
	remoteTemp := filepath.Join(root, "remote-tmp")
	if err := os.Mkdir(remoteTemp, 0o700); err != nil {
		t.Fatal(err)
	}
	tmuxTemp := filepath.Join(root, "tmux")
	remoteBin := filepath.Join(root, "remote-bin")
	clientBin := filepath.Join(root, "client-bin")
	for _, directory := range []string{tmuxTemp, remoteBin, clientBin} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	providerPID := filepath.Join(root, "provider.pid")
	writeIntegrationExecutable(t, filepath.Join(remoteBin, "tmux"), "#!/bin/sh\n"+
		"export TMUX_TMPDIR="+singleQuote(tmuxTemp)+"\n"+
		"exec "+singleQuote(tmux)+" \"$@\"\n")
	writeIntegrationExecutable(t, filepath.Join(remoteBin, "claude"), "#!/bin/sh\n"+
		"printf '%s\\n' \"$$\" > \"$ARS_TEST_PROVIDER_PID\"\n"+
		"printf 'ARS_REMOTE_PROVIDER_ATTACHED\\n'\n"+
		"trap 'exit 0' TERM INT HUP\n"+
		"while :; do sleep 1; done\n")
	hostKey := filepath.Join(root, "host-key")
	clientKey := filepath.Join(root, "client-key")
	generateIntegrationKey(t, sshKeygen, hostKey)
	generateIntegrationKey(t, sshKeygen, clientKey)
	authorizedKeys := filepath.Join(root, "authorized_keys")
	clientPublic, err := os.ReadFile(clientKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authorizedKeys, clientPublic, 0o600); err != nil {
		t.Fatal(err)
	}

	forceCommand := filepath.Join(root, "force-command")
	forceScript := "#!/bin/sh\n" +
		"export TMPDIR=" + singleQuote(remoteTemp) + "\n" +
		"export PATH=" + singleQuote(remoteBin+":/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin") + "\n" +
		"export ARS_TEST_PROVIDER_PID=" + singleQuote(providerPID) + "\n" +
		"exec /bin/sh -c \"$SSH_ORIGINAL_COMMAND\"\n"
	writeIntegrationExecutable(t, forceCommand, forceScript)

	currentUser, err := user.Current()
	if err != nil {
		t.Fatalf("resolve current user: %v", err)
	}
	port := reserveLoopbackPort(t)
	serverConfig := filepath.Join(root, "sshd_config")
	serverText := strings.Join([]string{
		"Port " + strconv.Itoa(port),
		"ListenAddress 127.0.0.1",
		"HostKey " + hostKey,
		"PidFile " + filepath.Join(root, "sshd.pid"),
		"AuthorizedKeysFile " + authorizedKeys,
		"AllowUsers " + currentUser.Username,
		"AuthenticationMethods publickey",
		"PubkeyAuthentication yes",
		"PasswordAuthentication no",
		"KbdInteractiveAuthentication no",
		"PermitRootLogin no",
		"StrictModes no",
		"UsePAM no",
		"AllowAgentForwarding no",
		"AllowTcpForwarding no",
		"X11Forwarding no",
		"PermitTunnel no",
		"PermitUserRC no",
		"UseDNS no",
		"LogLevel ERROR",
		"ForceCommand " + forceCommand,
	}, "\n") + "\n"
	if err := os.WriteFile(serverConfig, []byte(serverText), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(sshd, "-t", "-f", serverConfig).CombinedOutput(); err != nil {
		t.Fatalf("validate disposable sshd config: %v: %s", err, output)
	}

	hostPublic, err := os.ReadFile(hostKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	hostFields := strings.Fields(string(hostPublic))
	if len(hostFields) < 2 {
		t.Fatalf("invalid generated host public key: %q", hostPublic)
	}
	knownHosts := filepath.Join(root, "known_hosts")
	if err := os.WriteFile(knownHosts, []byte("ars-integration "+hostFields[0]+" "+hostFields[1]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	clientConfig := filepath.Join(root, "ssh_config")
	clientText := strings.Join([]string{
		"Host ars-integration",
		"  HostName 127.0.0.1",
		"  Port " + strconv.Itoa(port),
		"  User " + currentUser.Username,
		"  IdentityFile " + clientKey,
		"  IdentitiesOnly yes",
		"  IdentityAgent none",
		"  UserKnownHostsFile " + knownHosts,
		"  HostKeyAlias ars-integration",
		"  StrictHostKeyChecking yes",
		"  BatchMode yes",
		"  LogLevel ERROR",
	}, "\n") + "\n"
	if err := os.WriteFile(clientConfig, []byte(clientText), 0o600); err != nil {
		t.Fatal(err)
	}
	writeIntegrationExecutable(t, filepath.Join(clientBin, "ssh"), "#!/bin/sh\nexec "+singleQuote(ssh)+" -F "+singleQuote(clientConfig)+" \"$@\"\n")

	logPath := filepath.Join(root, "sshd.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(sshd, "-D", "-e", "-f", serverConfig)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start disposable sshd: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	server := &ephemeralSSHD{
		target:       "ars-integration",
		clientConfig: clientConfig,
		clientBin:    clientBin,
		remoteTemp:   remoteTemp,
		tmuxTemp:     tmuxTemp,
		providerPID:  providerPID,
		root:         root,
		tmux:         tmux,
		tmuxSocket:   filepath.Join(tmuxTemp, "tmux-"+strconv.Itoa(os.Getuid()), arsruntime.SocketName),
		command:      command,
		done:         done,
		logFile:      logFile,
	}
	t.Cleanup(func() { server.cleanupSSHD(t) })
	t.Cleanup(func() { server.cleanupTmux(t) })
	waitForSSHD(t, port, done, logPath)
	return server
}

func (server *ephemeralSSHD) cleanupTmux(t *testing.T) {
	t.Helper()
	if server.tmuxCleaned {
		return
	}
	providerPID := integrationProviderPIDIfPresent(t, server.providerPID)
	if !integrationPathExists(server.tmuxSocket) && providerPID == 0 {
		server.tmuxCleaned = true
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := cleanupIntegrationTmux(ctx, func(ctx context.Context) error {
		command := integrationTmuxCommand(ctx, server.tmux, server.tmuxTemp, true, "kill-server")
		output, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}, server.tmuxSocket, providerPID)
	if err != nil {
		t.Errorf("cleanup ephemeral SSH ARS tmux: %v", err)
		return
	}
	server.tmuxCleaned = true
}

func (server *ephemeralSSHD) cleanupSSHD(t *testing.T) {
	t.Helper()
	if server.sshdCleaned {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !server.sshdKillSent {
		if err := server.command.Process.Kill(); err != nil {
			t.Errorf("cleanup ephemeral sshd: kill owned process: %v", err)
			return
		}
		server.sshdKillSent = true
	}
	select {
	case <-server.done:
	case <-ctx.Done():
		t.Errorf("cleanup ephemeral sshd: owned process cleanup deadline: %v", ctx.Err())
		return
	}
	if err := server.logFile.Close(); err != nil {
		t.Errorf("close ephemeral sshd log: %v", err)
		return
	}
	server.sshdCleaned = true
}

type integrationDefaultTmuxSentinel struct {
	executable string
	tempDir    string
	socket     string
	pid        int
	before     integrationDefaultTmuxSnapshot
	cleaned    bool
}

type integrationDefaultTmuxSnapshot struct {
	sessions string
	ctrlQ    string
}

func newIntegrationDefaultTmuxSentinel(t *testing.T, tmux, tempDir string) *integrationDefaultTmuxSentinel {
	t.Helper()
	sentinel := &integrationDefaultTmuxSentinel{
		executable: tmux,
		tempDir:    tempDir,
		socket:     filepath.Join(tempDir, "tmux-"+strconv.Itoa(os.Getuid()), "default"),
	}
	sentinel.run(t, "new-session", "-d", "-s", "default-sentinel")
	t.Cleanup(func() { sentinel.cleanup(t) })
	pid, err := strconv.Atoi(strings.TrimSpace(sentinel.output(t, "list-panes", "-t", "=default-sentinel", "-F", "#{pane_pid}")))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid default tmux sentinel PID: %d (%v)", pid, err)
	}
	sentinel.pid = pid
	sentinel.run(t, "bind-key", "-n", "C-q", "display-message", "default-sentinel")
	sentinel.before = sentinel.snapshot(t)
	return sentinel
}

func (sentinel *integrationDefaultTmuxSentinel) assertUnchanged(t *testing.T) {
	t.Helper()
	after := sentinel.snapshot(t)
	if after != sentinel.before {
		t.Fatalf("test-owned default tmux changed:\nbefore: %#v\nafter:  %#v", sentinel.before, after)
	}
}

func (sentinel *integrationDefaultTmuxSentinel) snapshot(t *testing.T) integrationDefaultTmuxSnapshot {
	t.Helper()
	sessions := sentinel.output(t, "list-sessions", "-F", "#{session_id}\\t#{session_name}\\t#{session_created}")
	keys := sentinel.output(t, "list-keys", "-T", "root")
	var ctrlQ []string
	for _, line := range strings.Split(strings.TrimSpace(keys), "\n") {
		if strings.Contains(line, " C-q ") {
			ctrlQ = append(ctrlQ, line)
		}
	}
	if len(ctrlQ) == 0 {
		t.Fatal("test-owned default tmux has no C-q list-keys state")
	}
	return integrationDefaultTmuxSnapshot{sessions: sessions, ctrlQ: strings.Join(ctrlQ, "\n")}
}

func (sentinel *integrationDefaultTmuxSentinel) cleanup(t *testing.T) {
	t.Helper()
	if sentinel.cleaned {
		return
	}
	if !integrationPathExists(sentinel.socket) {
		sentinel.cleaned = true
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := cleanupIntegrationTmux(ctx, func(ctx context.Context) error {
		command := integrationTmuxCommand(ctx, sentinel.executable, sentinel.tempDir, false, "kill-server")
		output, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}, sentinel.socket, sentinel.pid)
	if err != nil {
		t.Errorf("cleanup test-owned default tmux: %v", err)
		return
	}
	sentinel.cleaned = true
}

func (sentinel *integrationDefaultTmuxSentinel) run(t *testing.T, args ...string) {
	t.Helper()
	command := integrationTmuxCommand(context.Background(), sentinel.executable, sentinel.tempDir, false, args...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run test-owned default tmux %q: %v: %s", args, err, output)
	}
}

func (sentinel *integrationDefaultTmuxSentinel) output(t *testing.T, args ...string) string {
	t.Helper()
	command := integrationTmuxCommand(context.Background(), sentinel.executable, sentinel.tempDir, false, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("query test-owned default tmux %q: %v: %s", args, err, output)
	}
	return string(output)
}

func integrationTmuxCommand(ctx context.Context, tmux, tempDir string, ars bool, args ...string) *exec.Cmd {
	prefix := []string{"-f", "/dev/null"}
	if ars {
		prefix = []string{"-L", arsruntime.SocketName, "-f", "/dev/null"}
	}
	command := exec.CommandContext(ctx, tmux, append(prefix, args...)...)
	command.Env = integrationTmuxEnv(tempDir)
	return command
}

func integrationTmuxEnv(tempDir string) []string {
	environment := make([]string, 0, len(os.Environ())+3)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "TMUX=") || strings.HasPrefix(value, "TMUX_PANE=") || strings.HasPrefix(value, "TMUX_TMPDIR=") {
			continue
		}
		environment = append(environment, value)
	}
	return append(environment, "TMUX=", "TMUX_PANE=", "TMUX_TMPDIR="+tempDir)
}

func cleanupIntegrationTmux(ctx context.Context, kill func(context.Context) error, socket string, providerPID int) error {
	if err := kill(ctx); err != nil {
		return fmt.Errorf("kill owned tmux server: %w", err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		providerAlive := providerPID > 0 && integrationProcessExists(providerPID)
		if !providerAlive {
			if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove owned tmux socket %s: %w", socket, err)
			}
		}
		socketAlive := integrationPathExists(socket)
		if !providerAlive && !socketAlive {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("owned tmux cleanup deadline: socket %s exists=%v; provider PID %d alive=%v: %w", socket, socketAlive, providerPID, providerAlive, ctx.Err())
		case <-ticker.C:
		}
	}
}

func integrationProviderPIDIfPresent(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid provider PID in %s: %q", path, data)
	}
	return pid
}

func integrationProcessExists(pid int) bool {
	process, err := os.FindProcess(pid)
	return err == nil && process.Signal(syscall.Signal(0)) == nil
}

func integrationPathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func integrationExecutable(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("sshd integration unavailable: %s was not found", name)
	}
	return path
}

func generateIntegrationKey(t *testing.T, sshKeygen, path string) {
	t.Helper()
	if output, err := exec.Command(sshKeygen, "-q", "-t", "ed25519", "-N", "", "-f", path).CombinedOutput(); err != nil {
		t.Fatalf("generate disposable SSH key: %v: %s", err, output)
	}
}

func writeIntegrationExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}

func verifyUnknownHostKeyRejected(t *testing.T, ssh string, server *ephemeralSSHD) {
	t.Helper()
	knownHosts := filepath.Join(server.root, "unknown-known-hosts")
	if err := os.WriteFile(knownHosts, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(server.clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(config), "\n")
	for index, line := range lines {
		if strings.HasPrefix(line, "  UserKnownHostsFile ") {
			lines[index] = "  UserKnownHostsFile " + knownHosts
		}
	}
	unknownConfig := filepath.Join(server.root, "unknown_ssh_config")
	if err := os.WriteFile(unknownConfig, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(ssh, "-F", unknownConfig, server.target, "true")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "Host key verification failed") {
		t.Fatalf("unknown host key was not rejected: error=%v output=%q", err, output)
	}
}

func exerciseRemoteAttachHandoff(t *testing.T, server *ephemeralSSHD) {
	t.Helper()
	item, err := session.BindDiscovered(server.target, session.Discovered{Candidate: session.Candidate{
		Provider:  session.Claude,
		NativeID:  "123e4567-e89b-42d3-a456-426614174000",
		UpdatedAt: time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC),
		CWD:       server.root,
		Title:     "Ephemeral SSH",
	}, Runtime: session.Runtime{State: session.RuntimeSaved}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	start := func() *integrationAttachClient {
		command, commandErr := NewAttachCommand(ctx, server.target, item, provider.ResumeSpec{
			Executable: "claude",
			Args:       []string{"--resume", item.NativeID},
		})
		if commandErr != nil {
			t.Fatal(commandErr)
		}
		return startIntegrationAttachClient(t, command)
	}

	first := start()
	waitIntegrationAttachOutput(t, first, "ARS_REMOTE_PROVIDER_ATTACHED")
	waitRemoteClients(t, server, 1)
	beforePID := readIntegrationProviderPID(t, server.providerPID)
	first.detach(t)
	waitRemoteClients(t, server, 0)
	afterDetachPID := readIntegrationProviderPID(t, server.providerPID)
	if beforePID != afterDetachPID {
		t.Fatalf("provider restarted: %d -> %d", beforePID, afterDetachPID)
	}

	previous := start()
	waitIntegrationAttachOutput(t, previous, "ARS_REMOTE_PROVIDER_ATTACHED")
	waitRemoteClients(t, server, 1)
	replacement := start()
	waitIntegrationAttachOutput(t, replacement, "ARS_REMOTE_PROVIDER_ATTACHED")
	previous.wait(t, "previous SSH client handoff")
	waitRemoteClients(t, server, 1)
	afterHandoffPID := readIntegrationProviderPID(t, server.providerPID)
	if beforePID != afterHandoffPID {
		t.Fatalf("provider restarted during second-client handoff: %d -> %d", beforePID, afterHandoffPID)
	}
	replacement.detach(t)
	waitRemoteClients(t, server, 0)
	if count := remoteSessionCount(t, server); count != 1 {
		t.Fatalf("remote ARS runtime count = %d, want 1", count)
	}
	if err := syscall.Kill(beforePID, 0); err != nil {
		t.Fatalf("provider PID %d did not survive remote detach: %v", beforePID, err)
	}
}

type integrationAttachClient struct {
	master   *os.File
	terminal *os.File
	done     chan error
	output   integrationCapture
}

func startIntegrationAttachClient(t *testing.T, command *AttachCommand) *integrationAttachClient {
	t.Helper()
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := pty.Setsize(master, &pty.Winsize{Rows: 24, Cols: 100}); err != nil {
		_ = master.Close()
		_ = terminal.Close()
		t.Fatal(err)
	}
	client := &integrationAttachClient{master: master, terminal: terminal, done: make(chan error, 1)}
	command.SetStdin(terminal)
	command.SetStdout(terminal)
	command.SetStderr(terminal)
	go func() { client.done <- command.Run() }()
	go func() { _, _ = io.Copy(&client.output, master) }()
	t.Cleanup(func() {
		_ = client.master.Close()
		_ = client.terminal.Close()
	})
	return client
}

func (client *integrationAttachClient) detach(t *testing.T) {
	t.Helper()
	if _, err := client.master.Write([]byte{0x11}); err != nil {
		t.Fatalf("write remote Ctrl+Q: %v", err)
	}
	client.wait(t, "remote Ctrl+Q")
}

func (client *integrationAttachClient) wait(t *testing.T, label string) {
	t.Helper()
	select {
	case err := <-client.done:
		if err != nil {
			t.Fatalf("%s returned %v; output: %q", label, err, client.output.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s; output: %q", label, client.output.String())
	}
}

type integrationCapture struct {
	mu sync.Mutex
	b  strings.Builder
}

func (capture *integrationCapture) Write(value []byte) (int, error) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.b.Write(value)
}

func (capture *integrationCapture) String() string {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.b.String()
}

func waitIntegrationAttachOutput(t *testing.T, client *integrationAttachClient, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-client.done:
			t.Fatalf("SSH attach exited before %q: %v; output: %q", want, err, client.output.String())
		default:
		}
		if strings.Contains(client.output.String(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; output: %q", want, client.output.String())
}

func readIntegrationProviderPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && pid > 0 {
				return pid
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("remote provider PID did not appear at %s", path)
	return 0
}

func waitRemoteClients(t *testing.T, server *ephemeralSSHD, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		clients, ok := remoteAttachedClients(server)
		if ok && clients == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("remote attached clients did not become %d", want)
}

func remoteAttachedClients(server *ephemeralSSHD) (int, bool) {
	command := exec.Command("tmux", "-L", arsruntime.SocketName, "-f", "/dev/null", "list-sessions", "-F", "#{session_attached}")
	command.Env = append(os.Environ(), "TMUX=", "TMUX_PANE=", "TMUX_TMPDIR="+server.tmuxTemp)
	output, err := command.Output()
	if err != nil {
		return 0, false
	}
	clients, err := strconv.Atoi(strings.TrimSpace(string(output)))
	return clients, err == nil
}

func remoteSessionCount(t *testing.T, server *ephemeralSSHD) int {
	t.Helper()
	command := exec.Command("tmux", "-L", arsruntime.SocketName, "-f", "/dev/null", "list-sessions", "-F", "#{session_name}")
	command.Env = append(os.Environ(), "TMUX=", "TMUX_PANE=", "TMUX_TMPDIR="+server.tmuxTemp)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list remote ARS runtimes: %v", err)
	}
	return len(strings.Fields(string(output)))
}

func reserveLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release loopback port: %v", err)
	}
	return port
}

func waitForSSHD(t *testing.T, port int, done <-chan error, logPath string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			log, _ := os.ReadFile(logPath)
			t.Fatalf("disposable sshd exited before readiness: %v: %s", err, log)
		default:
		}
		connection, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	log, _ := os.ReadFile(logPath)
	t.Fatalf("disposable sshd did not listen on loopback: %s", log)
}

type integrationAssets struct{ data []byte }

func (assets integrationAssets) ForTarget(string, string) ([]byte, error) {
	return append([]byte(nil), assets.data...), nil
}

type configuredSSHRunner struct {
	ssh    string
	config string
}

func (runner configuredSSHRunner) Run(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if name != "ssh" {
		return fmt.Errorf("unexpected command %q", name)
	}
	configured := make([]string, 0, len(args)+2)
	configured = append(configured, "-F", runner.config)
	configured = append(configured, args...)
	command := exec.CommandContext(ctx, runner.ssh, configured...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

var _ Runner = configuredSSHRunner{}
var _ CollectorAssets = integrationAssets{}
