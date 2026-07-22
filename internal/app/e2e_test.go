package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/app"
	"github.com/baleen37/agent-remote-sessions/internal/protocol"
	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/runtime"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	arsSSH "github.com/baleen37/agent-remote-sessions/internal/ssh"
	"github.com/baleen37/agent-remote-sessions/internal/tui"
)

const (
	e2eLocalClaudeID  = "11111111-1111-1111-1111-111111111111"
	e2eLocalRunningID = "33333333-3333-3333-3333-333333333333"
	e2eRemoteCodexID  = "22222222-2222-2222-2222-222222222222"
	e2eSecret         = "RAW_TRANSCRIPT_MUST_NOT_CROSS_BOUNDARY"
)

func TestEndToEndRoutesCommonTopologyThroughInteractiveAndJSONModes(t *testing.T) {
	localHome := writeSyntheticClaudeHome(t)
	remoteHome := writeSyntheticCodexHome(t)
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	writeTopology(t, configHome)

	t.Run("interactive routes canonical local and remote sessions and refreshes after attach", func(t *testing.T) {
		harness := newE2EHarness(localHome, remoteHome)
		var stdout, stderr bytes.Buffer
		harness.dependencies.Stdout = &stdout
		harness.dependencies.Stderr = &stderr
		started := false
		harness.dependencies.RunInteractive = func(ctx context.Context, hosts []app.Host) error {
			started = true
			if want := []app.Host{{Target: "macbook", Local: true}, {Target: "healthy"}, {Target: "down"}}; !slices.Equal(hosts, want) {
				t.Fatalf("hosts = %#v, want %#v", hosts, want)
			}

			result := harness.collect(ctx, hosts)
			assertCanonicalResult(t, result)
			tuiResult := tui.Result{
				Hosts: result.Hosts, Sessions: result.Sessions, Errors: result.Errors, Warnings: result.Warnings,
			}
			if len(tuiResult.Sessions) != 3 || len(tuiResult.Warnings) != 1 || tuiResult.Warnings[0].Host != "healthy" {
				t.Fatalf("TUI sessions = %#v", tuiResult.Sessions)
			}

			remoteCommand, err := harness.attach(ctx, hosts, findE2ESession(t, tuiResult.Sessions, e2eRemoteCodexID))
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := remoteCommand.(*arsSSH.AttachCommand); !ok {
				t.Fatalf("remote command = %T", remoteCommand)
			}
			localCommand, err := harness.attach(ctx, hosts, findE2ESession(t, tuiResult.Sessions, e2eLocalClaudeID))
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := localCommand.(*runtime.AttachCommand); !ok {
				t.Fatalf("local command = %T", localCommand)
			}
			localCommand.SetStdin(strings.NewReader(""))
			localCommand.SetStdout(io.Discard)
			localCommand.SetStderr(io.Discard)
			if err := localCommand.Run(); err != nil {
				t.Fatal(err)
			}

			refreshed := harness.collect(ctx, hosts)
			assertCanonicalResult(t, refreshed)
			return nil
		}

		if code := app.Run(context.Background(), nil, harness.dependencies); code != 0 {
			t.Fatalf("Run() = %d, want 0; stderr = %q", code, stderr.String())
		}
		if !started || harness.collections != 2 || harness.sshRunner.uploadCount() != 2 {
			t.Fatalf("started/collections/uploads = %v/%d/%d", started, harness.collections, harness.sshRunner.uploadCount())
		}
		if want := []string{"has-session", "bind-key", "attach-session"}; !slices.Equal(harness.runtimeRunner.attachCommands(), want) {
			t.Fatalf("local attach commands = %v, want %v", harness.runtimeRunner.attachCommands(), want)
		}
		if harness.sshRunner.sawUnexpectedCommand() {
			t.Fatalf("unexpected command names = %v", harness.sshRunner.commandNames())
		}
	})

	t.Run("JSON collects topology once and never starts interactive mode", func(t *testing.T) {
		harness := newE2EHarness(localHome, remoteHome)
		var stdout, stderr bytes.Buffer
		harness.dependencies.Stdout = &stdout
		harness.dependencies.Stderr = &stderr
		harness.dependencies.RunInteractive = func(context.Context, []app.Host) error {
			t.Fatal("interactive mode started")
			return nil
		}

		if code := app.Run(context.Background(), []string{"list", "--json"}, harness.dependencies); code != 0 {
			t.Fatalf("Run() = %d, want 0; stderr = %q", code, stderr.String())
		}
		var document e2eDocument
		if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
			t.Fatalf("decode JSON output: %v; output = %q", err, stdout.String())
		}
		if document.SchemaVersion != 1 || harness.collections != 1 || harness.sshRunner.uploadCount() != 1 {
			t.Fatalf("schema/collections/uploads = %d/%d/%d", document.SchemaVersion, harness.collections, harness.sshRunner.uploadCount())
		}
		assertJSONV1Shape(t, stdout.Bytes())
		wantSessions := []e2eSession{
			{Host: "healthy", Provider: "codex", NativeID: e2eRemoteCodexID, UpdatedAt: "2026-07-19T02:00:00Z", CWD: "/work/remote"},
			{Host: "macbook", Provider: "claude", NativeID: e2eLocalRunningID, UpdatedAt: "2026-07-19T01:30:00Z", CWD: "/work/running", Title: "Running task"},
			{Host: "macbook", Provider: "claude", NativeID: e2eLocalClaudeID, UpdatedAt: "2026-07-19T01:00:00Z", CWD: "/work/local", Title: "Local task"},
		}
		if !slices.Equal(document.Sessions, wantSessions) {
			t.Fatalf("sessions = %#v, want %#v", document.Sessions, wantSessions)
		}
		if strings.Contains(stdout.String(), e2eSecret) || strings.Contains(stdout.String(), localHome) || strings.Contains(stdout.String(), remoteHome) ||
			strings.Contains(stdout.String(), "runtime_state") || strings.Contains(stdout.String(), "tmux_unavailable") {
			t.Fatalf("public JSON leaked raw content or provider source path: %q", stdout.String())
		}
		if harness.sshRunner.sawUnexpectedCommand() {
			t.Fatalf("unexpected command names = %v", harness.sshRunner.commandNames())
		}
	})
}

