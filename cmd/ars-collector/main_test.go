package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/protocol"
	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/runtime"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

const collectorNonce = "0123456789abcdef0123456789abcdef"

// isolateCollectorTmuxSocket points run()'s real runtime.SystemRunner at a
// private, almost certainly nonexistent tmux socket, so these tests exercise
// discovery/encoding without depending on (or polluting) the shared ars-v1
// server. A missing server still resolves to a clean Report{Status: StatusOK}
// with every candidate defaulting to RuntimeSaved, which is what these tests
// expect.
func isolateCollectorTmuxSocket(t *testing.T) {
	t.Helper()
	t.Setenv("ARS_TMUX_SOCKET", "ars-test-"+strconv.Itoa(os.Getpid())+"-"+t.Name())
}

func TestRunRequiresHexadecimal128BitNonce(t *testing.T) {
	isolateCollectorTmuxSocket(t)
	adapters := emptyAdapters()
	for _, args := range [][]string{nil, {"not-hex"}, {"abcd"}, {collectorNonce, "extra"}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, "/remote/home", adapters, &stdout, &stderr); code == 0 {
			t.Fatalf("run(%q) code = 0, want non-zero", args)
		}
		if stdout.Len() != 0 {
			t.Fatalf("run(%q) stdout = %q, want empty", args, stdout.String())
		}
	}
}

func TestRunDiscoversBothProvidersAndSortsSessions(t *testing.T) {
	isolateCollectorTmuxSocket(t)
	claudeFirst := validCollectorCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")
	claudeSecond := validCollectorCandidate(session.Claude, "33333333-3333-3333-3333-333333333333")
	codex := validCollectorCandidate(session.Codex, "22222222-2222-2222-2222-222222222222")
	claudeAdapter := &fakeAdapter{name: session.Claude, result: provider.Result{
		Provider: session.Claude, Sessions: []session.Candidate{claudeSecond, claudeFirst}, Status: provider.OK, Seen: 2,
	}}
	codexAdapter := &fakeAdapter{name: session.Codex, result: provider.Result{
		Provider: session.Codex, Sessions: []session.Candidate{codex}, Status: provider.OK, Seen: 1,
	}}

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{collectorNonce}, "/remote/home", []provider.Adapter{codexAdapter, claudeAdapter}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("run() stderr = %q, want empty", stderr.String())
	}
	if claudeAdapter.calls != 1 || codexAdapter.calls != 1 || claudeAdapter.home != "/remote/home" || codexAdapter.home != "/remote/home" {
		t.Fatalf("adapter calls = claude(%d, %q) codex(%d, %q), want one call each with remote home",
			claudeAdapter.calls, claudeAdapter.home, codexAdapter.calls, codexAdapter.home)
	}

	discovered, results, _, err := protocol.Decode(bytes.NewReader(stdout.Bytes()), collectorNonce, protocol.DefaultLimits())
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	wantDiscovered := []session.Discovered{
		{Candidate: claudeFirst, Runtime: session.Runtime{State: session.RuntimeSaved}},
		{Candidate: claudeSecond, Runtime: session.Runtime{State: session.RuntimeSaved}},
		{Candidate: codex, Runtime: session.Runtime{State: session.RuntimeSaved}},
	}
	if !reflect.DeepEqual(discovered, wantDiscovered) {
		t.Fatalf("discovered = %#v, want %#v", discovered, wantDiscovered)
	}
	if len(results) != 2 || results[0].Provider != session.Claude || results[1].Provider != session.Codex {
		t.Fatalf("results = %#v, want Claude then Codex summaries", results)
	}
}

