package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/app"
	"github.com/baleen37/agent-remote-sessions/internal/protocol"
	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/runtime"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/baleen37/agent-remote-sessions/internal/ssh"
	"github.com/baleen37/agent-remote-sessions/internal/tui"
	"github.com/baleen37/agent-remote-sessions/internal/update"
)

func TestRunExplicitUpdatePrintsResult(t *testing.T) {
	tests := []struct {
		name        string
		tag         string
		wantOutput  string
		wantInstall bool
	}{
		{
			name:       "already current",
			tag:        "v1.2.0",
			wantOutput: "ars v1.2.0 is already up to date\n",
		},
		{
			name:        "updated",
			tag:         "v1.3.0",
			wantOutput:  "Updated ars from v1.2.0 to v1.3.0\n",
			wantInstall: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, `{"tag_name":%q}`, test.tag)
			}))
			defer server.Close()

			installed := false
			deps := update.Dependencies{
				CurrentVersion: "1.2.0",
				Client:         server.Client(),
				ReleaseAPI:     server.URL,
				Executable: func() (string, error) {
					return "/usr/local/lib/node_modules/@baleen37/ars/vendor/ars-darwin-arm64", nil
				},
				RunCommand: func(context.Context, string, ...string) error {
					installed = true
					return nil
				},
				CheckTimeout: time.Second,
			}
			var stdout bytes.Buffer
			if err := runExplicitUpdate(context.Background(), deps, &stdout); err != nil {
				t.Fatal(err)
			}
			if stdout.String() != test.wantOutput || installed != test.wantInstall {
				t.Fatalf("stdout/installed = %q/%v, want %q/%v", stdout.String(), installed, test.wantOutput, test.wantInstall)
			}
		})
	}
}

