package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/protocol"
	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func TestRemoteShellCommandSafelyQuotesSingleQuotes(t *testing.T) {
	t.Parallel()

	script := `printf '%s' "it's safe"`
	command := remoteShellCommand(script)
	want := `/bin/sh -c 'printf '\''%s'\'' "it'\''s safe"'`
	if command != want {
		t.Fatalf("remoteShellCommand() = %q, want %q", command, want)
	}
	shells := []string{"/bin/sh"}
	if csh, err := exec.LookPath("csh"); err == nil {
		shells = append(shells, csh)
	}
	for _, shell := range shells {
		output, err := exec.Command(shell, "-c", command).Output()
		if err != nil {
			t.Fatalf("execute wrapped command through %s: %v", shell, err)
		}
		if got := string(output); got != "it's safe" {
			t.Fatalf("wrapped output through %s = %q, want %q", shell, got, "it's safe")
		}
	}
}

type runnerCall struct {
	name  string
	args  []string
	stdin []byte
}

type fakeRunner struct {
	calls []runnerCall
	run   func(context.Context, int, runnerCall, io.Writer, io.Writer) error
}

func (runner *fakeRunner) Run(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	var input []byte
	if stdin != nil {
		var err error
		input, err = io.ReadAll(stdin)
		if err != nil {
			return err
		}
	}
	call := runnerCall{name: name, args: append([]string(nil), args...), stdin: input}
	runner.calls = append(runner.calls, call)
	if runner.run == nil {
		return nil
	}
	return runner.run(ctx, len(runner.calls)-1, call, stdout, stderr)
}

type fakeAssets struct {
	requests [][2]string
	data     []byte
	err      error
}

func (assets *fakeAssets) ForTarget(goos, goarch string) ([]byte, error) {
	assets.requests = append(assets.requests, [2]string{goos, goarch})
	return assets.data, assets.err
}

func TestEmbeddedCollectorAssetsSupportExactTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		goos, goarch string
		wantName     string
		wantErr      bool
	}{
		{goos: "darwin", goarch: "arm64", wantName: "ars-collector-darwin-arm64"},
		{goos: "linux", goarch: "amd64", wantName: "ars-collector-linux-amd64"},
		{goos: "linux", goarch: "arm64", wantName: "ars-collector-linux-arm64"},
		{goos: "darwin", goarch: "amd64", wantErr: true},
		{goos: "linux", goarch: "386", wantErr: true},
		{goos: "windows", goarch: "amd64", wantErr: true},
		{goos: "Linux", goarch: "x86_64", wantErr: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.goos+"_"+test.goarch, func(t *testing.T) {
			t.Parallel()
			name, err := collectorAssetName(test.goos, test.goarch)
			if (err != nil) != test.wantErr {
				t.Fatalf("collectorAssetName(%q, %q) error = %v, wantErr %v", test.goos, test.goarch, err, test.wantErr)
			}
			if name != test.wantName {
				t.Fatalf("collectorAssetName(%q, %q) = %q, want %q", test.goos, test.goarch, name, test.wantName)
			}
		})
	}
}

func TestEmbeddedCollectorAssetsLoadGeneratedTargets(t *testing.T) {
	entries, err := collectorFiles.ReadDir("generated")
	if err != nil {
		t.Fatal(err)
	}
	generated := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "ars-collector-") {
			generated++
		}
	}
	if generated == 0 {
		t.Skip("collector assets have not been generated")
	}
	if generated != 3 {
		t.Fatalf("embedded generated collectors = %d, want 3", generated)
	}
	for target := range collectorAssetNames {
		data, err := (EmbeddedCollectorAssets{}).ForTarget(target[0], target[1])
		if err != nil {
			t.Fatalf("ForTarget(%q, %q) error = %v", target[0], target[1], err)
		}
		if len(data) == 0 {
			t.Fatalf("ForTarget(%q, %q) returned an empty asset", target[0], target[1])
		}
	}
}

