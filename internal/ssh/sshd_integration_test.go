package ssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const runSSHDIntegration = "ARS_RUN_SSHD_INTEGRATION"

func TestEphemeralSSHDCollects(t *testing.T) {
	if os.Getenv(runSSHDIntegration) != "1" {
		t.Skip("set ARS_RUN_SSHD_INTEGRATION=1 to run the disposable loopback sshd integration")
	}
	sshd := integrationExecutable(t, "sshd")
	ssh := integrationExecutable(t, "ssh")
	sshKeygen := integrationExecutable(t, "ssh-keygen")
	server := startEphemeralSSHD(t, sshd, sshKeygen)
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

}

type ephemeralSSHD struct {
	target       string
	clientConfig string
	remoteTemp   string
}

func startEphemeralSSHD(t *testing.T, sshd, sshKeygen string) ephemeralSSHD {
	t.Helper()
	root := t.TempDir()
	remoteTemp := filepath.Join(root, "remote-tmp")
	if err := os.Mkdir(remoteTemp, 0o700); err != nil {
		t.Fatal(err)
	}
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
		"exec /bin/sh -c \"$SSH_ORIGINAL_COMMAND\"\n"
	if err := os.WriteFile(forceCommand, []byte(forceScript), 0o700); err != nil {
		t.Fatal(err)
	}

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
	t.Cleanup(func() {
		_ = command.Process.Kill()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		_ = logFile.Close()
	})
	waitForSSHD(t, port, done, logPath)
	return ephemeralSSHD{
		target:       "ars-integration",
		clientConfig: clientConfig,
		remoteTemp:   remoteTemp,
	}
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
