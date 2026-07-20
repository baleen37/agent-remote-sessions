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

	"github.com/baleen37/agent-remote-sessions/internal/app"
	"github.com/baleen37/agent-remote-sessions/internal/output"
	"github.com/baleen37/agent-remote-sessions/internal/protocol"
	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/runtime"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	arsSSH "github.com/baleen37/agent-remote-sessions/internal/ssh"
)

const (
	e2eClaudeID = "11111111-1111-1111-1111-111111111111"
	e2eCodexID  = "22222222-2222-2222-2222-222222222222"
	e2eSecret   = "RAW_TRANSCRIPT_MUST_NOT_CROSS_BOUNDARY"
)

func TestEndToEndListAndResumeThroughSSHBoundary(t *testing.T) {
	remoteHome := writeSyntheticProviderHomes(t)
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	inventory := filepath.Join(configHome, "ars", "hosts")
	if err := os.MkdirAll(filepath.Dir(inventory), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inventory, []byte("healthy\ndown\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("list JSON keeps healthy sessions beside an unreachable peer", func(t *testing.T) {
		runner := &e2eRunner{remoteHome: remoteHome, collectorAsset: []byte("synthetic collector")}
		var stdout, stderr bytes.Buffer
		dependencies := e2eDependencies(runner, &stdout, &stderr)

		if code := app.Run(context.Background(), []string{"list", "--json"}, dependencies); code != 0 {
			t.Fatalf("Run() = %d, want 0; stderr = %q", code, stderr.String())
		}
		var document e2eDocument
		if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
			t.Fatalf("decode JSON output: %v; output = %q", err, stdout.String())
		}
		if document.SchemaVersion != 1 {
			t.Fatalf("schema_version = %d, want 1", document.SchemaVersion)
		}
		wantSessions := []e2eSession{
			{Host: "healthy", Provider: "codex", NativeID: e2eCodexID, UpdatedAt: "2026-07-19T02:00:00Z", CWD: "/work/codex"},
			{Host: "healthy", Provider: "claude", NativeID: e2eClaudeID, UpdatedAt: "2026-07-19T01:00:00Z", CWD: "/work/claude", Title: "Synthetic Claude task"},
		}
		if !slices.Equal(document.Sessions, wantSessions) {
			t.Fatalf("sessions = %#v, want %#v", document.Sessions, wantSessions)
		}
		if len(document.Hosts) != 2 || document.Hosts[0] != (e2eHost{Target: "healthy", Status: "ok"}) ||
			document.Hosts[1] != (e2eHost{Target: "down", Status: "error"}) {
			t.Fatalf("hosts = %#v, want healthy and unreachable results", document.Hosts)
		}
		if len(document.Errors) != 1 || document.Errors[0] != (e2eHostError{Host: "down", Code: "ssh_failed", Message: "SSH collection failed"}) {
			t.Fatalf("errors = %#v, want structured unreachable-host error", document.Errors)
		}
		if strings.Contains(stdout.String(), e2eSecret) || strings.Contains(stdout.String(), remoteHome) {
			t.Fatalf("public JSON leaked raw content or provider source path: %q", stdout.String())
		}
		if runner.uploadCount() != 1 {
			t.Fatalf("collector uploads = %d, want one healthy-host upload", runner.uploadCount())
		}
	})

	t.Run("opaque fzf index resumes the selected native session", func(t *testing.T) {
		runner := &e2eRunner{
			remoteHome:     remoteHome,
			collectorAsset: []byte("synthetic collector"),
			fzfSelection:   "1\n",
		}
		var stdout, stderr bytes.Buffer
		dependencies := e2eDependencies(runner, &stdout, &stderr)

		if code := app.Run(context.Background(), nil, dependencies); code != 0 {
			t.Fatalf("Run() = %d, want 0; stderr = %q", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "down: SSH collection failed (ssh_failed)") {
			t.Fatalf("stderr = %q, want partial-host diagnostic", stderr.String())
		}
		fzfInput, resumeArgs := runner.observations()
		rows := strings.Split(strings.TrimSuffix(fzfInput, "\n"), "\n")
		if len(rows) != 2 || !strings.HasPrefix(rows[0], "0\t") || !strings.HasPrefix(rows[1], "1\t") {
			t.Fatalf("fzf input = %q, want two opaque indexed rows", fzfInput)
		}
		wantResume := []string{
			"-tt",
			"healthy",
			"cd '/work/claude' && exec claude --resume '" + e2eClaudeID + "'",
		}
		if !slices.Equal(resumeArgs, wantResume) {
			t.Fatalf("resume argv = %#v, want %#v", resumeArgs, wantResume)
		}
		if strings.Contains(fzfInput, e2eSecret) || strings.Contains(fzfInput, remoteHome) {
			t.Fatalf("fzf input leaked raw content or provider source path: %q", fzfInput)
		}
	})
}