func TestCollectMapsUnameAndUsesDedicatedSSHOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		probe      string
		wantTarget [2]string
	}{
		{name: "darwin", probe: "Darwin\narm64\n", wantTarget: [2]string{"darwin", "arm64"}},
		{name: "linux_x86_64", probe: "Linux\nx86_64\n", wantTarget: [2]string{"linux", "amd64"}},
		{name: "linux_amd64", probe: "Linux\namd64\n", wantTarget: [2]string{"linux", "amd64"}},
		{name: "linux_aarch64", probe: "Linux\naarch64\n", wantTarget: [2]string{"linux", "arm64"}},
		{name: "linux_arm64", probe: "Linux\narm64\n", wantTarget: [2]string{"linux", "arm64"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assets := &fakeAssets{data: []byte("collector")}
			target := "user@host;printf injected"
			runner := successfulRunner(test.probe)
			if _, _, err := Collect(context.Background(), runner, assets, target, CollectOptions{}); err != nil {
				t.Fatalf("Collect() error = %v", err)
			}
			if !reflect.DeepEqual(assets.requests, [][2]string{test.wantTarget}) {
				t.Fatalf("asset requests = %#v, want %#v", assets.requests, [][2]string{test.wantTarget})
			}
			if len(runner.calls) != 2 {
				t.Fatalf("runner calls = %d, want 2", len(runner.calls))
			}
			for _, call := range runner.calls {
				if call.name != "ssh" {
					t.Errorf("command = %q, want ssh", call.name)
				}
				assertCollectionSSHArgs(t, call.args, target, 5)
			}
		})
	}
}

func TestCollectWrapsEachRemoteCommandForBinSh(t *testing.T) {
	t.Parallel()

	target := "host"
	runner := successfulRunner("Linux\namd64\n")
	if _, _, err := Collect(context.Background(), runner, &fakeAssets{data: []byte("collector")}, target, CollectOptions{}); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("runner calls = %d, want probe and collector", len(runner.calls))
	}
	for _, call := range runner.calls {
		assertCollectionSSHArgs(t, call.args, target, 5)
		command := call.args[len(call.args)-1]
		if !strings.HasPrefix(command, "/bin/sh -c '") || !strings.HasSuffix(command, "'") {
			t.Errorf("remote command is not one /bin/sh wrapper: %q", command)
		}
	}
}

func TestCollectRemoteLifecycle(t *testing.T) {
	t.Parallel()

	assets := &fakeAssets{data: []byte{0, 1, 2, 3, 255}}
	var nonce string
	runner := &fakeRunner{run: func(_ context.Context, index int, call runnerCall, stdout, _ io.Writer) error {
		switch index {
		case 0:
			_, _ = io.WriteString(stdout, "Linux\nx86_64\n")
		case 1:
			if !bytes.Equal(call.stdin, assets.data) {
				t.Errorf("collector stdin = %v, want %v", call.stdin, assets.data)
			}
			remoteCommand := call.args[len(call.args)-1]
			nonce = extractNonce(t, remoteCommand)
			if remoteCommand != remoteShellCommand(collectorCommand(nonce)) {
				t.Errorf("collector wrapper = %q, want exact /bin/sh wrapper", remoteCommand)
			}
			assertRemoteLifecycleCommand(t, collectorCommand(nonce), nonce)
			_, _ = fmt.Fprintf(stdout, "/var/tmp/ars-%s\n", nonce)
			writeValidProtocol(t, stdout, nonce)
		default:
			t.Fatalf("unexpected runner call %d", index)
		}
		return nil
	}}

	if _, _, err := Collect(context.Background(), runner, assets, "host", CollectOptions{}); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if matched, _ := regexp.MatchString(`^[0-9a-f]{32}$`, nonce); !matched {
		t.Fatalf("nonce = %q, want 128-bit lowercase hexadecimal", nonce)
	}
}

