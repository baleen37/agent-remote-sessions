package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/protocol"
	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/runtime"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

const collectorNonce = "0123456789abcdef0123456789abcdef"

func TestRunRequiresHexadecimal128BitNonce(t *testing.T) {
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

func TestRunEmitsPartialProviderSummaries(t *testing.T) {
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
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if strings.Contains(stderr.String(), invalid.CWD) {
		t.Fatalf("stderr exposed provider path: %q", stderr.String())
	}
}

func TestRunReturnsNonZeroWhenEncodingFails(t *testing.T) {
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

func (adapter *fakeAdapter) Name() session.Provider { return adapter.name }

func (adapter *fakeAdapter) Discover(_ context.Context, home string) provider.Result {
	adapter.calls++
	adapter.home = home
	return adapter.result
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