type e2eHarness struct {
	dependencies  app.Dependencies
	collect       func(context.Context, []app.Host) app.Result
	attach        func(context.Context, []app.Host, session.Session) (tea.ExecCommand, error)
	sshRunner     *e2eRunner
	runtimeRunner *e2eRuntimeRunner
	collections   int
}

func newE2EHarness(localHome, remoteHome string) *e2eHarness {
	harness := &e2eHarness{
		sshRunner:     &e2eRunner{remoteHome: remoteHome, collectorAsset: []byte("synthetic collector")},
		runtimeRunner: &e2eRuntimeRunner{},
	}
	assets := e2eAssets{data: harness.sshRunner.collectorAsset}
	collector := func(ctx context.Context, host app.Host) ([]session.Discovered, []provider.Result, runtime.Report, error) {
		if host.Local {
			candidates, results, err := provider.DiscoverAll(ctx, localHome, provider.Builtin())
			if err != nil {
				return nil, nil, runtime.Report{}, err
			}
			states, report := runtime.Inspect(ctx, harness.runtimeRunner, candidates)
			return combineE2ERuntime(candidates, states), results, report, nil
		}
		return arsSSH.Collect(ctx, harness.sshRunner, assets, host.Target, arsSSH.CollectOptions{
			ConnectTimeout: 5 * time.Second,
			HostTimeout:    60 * time.Second,
			ProtocolLimits: protocol.DefaultLimits(),
		})
	}
	harness.collect = func(ctx context.Context, hosts []app.Host) app.Result {
		harness.collections++
		return app.CollectHosts(ctx, hosts, 4, collector)
	}
	harness.attach = func(ctx context.Context, hosts []app.Host, item session.Session) (tea.ExecCommand, error) {
		host, ok := findHost(hosts, item.Host)
		if !ok {
			return nil, fmt.Errorf("session host is not selected")
		}
		adapter, ok := provider.Lookup(item.Provider)
		if !ok {
			return nil, fmt.Errorf("unsupported session provider")
		}
		spec, err := adapter.Resume(item.NativeID)
		if err != nil {
			return nil, err
		}
		if host.Local {
			return runtime.NewAttachCommand(ctx, harness.runtimeRunner, item, spec)
		}
		return arsSSH.NewAttachCommand(ctx, host.Target, item, spec)
	}
	harness.dependencies = app.Dependencies{
		LoadTopology: app.LoadTopology,
		Collect:      harness.collect,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	}
	return harness
}

func findHost(hosts []app.Host, target string) (app.Host, bool) {
	for _, host := range hosts {
		if host.Target == target {
			return host, true
		}
	}
	return app.Host{}, false
}

func combineE2ERuntime(candidates []session.Candidate, states map[string]session.Runtime) []session.Discovered {
	discovered := make([]session.Discovered, len(candidates))
	for index, candidate := range candidates {
		state := states[runtime.Key(string(candidate.Provider), candidate.NativeID)]
		discovered[index] = session.Discovered{Candidate: candidate, Runtime: state}
	}
	return discovered
}