func TestCollectorCommandNormalizesTrailingSlashTMPDIR(t *testing.T) {
	t.Parallel()

	nonce := strings.Repeat("a", 32)
	tmpdir := filepath.Join(t.TempDir(), "var", "folders", "session", "T")
	if err := os.MkdirAll(tmpdir, 0o755); err != nil {
		t.Fatal(err)
	}
	shells := []string{"/bin/sh"}
	if csh, err := exec.LookPath("csh"); err == nil {
		shells = append(shells, csh)
	}
	for _, shell := range shells {
		command := exec.Command(shell, "-c", remoteShellCommand(collectorCommand(nonce)))
		command.Env = append(os.Environ(), "TMPDIR="+tmpdir+"/")
		command.Stdin = strings.NewReader("#!/bin/sh\nexit 0\n")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("collector bootstrap through %s error = %v: %s", shell, err, output)
		}
		want := filepath.Join(tmpdir, "ars-"+nonce) + "\n"
		if got := string(output); got != want {
			t.Fatalf("temporary path through %s = %q, want normalized %q", shell, got, want)
		}
	}
}

func TestCollectorCommandPreservesRootTMPDIRWithoutDoubleSlash(t *testing.T) {
	t.Parallel()

	nonce := strings.Repeat("c", 32)
	script := collectorCommand(nonce)
	mkdir := strings.Index(script, "if mkdir -- \"$dir\"")
	if mkdir < 0 {
		t.Fatalf("collector bootstrap missing mkdir: %q", script)
	}
	script = script[:mkdir] + "printf '%s\\n' \"$dir\"\n"
	command := exec.Command("/bin/sh", "-c", script)
	command.Env = append(os.Environ(), "TMPDIR=/")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("root path bootstrap error = %v", err)
	}
	if got, want := string(output), "/ars-"+nonce+"\n"; got != want {
		t.Fatalf("root temporary path = %q, want %q", got, want)
	}
}

func TestCollectorCommandCleansUpOnSignalImmediatelyAfterMkdir(t *testing.T) {
	t.Parallel()

	nonce := strings.Repeat("b", 32)
	tmpdir := t.TempDir()
	script := collectorCommand(nonce)
	needle := "if mkdir -- \"$dir\"; then owned=1; "
	if !strings.Contains(script, needle) {
		t.Fatalf("collector bootstrap missing mkdir: %q", script)
	}
	script = strings.Replace(script, needle, "if mkdir -- \"$dir\"; then kill -HUP $$; owned=1; ", 1)
	command := exec.Command("/bin/sh", "-c", script)
	command.Env = append(os.Environ(), "TMPDIR="+tmpdir)
	_ = command.Run()
	directory := filepath.Join(tmpdir, "ars-"+nonce)
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("nonce directory remains after HUP: %s (stat error %v)", directory, err)
	}
}

func TestCollectorCommandCleansUpOnSignalAfterOwnership(t *testing.T) {
	t.Parallel()

	nonce := strings.Repeat("f", 32)
	tmpdir := t.TempDir()
	original := collectorCommand(nonce)
	script := strings.Replace(original, "printf '%s\\n' \"$dir\"; ", "kill -TERM $$; printf '%s\\n' \"$dir\"; ", 1)
	if script == original {
		t.Fatalf("collector bootstrap missing post-ownership boundary: %q", original)
	}
	command := exec.Command("/bin/sh", "-c", script)
	command.Env = append(os.Environ(), "TMPDIR="+tmpdir)
	_ = command.Run()
	directory := filepath.Join(tmpdir, "ars-"+nonce)
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("owned nonce directory remains after TERM: %s (stat error %v)", directory, err)
	}
}