func TestNewCollectorLocalEmitsRecentAndReturnsAuthoritativeFinal(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	home := writeCollectorHome(t, now)
	runtimeRunner := &countingRuntimeRunner{}
	homeCalls := 0
	nowCalls := 0
	collector := newCollector(
		func() (string, error) {
			homeCalls++
			return home, nil
		},
		func() time.Time {
			nowCalls++
			return now
		},
		runtimeRunner,
		nil,
		nil,
		ssh.CollectOptions{},
	)

	var early [][]session.Discovered
	final, results, report, err := collector(
		context.Background(),
		app.Host{Target: app.LocalhostTarget, Local: true},
		func(discovered []session.Discovered) error {
			early = append(early, append([]session.Discovered(nil), discovered...))
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(early) != 1 {
		t.Fatalf("early callback count = %d, want 1", len(early))
	}
	if got, want := discoveredIDs(early[0]), []string{"11111111-1111-1111-1111-111111111111"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("early IDs = %v, want recent-only %v", got, want)
	}
	if got, want := discoveredIDs(final), []string{
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("final IDs = %v, want authoritative %v", got, want)
	}
	if len(results) != 2 || report.Status != runtime.StatusOK {
		t.Fatalf("early/results/report = %#v/%#v/%#v", early, results, report)
	}
	if homeCalls != 1 || nowCalls != 1 || runtimeRunner.outputs != 2 {
		t.Fatalf("home/now/runtime calls = %d/%d/%d, want 1/1/2", homeCalls, nowCalls, runtimeRunner.outputs)
	}
}

func TestNewCollectorRemoteEmitsRecentAndReturnsAuthoritativeFinal(t *testing.T) {
	recent, complete := collectorSnapshots()
	sshRunner := &streamingCollectorRunner{recent: recent, complete: complete}
	collector := newCollector(
		func() (string, error) {
			t.Fatal("remote collection requested local home")
			return "", nil
		},
		func() time.Time {
			t.Fatal("remote collection requested current time")
			return time.Time{}
		},
		nil,
		sshRunner,
		collectorAssets{data: []byte("collector")},
		ssh.CollectOptions{},
	)

	var early [][]session.Discovered
	final, results, report, err := collector(
		context.Background(),
		app.Host{Target: "server"},
		func(discovered []session.Discovered) error {
			early = append(early, append([]session.Discovered(nil), discovered...))
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(early) != 1 || !reflect.DeepEqual(early[0], recent.Discovered) {
		t.Fatalf("early = %#v, want %#v", early, recent.Discovered)
	}
	if !reflect.DeepEqual(final, complete.Discovered) || !reflect.DeepEqual(results, complete.Results) || report != complete.Report {
		t.Fatalf("final = (%#v, %#v, %#v), want authoritative %#v", final, results, report, complete)
	}
	if sshRunner.calls != 2 {
		t.Fatalf("SSH calls = %d, want one probe and one collector", sshRunner.calls)
	}
}

func TestRunTUIRejectsNonTTYStdinBeforeStartingBubbleTea(t *testing.T) {
	stdin, stdout := terminalFiles(t)
	err := runTUI(context.Background(), tui.Dependencies{}, stdin, stdout, func(fd int) bool {
		return fd != int(stdin.Fd())
	})
	if err == nil || err.Error() != "interactive mode requires a TTY; use ars list --json" {
		t.Fatalf("runTUI() error = %v", err)
	}
}

func TestRunTUIRejectsNonTTYStdoutBeforeStartingBubbleTea(t *testing.T) {
	stdin, stdout := terminalFiles(t)
	err := runTUI(context.Background(), tui.Dependencies{}, stdin, stdout, func(fd int) bool {
		return fd != int(stdout.Fd())
	})
	if err == nil || err.Error() != "interactive mode requires a TTY; use ars list --json" {
		t.Fatalf("runTUI() error = %v", err)
	}
}

func terminalFiles(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	stdin, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	return stdin, stdout
}

type countingRuntimeRunner struct {
	outputs int
}

func (runner *countingRuntimeRunner) Output(context.Context, runtime.Command) ([]byte, error) {
	runner.outputs++
	return nil, nil
}

func (*countingRuntimeRunner) Run(context.Context, runtime.Command, io.Reader, io.Writer, io.Writer) error {
	return fmt.Errorf("unexpected runtime command")
}

func writeCollectorHome(t *testing.T, now time.Time) string {
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
	t.Setenv("PATH", bin)

	writeCollectorHistory(
		t,
		filepath.Join(home, ".claude", "projects", "test", "recent.jsonl"),
		"11111111-1111-1111-1111-111111111111",
		"/work/recent",
		"Recent",
		now.Add(-time.Hour),
	)
	writeCollectorHistory(
		t,
		filepath.Join(home, ".claude", "projects", "test", "old.jsonl"),
		"22222222-2222-2222-2222-222222222222",
		"/work/old",
		"Old",
		now.Add(-session.RecentWindow-time.Hour),
	)
	return home
}

func writeCollectorHistory(t *testing.T, path, id, cwd, title string, modified time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(
		"{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q,\"message\":{\"content\":\"private\"}}\n"+
			"{\"type\":\"ai-title\",\"sessionId\":%q,\"title\":%q}\n",
		id, cwd, id, title,
	)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func discoveredIDs(discovered []session.Discovered) []string {
	ids := make([]string, len(discovered))
	for index, item := range discovered {
		ids[index] = item.Candidate.NativeID
	}
	return ids
}

type collectorAssets struct {
	data []byte
}

func (assets collectorAssets) ForTarget(goos, goarch string) ([]byte, error) {
	if goos != "linux" || goarch != "amd64" {
		return nil, fmt.Errorf("unexpected target %s/%s", goos, goarch)
	}
	return append([]byte(nil), assets.data...), nil
}

type streamingCollectorRunner struct {
	recent   protocol.Snapshot
	complete protocol.Snapshot
	calls    int
}

func (runner *streamingCollectorRunner) Run(
	_ context.Context,
	_ string,
	args []string,
	_ io.Reader,
	stdout io.Writer,
	_ io.Writer,
) error {
	index := runner.calls
	runner.calls++
	if index == 0 {
		_, err := io.WriteString(stdout, "Linux\namd64\n")
		return err
	}
	match := regexp.MustCompile(`([0-9a-f]{32})`).FindStringSubmatch(args[len(args)-1])
	if len(match) != 2 {
		return fmt.Errorf("collector nonce missing")
	}
	nonce := match[1]
	if _, err := fmt.Fprintf(stdout, "/tmp/ars-%s\n", nonce); err != nil {
		return err
	}
	encoder, err := protocol.NewStreamEncoder(stdout, nonce, protocol.DefaultLimits())
	if err != nil {
		return err
	}
	if err := encoder.Encode(runner.recent); err != nil {
		return err
	}
	if err := encoder.Encode(runner.complete); err != nil {
		return err
	}
	return encoder.Close()
}

func collectorSnapshots() (protocol.Snapshot, protocol.Snapshot) {
	recentCandidate := session.Candidate{
		Provider: session.Claude, NativeID: "11111111-1111-1111-1111-111111111111",
		UpdatedAt: time.Unix(20, 0).UTC(), CWD: "/work/recent", Title: "Recent",
	}
	finalCandidate := session.Candidate{
		Provider: session.Claude, NativeID: "22222222-2222-2222-2222-222222222222",
		UpdatedAt: time.Unix(10, 0).UTC(), CWD: "/work/final", Title: "Final",
	}
	recent := protocol.Snapshot{
		Phase: provider.PhaseRecent,
		Discovered: []session.Discovered{{
			Candidate: recentCandidate, Runtime: session.Runtime{State: session.RuntimeSaved},
		}},
		Report: runtime.Report{Status: runtime.StatusOK},
	}
	complete := protocol.Snapshot{
		Phase: provider.PhaseComplete,
		Discovered: []session.Discovered{{
			Candidate: finalCandidate, Runtime: session.Runtime{State: session.RuntimeSaved},
		}},
		Results: []provider.Result{
			{Provider: session.Claude, Sessions: []session.Candidate{finalCandidate}, Status: provider.OK, Seen: 1},
			{Provider: session.Codex, Status: provider.Absent},
		},
		Report: runtime.Report{Status: runtime.StatusOK},
	}
	return recent, complete
}