func e2eDependencies(runner *e2eRunner, stdout, stderr io.Writer) app.Dependencies {
	assets := e2eAssets{data: runner.collectorAsset}
	return app.Dependencies{
		LoadHosts: app.Load,
		Collect: func(ctx context.Context, hosts []app.Host) app.Result {
			return app.CollectHosts(ctx, hosts, 4, func(ctx context.Context, host app.Host) ([]session.Discovered, []provider.Result, runtime.Report, error) {
				return arsSSH.Collect(ctx, runner, assets, host.Target, arsSSH.CollectOptions{
					ConnectTimeout: 5 * time.Second,
					HostTimeout:    60 * time.Second,
					ProtocolLimits: protocol.DefaultLimits(),
				})
			})
		},
		Pick: (output.FZF{Runner: runner}).Select,
		Resume: func(ctx context.Context, item session.Session) error {
			adapter, ok := provider.Lookup(item.Provider)
			if !ok {
				return fmt.Errorf("unsupported provider")
			}
			return arsSSH.Resume(ctx, runner, item.Host, item, adapter)
		},
		Stdout: stdout,
		Stderr: stderr,
	}
}

func writeSyntheticProviderHomes(t *testing.T) string {
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

	claudePath := filepath.Join(home, ".claude", "projects", "synthetic", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(claudePath), 0o700); err != nil {
		t.Fatal(err)
	}
	claudeHistory := strings.Join([]string{
		`{"type":"user","sessionId":"` + e2eClaudeID + `","cwd":"/work/claude","message":{"content":"` + e2eSecret + `"}}`,
		`{"type":"ai-title","sessionId":"` + e2eClaudeID + `","title":"Synthetic Claude task"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(claudePath, []byte(claudeHistory), 0o600); err != nil {
		t.Fatal(err)
	}

	codexPath := filepath.Join(home, ".codex", "sessions", "2026", "07", "19", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(codexPath), 0o700); err != nil {
		t.Fatal(err)
	}
	codexHistory := strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"` + e2eCodexID + `","cwd":"/work/codex","source":"cli","thread_source":"user"}}`,
		`{"type":"response_item","payload":{"content":"` + e2eSecret + `"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(codexPath, []byte(codexHistory), 0o600); err != nil {
		t.Fatal(err)
	}
	claudeTime := time.Date(2026, 7, 19, 1, 0, 0, 0, time.UTC)
	codexTime := claudeTime.Add(time.Hour)
	if err := os.Chtimes(claudePath, claudeTime, claudeTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(codexPath, codexTime, codexTime); err != nil {
		t.Fatal(err)
	}
	return home
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
	fzfSelection   string
	fzfInput       string
	resumeArgs     []string
	uploads        int
}

var e2eNoncePattern = regexp.MustCompile(`([0-9a-f]{32})`)

func (runner *e2eRunner) Run(ctx context.Context, name string, args []string, stdin io.Reader, stdout, _ io.Writer) error {
	switch name {
	case "fzf":
		input, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		runner.mu.Lock()
		runner.fzfInput = string(input)
		selection := runner.fzfSelection
		runner.mu.Unlock()
		_, err = io.WriteString(stdout, selection)
		return err
	case "ssh":
		if len(args) >= 3 && args[0] == "-tt" {
			runner.mu.Lock()
			runner.resumeArgs = append([]string(nil), args...)
			runner.mu.Unlock()
			return nil
		}
	default:
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
	for i, candidate := range candidates {
		discovered[i] = session.Discovered{Candidate: candidate, Runtime: session.Runtime{State: session.RuntimeSaved}}
	}
	if _, err := fmt.Fprintf(stdout, "/tmp/ars-%s\n", nonce); err != nil {
		return err
	}
	if err := protocol.Encode(stdout, nonce, discovered, results, runtime.Report{Status: runtime.StatusOK}); err != nil {
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

func (runner *e2eRunner) observations() (string, []string) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.fzfInput, append([]string(nil), runner.resumeArgs...)
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