func TestCollectorCommandDoesNotCleanPreExistingDirectory(t *testing.T) {
	t.Parallel()

	nonce := strings.Repeat("d", 32)
	tmpdir := t.TempDir()
	directory := filepath.Join(tmpdir, "ars-"+nonce)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(directory, "collector")
	if err := os.WriteFile(sentinel, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFailingCollectorBootstrap(t, tmpdir, nonce)
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "sentinel" {
		t.Fatalf("pre-existing sentinel changed: data=%q error=%v", data, err)
	}
}

func TestCollectorCommandDoesNotFollowPreExistingSymlink(t *testing.T) {
	t.Parallel()

	nonce := strings.Repeat("e", 32)
	tmpdir := t.TempDir()
	target := t.TempDir()
	sentinel := filepath.Join(target, "collector")
	if err := os.WriteFile(sentinel, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(tmpdir, "ars-"+nonce)); err != nil {
		t.Fatal(err)
	}
	runFailingCollectorBootstrap(t, tmpdir, nonce)
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "sentinel" {
		t.Fatalf("symlink target sentinel changed: data=%q error=%v", data, err)
	}
}

func runFailingCollectorBootstrap(t *testing.T, tmpdir, nonce string) {
	t.Helper()
	command := exec.Command("/bin/sh", "-c", collectorCommand(nonce))
	command.Env = append(os.Environ(), "TMPDIR="+tmpdir)
	command.Stdin = strings.NewReader("untrusted upload")
	if err := command.Run(); err == nil {
		t.Fatal("collector bootstrap succeeded with a pre-existing nonce path")
	}
}

func TestCollectRejectsFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		probe  string
		assets *fakeAssets
		run    func(context.Context, int, runnerCall, io.Writer, io.Writer) error
		want   string
	}{
		{
			name:   "probe failure",
			assets: &fakeAssets{data: []byte("collector")},
			run: func(_ context.Context, index int, _ runnerCall, _, stderr io.Writer) error {
				_, _ = io.WriteString(stderr, strings.Repeat("p", 1<<20))
				return errors.New("probe exit")
			},
			want: "probe",
		},
		{
			name:   "unsupported target",
			probe:  "FreeBSD\namd64\n",
			assets: &fakeAssets{data: []byte("collector")},
			want:   "unsupported",
		},
		{
			name:   "missing asset",
			probe:  "Linux\namd64\n",
			assets: &fakeAssets{err: errors.New("asset absent")},
			want:   "asset",
		},
		{
			name:   "upload failure",
			probe:  "Linux\namd64\n",
			assets: &fakeAssets{data: []byte("collector")},
			run: func(_ context.Context, index int, call runnerCall, stdout, _ io.Writer) error {
				if index == 0 {
					_, _ = io.WriteString(stdout, "Linux\namd64\n")
					return nil
				}
				nonce := extractNonce(t, call.args[len(call.args)-1])
				_, _ = fmt.Fprintf(stdout, "/tmp/ars-%s\n", nonce)
				return errors.New("upload exit")
			},
			want: "collector",
		},
		{
			name:   "invalid temporary path",
			probe:  "Linux\namd64\n",
			assets: &fakeAssets{data: []byte("collector")},
			run: func(_ context.Context, index int, call runnerCall, stdout, _ io.Writer) error {
				if index == 0 {
					_, _ = io.WriteString(stdout, "Linux\namd64\n")
					return nil
				}
				nonce := extractNonce(t, call.args[len(call.args)-1])
				_, _ = io.WriteString(stdout, "/tmp/not-ars\n")
				writeValidProtocol(t, stdout, nonce)
				return nil
			},
			want: "temporary path",
		},
		{
			name:   "stdout above limit",
			probe:  "Linux\namd64\n",
			assets: &fakeAssets{data: []byte("collector")},
			run: func(_ context.Context, index int, _ runnerCall, stdout, _ io.Writer) error {
				if index == 0 {
					_, _ = io.WriteString(stdout, "Linux\namd64\n")
					return nil
				}
				_, _ = io.WriteString(stdout, strings.Repeat("x", (16<<20)+1))
				return nil
			},
			want: "stdout",
		},
		{
			name:   "protocol failure",
			probe:  "Linux\namd64\n",
			assets: &fakeAssets{data: []byte("collector")},
			run: func(_ context.Context, index int, call runnerCall, stdout, _ io.Writer) error {
				if index == 0 {
					_, _ = io.WriteString(stdout, "Linux\namd64\n")
					return nil
				}
				nonce := extractNonce(t, call.args[len(call.args)-1])
				_, _ = fmt.Fprintf(stdout, "/tmp/ars-%s\nnot ARS\n", nonce)
				return nil
			},
			want: "protocol",
		},
		{
			name:   "non-zero remote exit",
			probe:  "Linux\namd64\n",
			assets: &fakeAssets{data: []byte("collector")},
			run: func(_ context.Context, index int, call runnerCall, stdout, _ io.Writer) error {
				if index == 0 {
					_, _ = io.WriteString(stdout, "Linux\namd64\n")
					return nil
				}
				nonce := extractNonce(t, call.args[len(call.args)-1])
				_, _ = fmt.Fprintf(stdout, "/tmp/ars-%s\n", nonce)
				writeValidProtocol(t, stdout, nonce)
				return errors.New("remote exit 1")
			},
			want: "collector",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeRunner{run: test.run}
			if test.run == nil {
				runner = successfulRunner(test.probe)
			}
			_, _, err := Collect(context.Background(), runner, test.assets, "host", CollectOptions{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Collect() error = %v, want containing %q", err, test.want)
			}
			if len(err.Error()) > (64<<10)+1024 {
				t.Fatalf("error length = %d, stderr was not bounded", len(err.Error()))
			}
		})
	}
}