func TestRunWritesRecentSnapshotBeforeFullDiscoveryCompletes(t *testing.T) {
	isolateCollectorTmuxSocket(t)
	recentCandidate := validCollectorCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")
	releaseComplete := make(chan struct{})
	adapters := []provider.Adapter{
		&blockingAdapter{
			name: session.Claude,
			recent: provider.Result{
				Provider: session.Claude,
				Sessions: []session.Candidate{recentCandidate},
				Status:   provider.OK,
				Seen:     1,
			},
			complete: provider.Result{
				Provider: session.Claude,
				Sessions: []session.Candidate{recentCandidate},
				Status:   provider.OK,
				Seen:     1,
			},
			releaseComplete: releaseComplete,
		},
		&fakeAdapter{name: session.Codex, result: provider.Result{Provider: session.Codex, Status: provider.Absent}},
	}

	reader, writer := io.Pipe()
	defer func() {
		select {
		case <-releaseComplete:
		default:
			close(releaseComplete)
		}
		_ = reader.Close()
	}()
	runDone := make(chan int, 1)
	go func() {
		runDone <- run(context.Background(), []string{collectorNonce}, "/remote/home", adapters, writer, io.Discard)
		_ = writer.Close()
	}()

	recent := make(chan protocol.Snapshot, 1)
	decodeDone := make(chan error, 1)
	go func() {
		decodeDone <- protocol.DecodeStream(reader, collectorNonce, protocol.DefaultLimits(), func(snapshot protocol.Snapshot) error {
			if snapshot.Phase == provider.PhaseRecent {
				recent <- snapshot
			}
			return nil
		})
	}()

	select {
	case snapshot := <-recent:
		if !reflect.DeepEqual(snapshot.Discovered, []session.Discovered{{
			Candidate: recentCandidate,
			Runtime:   session.Runtime{State: session.RuntimeSaved},
		}}) {
			t.Fatalf("recent discovered = %#v", snapshot.Discovered)
		}
	case <-time.After(time.Second):
		t.Fatal("recent snapshot was not written before complete discovery was released")
	}

	close(releaseComplete)
	select {
	case code := <-runDone:
		if code != 0 {
			t.Fatalf("run() code = %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("run() did not finish after complete discovery was released")
	}
	select {
	case err := <-decodeDone:
		if err != nil {
			t.Fatalf("DecodeStream() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("DecodeStream() did not finish after complete discovery was released")
	}
}

func TestRunEmitsPartialProviderSummaries(t *testing.T) {
	isolateCollectorTmuxSocket(t)
	candidate := validCollectorCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")
	adapters := []provider.Adapter{
		&fakeAdapter{name: session.Claude, result: provider.Result{
			Provider: session.Claude, Sessions: []session.Candidate{candidate}, Status: provider.Partial,
			Seen: 3, Skipped: 2, ErrorCode: "corrupt",
		}},
		&fakeAdapter{name: session.Codex, result: provider.Result{
			Provider: session.Codex, Status: provider.Error, Seen: 1, Skipped: 1, ErrorCode: "unavailable",
		}},
	}

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{collectorNonce}, "/remote/home", adapters, &stdout, &stderr); code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	_, results, _, err := protocol.Decode(bytes.NewReader(stdout.Bytes()), collectorNonce, protocol.DefaultLimits())
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(results) != 2 || results[0].Status != provider.Partial || results[0].ErrorCode != "corrupt" ||
		results[1].Status != provider.Error || results[1].ErrorCode != "unavailable" {
		t.Fatalf("results = %#v, want partial Claude and failed Codex summaries", results)
	}
	if got := stderr.String(); !strings.Contains(got, "claude: partial (corrupt)") || !strings.Contains(got, "codex: error (unavailable)") {
		t.Fatalf("stderr = %q, want sanitized provider diagnostics", got)
	}
}

func TestRunRejectsInvalidCandidateBeforeEncoding(t *testing.T) {
	isolateCollectorTmuxSocket(t)
	invalid := validCollectorCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")
	invalid.CWD = "relative/provider/path"
	adapters := []provider.Adapter{
		&fakeAdapter{name: session.Claude, result: provider.Result{Provider: session.Claude, Sessions: []session.Candidate{invalid}, Status: provider.OK, Seen: 1}},
		&fakeAdapter{name: session.Codex, result: provider.Result{Provider: session.Codex, Status: provider.Absent}},
	}

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{collectorNonce}, "/remote/home", adapters, &stdout, &stderr); code == 0 {
		t.Fatal("run() code = 0, want non-zero")
	}
	if _, _, _, err := protocol.Decode(&stdout, collectorNonce, protocol.DefaultLimits()); err == nil {
		t.Fatal("Decode() error = nil, want incomplete stream rejection")
	}
	if strings.Contains(stdout.String(), invalid.CWD) || strings.Contains(stderr.String(), invalid.CWD) {
		t.Fatalf("collector output exposed provider path: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunReturnsNonZeroWhenEncodingFails(t *testing.T) {
	isolateCollectorTmuxSocket(t)
	var stderr bytes.Buffer
	code := run(context.Background(), []string{collectorNonce}, "/remote/home", emptyAdapters(), errorWriter{}, &stderr)
	if code == 0 {
		t.Fatal("run() code = 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "encode failed") {
		t.Fatalf("stderr = %q, want generic encode diagnostic", stderr.String())
	}
}

func TestRunEncodesSessionsWhenRuntimeIsUnavailable(t *testing.T) {
	candidate := validCollectorCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")
	adapters := []provider.Adapter{
		&fakeAdapter{name: session.Claude, result: provider.Result{Provider: session.Claude, Sessions: []session.Candidate{candidate}, Status: provider.OK, Seen: 1}},
		&fakeAdapter{name: session.Codex, result: provider.Result{Provider: session.Codex, Status: provider.Absent}},
	}
	var stdout, stderr bytes.Buffer
	code := runWithRuntime(context.Background(), []string{collectorNonce}, "/remote/home", adapters, unavailableRuntimeRunner{}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runWithRuntime() = %d, stderr = %q", code, stderr.String())
	}
	discovered, _, report, err := protocol.Decode(&stdout, collectorNonce, protocol.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(discovered) != 1 || discovered[0].Runtime.State != session.RuntimeSaved || report != (runtime.Report{Status: runtime.StatusUnavailable, ErrorCode: "tmux_unavailable"}) {
		t.Fatalf("decoded = %#v %#v", discovered, report)
	}
}

type fakeAdapter struct {
	name   session.Provider
	result provider.Result
	calls  int
	home   string
}

type blockingAdapter struct {
	name            session.Provider
	recent          provider.Result
	complete        provider.Result
	releaseComplete <-chan struct{}
}

func (adapter *blockingAdapter) Name() session.Provider { return adapter.name }

func (adapter *blockingAdapter) Discover(context.Context, string) provider.Result {
	return adapter.complete
}

func (adapter *blockingAdapter) DiscoverStream(
	ctx context.Context,
	_ string,
	_ time.Time,
	emit func(provider.Phase, provider.Result) error,
) error {
	if err := emit(provider.PhaseRecent, adapter.recent); err != nil {
		return err
	}
	select {
	case <-adapter.releaseComplete:
	case <-ctx.Done():
		return ctx.Err()
	}
	return emit(provider.PhaseComplete, adapter.complete)
}

func (adapter *blockingAdapter) ValidateID(string) error { return nil }

func (adapter *blockingAdapter) Resume(string) (provider.ResumeSpec, error) {
	return provider.ResumeSpec{}, nil
}

func (adapter *fakeAdapter) Name() session.Provider { return adapter.name }

func (adapter *fakeAdapter) Discover(_ context.Context, home string) provider.Result {
	adapter.calls++
	adapter.home = home
	return adapter.result
}

func (adapter *fakeAdapter) DiscoverStream(
	_ context.Context,
	home string,
	_ time.Time,
	emit func(provider.Phase, provider.Result) error,
) error {
	adapter.calls++
	adapter.home = home
	if err := emit(provider.PhaseRecent, provider.Result{Provider: adapter.name}); err != nil {
		return err
	}
	return emit(provider.PhaseComplete, adapter.result)
}

func (adapter *fakeAdapter) ValidateID(string) error { return nil }

func (adapter *fakeAdapter) Resume(string) (provider.ResumeSpec, error) {
	return provider.ResumeSpec{}, nil
}

func emptyAdapters() []provider.Adapter {
	return []provider.Adapter{
		&fakeAdapter{name: session.Claude, result: provider.Result{Provider: session.Claude, Status: provider.Absent}},
		&fakeAdapter{name: session.Codex, result: provider.Result{Provider: session.Codex, Status: provider.Absent}},
	}
}

func validCollectorCandidate(name session.Provider, id string) session.Candidate {
	return session.Candidate{
		Provider:  name,
		NativeID:  id,
		UpdatedAt: time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC),
		CWD:       "/synthetic/collector",
		Title:     "Collector synthetic title",
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errors.New("synthetic write failure") }

var _ io.Writer = errorWriter{}

type unavailableRuntimeRunner struct{}

func (unavailableRuntimeRunner) Output(context.Context, runtime.Command) ([]byte, error) {
	return nil, exec.ErrNotFound
}

func (unavailableRuntimeRunner) Run(context.Context, runtime.Command, io.Reader, io.Writer, io.Writer) error {
	return nil
}