func assertCanonicalResult(t *testing.T, result app.Result) {
	t.Helper()
	if len(result.Hosts) != 3 || result.Hosts[0].Target != "macbook" || result.Hosts[0].Status != "ok" ||
		result.Hosts[1].Target != "healthy" || result.Hosts[1].Status != "ok" ||
		result.Hosts[2].Target != "down" || result.Hosts[2].Status != "error" {
		t.Fatalf("hosts = %#v", result.Hosts)
	}
	if len(result.Sessions) != 3 ||
		findE2ESession(t, result.Sessions, e2eRemoteCodexID).Runtime.State != session.RuntimeSaved ||
		findE2ESession(t, result.Sessions, e2eLocalRunningID).Runtime.State != session.RuntimeRunning ||
		findE2ESession(t, result.Sessions, e2eLocalClaudeID).Runtime.State != session.RuntimeAttached {
		t.Fatalf("sessions = %#v", result.Sessions)
	}
	if len(result.Errors) != 1 || result.Errors[0].Host != "down" || result.Errors[0].Code != "ssh_failed" {
		t.Fatalf("errors = %#v", result.Errors)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Host != "healthy" || result.Warnings[0].Code != "tmux_unavailable" {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
}

func findE2ESession(t *testing.T, sessions []session.Session, nativeID string) session.Session {
	t.Helper()
	for _, item := range sessions {
		if item.NativeID == nativeID {
			return item
		}
	}
	t.Fatalf("session %s not found in %#v", nativeID, sessions)
	return session.Session{}
}

func assertJSONV1Shape(t *testing.T, data []byte) {
	t.Helper()
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if got := sortedJSONKeys(document); !slices.Equal(got, []string{"errors", "hosts", "schema_version", "sessions"}) {
		t.Fatalf("public JSON keys = %v", got)
	}
	var sessions []map[string]json.RawMessage
	if err := json.Unmarshal(document["sessions"], &sessions); err != nil {
		t.Fatal(err)
	}
	want := []string{"cwd", "host", "native_id", "provider", "title", "updated_at"}
	for _, item := range sessions {
		if got := sortedJSONKeys(item); !slices.Equal(got, want) {
			t.Fatalf("public JSON session keys = %v, want %v", got, want)
		}
	}
}

func sortedJSONKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func writeTopology(t *testing.T, configHome string) {
	t.Helper()
	directory := filepath.Join(configHome, "ars")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "hosts"), []byte("macbook\nhealthy\ndown\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "local-host"), []byte("macbook\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeSyntheticClaudeHome(t *testing.T) string {
	t.Helper()
	home := writeProviderExecutables(t)
	path := filepath.Join(home, ".claude", "projects", "synthetic", "session.jsonl")
	writeHistory(t, path, strings.Join([]string{
		`{"type":"user","sessionId":"` + e2eLocalClaudeID + `","cwd":"/work/local","message":{"content":"` + e2eSecret + `"}}`,
		`{"type":"ai-title","sessionId":"` + e2eLocalClaudeID + `","title":"Local task"}`,
	}, "\n")+"\n", time.Date(2026, 7, 19, 1, 0, 0, 0, time.UTC))
	runningPath := filepath.Join(home, ".claude", "projects", "synthetic", "running.jsonl")
	writeHistory(t, runningPath, strings.Join([]string{
		`{"type":"user","sessionId":"` + e2eLocalRunningID + `","cwd":"/work/running","message":{"content":"` + e2eSecret + `"}}`,
		`{"type":"ai-title","sessionId":"` + e2eLocalRunningID + `","title":"Running task"}`,
	}, "\n")+"\n", time.Date(2026, 7, 19, 1, 30, 0, 0, time.UTC))
	return home
}

func writeSyntheticCodexHome(t *testing.T) string {
	t.Helper()
	home := writeProviderExecutables(t)
	path := filepath.Join(home, ".codex", "sessions", "2026", "07", "19", "session.jsonl")
	writeHistory(t, path, strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"` + e2eRemoteCodexID + `","cwd":"/work/remote","source":"cli","thread_source":"user"}}`,
		`{"type":"response_item","payload":{"content":"` + e2eSecret + `"}}`,
	}, "\n")+"\n", time.Date(2026, 7, 19, 2, 0, 0, 0, time.UTC))
	return home
}

func writeProviderExecutables(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return home
}

func writeHistory(t *testing.T, path, contents string, updatedAt time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, updatedAt, updatedAt); err != nil {
		t.Fatal(err)
	}
}

type e2eAssets struct{ data []byte }

func (assets e2eAssets) ForTarget(goos, goarch string) ([]byte, error) {
	if goos != "linux" || goarch != "amd64" {
		return nil, fmt.Errorf("unexpected target %s/%s", goos, goarch)
	}
	return append([]byte(nil), assets.data...), nil
}

type e2eRunner struct {
	mu             sync.Mutex
	remoteHome     string
	collectorAsset []byte
	commands       []string
	uploads        int
}

var e2eNoncePattern = regexp.MustCompile(`([0-9a-f]{32})`)

func (runner *e2eRunner) Run(ctx context.Context, name string, args []string, stdin io.Reader, stdout, _ io.Writer) error {
	runner.mu.Lock()
	runner.commands = append(runner.commands, name)
	runner.mu.Unlock()
	if name != "ssh" {
		return fmt.Errorf("unexpected command %q", name)
	}

	target := collectionTarget(args)
	if target == "down" {
		return errors.New("dial refused")
	}
	if target != "healthy" {
		return fmt.Errorf("unexpected SSH target %q", target)
	}
	remoteCommand := args[len(args)-1]
	if strings.Contains(remoteCommand, "uname -s; uname -m") {
		_, err := io.WriteString(stdout, "Linux\nx86_64\n")
		return err
	}

	upload, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	if !bytes.Equal(upload, runner.collectorAsset) {
		return fmt.Errorf("uploaded collector mismatch")
	}
	match := e2eNoncePattern.FindStringSubmatch(remoteCommand)
	if len(match) != 2 {
		return fmt.Errorf("collector nonce missing")
	}
	nonce := match[1]
	candidates, results, err := provider.DiscoverAll(ctx, runner.remoteHome, provider.Builtin())
	if err != nil {
		return err
	}
	discovered := make([]session.Discovered, len(candidates))
	for index, candidate := range candidates {
		discovered[index] = session.Discovered{Candidate: candidate, Runtime: session.Runtime{State: session.RuntimeSaved}}
	}
	var encoded bytes.Buffer
	if _, err := fmt.Fprintf(&encoded, "/tmp/ars-%s\n", nonce); err != nil {
		return err
	}
	if err := protocol.Encode(&encoded, nonce, discovered, results, runtime.Report{
		Status: runtime.StatusUnavailable, ErrorCode: "tmux_unavailable",
	}); err != nil {
		return err
	}
	if strings.Contains(encoded.String(), e2eSecret) || strings.Contains(encoded.String(), runner.remoteHome) {
		return errors.New("private collector protocol leaked transcript content or provider source path")
	}
	if _, err := stdout.Write(encoded.Bytes()); err != nil {
		return err
	}
	runner.mu.Lock()
	runner.uploads++
	runner.mu.Unlock()
	return nil
}

func collectionTarget(args []string) string {
	for index, arg := range args {
		if arg == "--" && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

func (runner *e2eRunner) uploadCount() int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.uploads
}

func (runner *e2eRunner) commandNames() []string {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]string(nil), runner.commands...)
}

func (runner *e2eRunner) sawUnexpectedCommand() bool {
	for _, name := range runner.commandNames() {
		if name != "ssh" {
			return true
		}
	}
	return false
}

type e2eRuntimeRunner struct {
	mu       sync.Mutex
	commands []runtime.Command
}

func (*e2eRuntimeRunner) Output(context.Context, runtime.Command) ([]byte, error) {
	attached := runtime.Key(string(session.Claude), e2eLocalClaudeID)
	running := runtime.Key(string(session.Claude), e2eLocalRunningID)
	return []byte(attached + "\t1\t1752790800\n" + running + "\t0\t1752790800\n"), nil
}

func (runner *e2eRuntimeRunner) Run(_ context.Context, command runtime.Command, _ io.Reader, _, _ io.Writer) error {
	runner.mu.Lock()
	runner.commands = append(runner.commands, command)
	runner.mu.Unlock()
	return nil
}

func (runner *e2eRuntimeRunner) attachCommands() []string {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	commands := make([]string, 0, len(runner.commands))
	for _, command := range runner.commands {
		commands = append(commands, command.Args[4])
	}
	return commands
}

type e2eDocument struct {
	SchemaVersion int            `json:"schema_version"`
	Hosts         []e2eHost      `json:"hosts"`
	Sessions      []e2eSession   `json:"sessions"`
	Errors        []e2eHostError `json:"errors"`
}

type e2eHost struct {
	Target string `json:"target"`
	Status string `json:"status"`
}

type e2eSession struct {
	Host      string `json:"host"`
	Provider  string `json:"provider"`
	NativeID  string `json:"native_id"`
	UpdatedAt string `json:"updated_at"`
	CWD       string `json:"cwd"`
	Title     string `json:"title"`
}

type e2eHostError struct {
	Host    string `json:"host"`
	Code    string `json:"code"`
	Message string `json:"message"`
}