func TestCollectSanitizesUntrustedDiagnostics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		wantEscape bool
		run        func(context.Context, int, runnerCall, io.Writer, io.Writer) error
	}{
		{
			name: "uname stdout",
			run: func(_ context.Context, _ int, _ runnerCall, stdout, _ io.Writer) error {
				_, _ = io.WriteString(stdout, "FreeBSD\x1b]0;SECRET-PATH\x07\namd64\n")
				return nil
			},
		},
		{
			name:       "probe stderr and error",
			wantEscape: true,
			run: func(_ context.Context, _ int, _ runnerCall, _, stderr io.Writer) error {
				_, _ = io.WriteString(stderr, "\x1b]0;owned\x07first\nsecond\t\x00")
				return errors.New("probe\nerror\x1b")
			},
		},
		{
			name:       "combined diagnostic bound",
			wantEscape: true,
			run: func(_ context.Context, _ int, _ runnerCall, _, stderr io.Writer) error {
				_, _ = io.WriteString(stderr, "\x1b\n"+strings.Repeat("s", 1<<20))
				return errors.New("\x1b\n" + strings.Repeat("e", 1<<20))
			},
		},
		{
			name:       "collector stderr and error",
			wantEscape: true,
			run: func(_ context.Context, index int, call runnerCall, stdout, stderr io.Writer) error {
				if index == 0 {
					_, _ = io.WriteString(stdout, "Linux\namd64\n")
					return nil
				}
				nonce := extractNonce(t, call.args[len(call.args)-1])
				_, _ = fmt.Fprintf(stdout, "/tmp/ars-%s\n", nonce)
				_, _ = io.WriteString(stderr, "remote\x1b[31m failure\r\n\x1b]8;;file:///secret\x07path\x1b]8;;\x07")
				return errors.New("remote\nexit")
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeRunner{run: test.run}
			_, _, err := Collect(context.Background(), runner, &fakeAssets{data: []byte("collector")}, "host", CollectOptions{})
			if err == nil {
				t.Fatal("Collect() error = nil")
			}
			message := err.Error()
			assertSafeASCII(t, message)
			if strings.Contains(message, "SECRET-PATH") {
				t.Fatalf("uname metadata leaked in error: %q", message)
			}
			if test.wantEscape && (!strings.Contains(message, `\x1b`) || !strings.Contains(message, `\n`)) {
				t.Fatalf("terminal controls were not visibly escaped: %q", message)
			}
			if len(message) > stderrOutputLimit {
				t.Fatalf("sanitized error length = %d, want bounded", len(message))
			}
		})
	}
}

func TestCollectAppliesSixtySecondHostDeadline(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{run: func(ctx context.Context, index int, call runnerCall, stdout, _ io.Writer) error {
		if index == 0 {
			_, _ = io.WriteString(stdout, "Linux\namd64\n")
			return nil
		}
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("collector context has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining < 59*time.Second || remaining > 60*time.Second {
			t.Errorf("collector deadline remaining = %s, want about 60s", remaining)
		}
		nonce := extractNonce(t, call.args[len(call.args)-1])
		_, _ = fmt.Fprintf(stdout, "/tmp/ars-%s\n", nonce)
		writeValidProtocol(t, stdout, nonce)
		return nil
	}}
	if _, _, err := Collect(context.Background(), runner, &fakeAssets{data: []byte("collector")}, "host", CollectOptions{}); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
}

func TestCollectCancellationAttemptsBoundedExactCleanup(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	runner := &fakeRunner{run: func(callCtx context.Context, index int, call runnerCall, stdout, _ io.Writer) error {
		switch index {
		case 0:
			_, _ = io.WriteString(stdout, "Linux\namd64\n")
			return nil
		case 1:
			nonce := extractNonce(t, call.args[len(call.args)-1])
			_, _ = fmt.Fprintf(stdout, "/tmp/custom/ars-%s\n", nonce)
			cancel()
			<-callCtx.Done()
			return callCtx.Err()
		case 2:
			deadline, ok := callCtx.Deadline()
			if !ok {
				t.Fatal("cleanup context has no deadline")
			}
			remaining := time.Until(deadline)
			if remaining < 4*time.Second || remaining > 5*time.Second {
				t.Errorf("cleanup deadline remaining = %s, want about 5s", remaining)
			}
			command := call.args[len(call.args)-1]
			nonce := extractPathNonce(t, command)
			path := "/tmp/custom/ars-" + nonce
			want := remoteShellCommand("rm -f -- " + singleQuote(path+"/collector") + "; rmdir -- " + singleQuote(path))
			if command != want {
				t.Errorf("cleanup command is not exact: %q", command)
			}
			if strings.Contains(command, "*") || strings.Contains(command, "rm -r") {
				t.Errorf("cleanup command is broad: %q", command)
			}
			return nil
		default:
			t.Fatalf("unexpected runner call %d", index)
			return nil
		}
	}}

	_, _, err := Collect(ctx, runner, &fakeAssets{data: []byte("collector")}, "host", CollectOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Collect() error = %v, want context.Canceled", err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("runner calls = %d, want probe, collector, cleanup", len(runner.calls))
	}
}

func TestCollectHostTimeoutAttemptsBoundedExactCleanup(t *testing.T) {
	t.Parallel()

	const hostTimeout = 25 * time.Millisecond
	processExit := errors.New("process exit")
	var temporaryPath string
	runner := &fakeRunner{run: func(callCtx context.Context, index int, call runnerCall, stdout, _ io.Writer) error {
		switch index {
		case 0:
			_, _ = io.WriteString(stdout, "Linux\namd64\n")
			return nil
		case 1:
			nonce := extractNonce(t, call.args[len(call.args)-1])
			temporaryPath = "/tmp/ars-" + nonce
			_, _ = fmt.Fprintln(stdout, temporaryPath)
			<-callCtx.Done()
			return processExit
		case 2:
			deadline, ok := callCtx.Deadline()
			if !ok {
				t.Fatal("cleanup context has no deadline")
			}
			remaining := time.Until(deadline)
			if remaining < 4*time.Second || remaining > cleanupTimeout {
				t.Errorf("cleanup deadline remaining = %s, want about 5s", remaining)
			}
			wantScript := "rm -f -- " + singleQuote(temporaryPath+"/collector") + "; rmdir -- " + singleQuote(temporaryPath)
			if got, want := call.args[len(call.args)-1], remoteShellCommand(wantScript); got != want {
				t.Errorf("cleanup command = %q, want exact wrapped %q", got, want)
			}
			return nil
		default:
			t.Fatalf("unexpected runner call %d", index)
			return nil
		}
	}}

	started := time.Now()
	_, _, err := Collect(context.Background(), runner, &fakeAssets{data: []byte("collector")}, "host", CollectOptions{HostTimeout: hostTimeout})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Collect() error = %v, want context.DeadlineExceeded", err)
	}
	if !errors.Is(err, processExit) {
		t.Fatalf("Collect() error = %v, want underlying process error", err)
	}
	if elapsed := time.Since(started); elapsed < hostTimeout || elapsed > time.Second {
		t.Fatalf("Collect() elapsed = %s, want deadline-bound failure", elapsed)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("runner calls = %d, want probe, timed-out collector, cleanup", len(runner.calls))
	}
}

func TestCollectRejectsOptionsAboveHardBounds(t *testing.T) {
	t.Parallel()

	defaultLimits := protocol.DefaultLimits()
	tests := []CollectOptions{
		{ConnectTimeout: defaultConnectTimeout + time.Second},
		{HostTimeout: defaultHostTimeout + time.Second},
		{ProtocolLimits: protocol.Limits{StartupBytes: defaultLimits.StartupBytes + 1, LineBytes: defaultLimits.LineBytes, TotalBytes: defaultLimits.TotalBytes, Sessions: defaultLimits.Sessions}},
		{ProtocolLimits: protocol.Limits{StartupBytes: defaultLimits.StartupBytes, LineBytes: defaultLimits.LineBytes + 1, TotalBytes: defaultLimits.TotalBytes, Sessions: defaultLimits.Sessions}},
		{ProtocolLimits: protocol.Limits{StartupBytes: defaultLimits.StartupBytes, LineBytes: defaultLimits.LineBytes, TotalBytes: defaultLimits.TotalBytes + 1, Sessions: defaultLimits.Sessions}},
		{ProtocolLimits: protocol.Limits{StartupBytes: defaultLimits.StartupBytes, LineBytes: defaultLimits.LineBytes, TotalBytes: defaultLimits.TotalBytes, Sessions: defaultLimits.Sessions + 1}},
	}
	for _, options := range tests {
		runner := &fakeRunner{}
		_, _, err := Collect(context.Background(), runner, &fakeAssets{}, "host", options)
		if err == nil || !strings.Contains(err.Error(), "hard limit") {
			t.Errorf("Collect(%+v) error = %v, want hard limit", options, err)
		}
		if len(runner.calls) != 0 {
			t.Errorf("Collect(%+v) made %d SSH calls", options, len(runner.calls))
		}
	}
}

func TestSystemRunnerDoesNotInvokeLocalShell(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	err := (SystemRunner{}).Run(context.Background(), "/usr/bin/printf", []string{"%s", "literal; printf injected"}, nil, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), "literal; printf injected"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func successfulRunner(probe string) *fakeRunner {
	return &fakeRunner{run: func(_ context.Context, index int, call runnerCall, stdout, _ io.Writer) error {
		if index == 0 {
			_, _ = io.WriteString(stdout, probe)
			return nil
		}
		nonceMatch := regexp.MustCompile(`([0-9a-f]{32})`).FindStringSubmatch(call.args[len(call.args)-1])
		if len(nonceMatch) != 2 {
			return errors.New("missing nonce")
		}
		_, _ = fmt.Fprintf(stdout, "/tmp/ars-%s\n", nonceMatch[1])
		return protocol.Encode(stdout, nonceMatch[1], nil, emptyResults())
	}}
}

func assertCollectionSSHArgs(t *testing.T, args []string, target string, connectSeconds int) {
	t.Helper()
	wantOptions := []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ForwardAgent=no",
		"-o", "ForwardX11=no",
		"-o", "ClearAllForwardings=yes",
		"-o", "ConnectionAttempts=1",
		"-o", fmt.Sprintf("ConnectTimeout=%d", connectSeconds),
		"-o", "StrictHostKeyChecking=yes",
		"--", target,
	}
	if len(args) != len(wantOptions)+1 {
		t.Fatalf("ssh args = %#v, want fixed options, target, and one remote command", args)
	}
	if !reflect.DeepEqual(args[:len(wantOptions)], wantOptions) {
		t.Fatalf("ssh args prefix = %#v, want %#v", args[:len(wantOptions)], wantOptions)
	}
	targetCount := 0
	for _, arg := range args {
		if arg == target {
			targetCount++
		}
	}
	if targetCount != 1 {
		t.Fatalf("target argv elements = %d, want exactly one in %#v", targetCount, args)
	}
}

func assertRemoteLifecycleCommand(t *testing.T, command, nonce string) {
	t.Helper()
	required := []string{
		"umask 077",
		"base=${TMPDIR:-/tmp}",
		"bin=\"$dir/collector\"",
		"trap cleanup EXIT",
		"owned=0; interrupted=0",
		"trap on_signal HUP INT TERM",
		"then owned=1; if [ \"$interrupted\" = 1 ]",
		"mkdir -- \"$dir\"",
		"cat > \"$bin\"",
		"chmod 700 \"$bin\"",
		"\"$bin\" \"$nonce\"",
		"rm -f -- \"$bin\"",
		"rmdir -- \"$dir\"",
	}
	for _, value := range required {
		if !strings.Contains(command, value) {
			t.Errorf("remote command missing %q:\n%s", value, command)
		}
	}
	if directory, binary, exitTrap, signalTrap, mkdir := strings.Index(command, "then dir="), strings.Index(command, "bin=\"$dir/collector\""), strings.Index(command, "trap cleanup"), strings.Index(command, "trap on_signal"), strings.Index(command, "mkdir --"); directory < 0 || binary < 0 || exitTrap < 0 || signalTrap < 0 || mkdir < 0 || directory > mkdir || binary > mkdir || exitTrap > mkdir || signalTrap > mkdir {
		t.Errorf("exact paths and traps must be established before mkdir:\n%s", command)
	}
	if strings.Contains(command, "created=") {
		t.Errorf("collector bootstrap uses a cleanup race flag:\n%s", command)
	}
	if strings.Contains(command, "rm -r") || strings.Contains(command, "rm -f -- *") || strings.Contains(command, "rmdir -- *") {
		t.Errorf("remote command contains broad cleanup:\n%s", command)
	}
}

func extractNonce(t *testing.T, command string) string {
	t.Helper()
	match := regexp.MustCompile(`([0-9a-f]{32})`).FindStringSubmatch(command)
	if len(match) != 2 {
		t.Fatalf("remote command nonce missing: %q", command)
	}
	return match[1]
}

func extractPathNonce(t *testing.T, command string) string {
	t.Helper()
	match := regexp.MustCompile(`/ars-([0-9a-f]{32})`).FindStringSubmatch(command)
	if len(match) != 2 {
		t.Fatalf("cleanup path nonce missing: %q", command)
	}
	return match[1]
}

func writeValidProtocol(t *testing.T, output io.Writer, nonce string) {
	t.Helper()
	if err := protocol.Encode(output, nonce, nil, emptyResults()); err != nil {
		t.Fatalf("protocol.Encode() error = %v", err)
	}
}

func emptyResults() []provider.Result {
	return []provider.Result{
		{Provider: session.Claude, Status: provider.Absent},
		{Provider: session.Codex, Status: provider.Absent},
	}
}

func assertSafeASCII(t *testing.T, value string) {
	t.Helper()
	for index := range len(value) {
		if value[index] < 0x20 || value[index] > 0x7e {
			t.Fatalf("diagnostic contains unsafe byte 0x%02x at %d: %q", value[index], index, value)
		}
	}
}
