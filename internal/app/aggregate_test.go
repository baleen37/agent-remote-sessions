package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/output"
	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/runtime"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func TestCollectHostsKeepsSessionsBesideRuntimeWarning(t *testing.T) {
	for _, report := range []runtime.Report{
		{Status: runtime.StatusUnavailable, ErrorCode: "tmux_unavailable"},
		{Status: runtime.StatusFailed, ErrorCode: "tmux_failed"},
	} {
		t.Run(string(report.Status), func(t *testing.T) {
			collector := func(context.Context, Host) ([]session.Discovered, []provider.Result, runtime.Report, error) {
				return []session.Discovered{{
					Candidate: aggregateCandidate(session.Claude, "123e4567-e89b-42d3-a456-426614174000", time.Unix(10, 0).UTC()),
					Runtime:   session.Runtime{State: session.RuntimeSaved},
				}}, nil, report, nil
			}
			got := CollectHosts(context.Background(), []Host{{Target: "macbook", Local: true}}, 1, collector)
			if len(got.Sessions) != 1 || got.Hosts[0].Status != output.HostOK || len(got.Warnings) != 1 || got.Warnings[0].Code != report.ErrorCode {
				t.Fatalf("result = %#v", got)
			}
		})
	}
}

func TestCollectHostsKeepsSessionsBesideProviderWarnings(t *testing.T) {
	candidate := aggregateCandidate(session.Claude, "123e4567-e89b-42d3-a456-426614174000", time.Unix(10, 0).UTC())
	collector := func(context.Context, Host) ([]session.Discovered, []provider.Result, runtime.Report, error) {
		return aggregateDiscovered(candidate), []provider.Result{
			{
				Provider: session.Claude, Status: provider.Partial,
				Sessions: []session.Candidate{candidate}, Seen: 2, Skipped: 1, ErrorCode: "corrupt",
			},
			{Provider: session.Codex, Status: provider.Error, Seen: 1, Skipped: 1, ErrorCode: "unavailable"},
		}, runtime.Report{Status: runtime.StatusOK}, nil
	}

	got := CollectHosts(context.Background(), []Host{{Target: "macbook", Local: true}}, 1, collector)
	wantWarnings := []output.HostError{
		{Host: "macbook", Code: "corrupt", Message: "Claude discovery partial"},
		{Host: "macbook", Code: "unavailable", Message: "Codex discovery failed"},
	}
	if len(got.Sessions) != 1 || got.Hosts[0].Status != output.HostOK || len(got.Errors) != 0 {
		t.Fatalf("result = %#v, want healthy session and host", got)
	}
	if !reflect.DeepEqual(got.Warnings, wantWarnings) {
		t.Fatalf("warnings = %#v, want %#v", got.Warnings, wantWarnings)
	}
}

func TestCollectHostsLimitsConcurrencyAndAttemptsEveryHostOnce(t *testing.T) {
	hosts := make([]Host, 12)
	for index := range hosts {
		hosts[index] = Host{Target: fmt.Sprintf("host-%02d", index)}
	}

	release := make(chan struct{})
	started := make(chan struct{}, len(hosts))
	var active atomic.Int32
	var maximum atomic.Int32
	var mu sync.Mutex
	calls := make(map[string]int)
	collector := func(_ context.Context, host Host) ([]session.Discovered, []provider.Result, runtime.Report, error) {
		mu.Lock()
		calls[host.Target]++
		mu.Unlock()
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		return nil, nil, runtime.Report{Status: runtime.StatusOK}, nil
	}

	done := make(chan Result, 1)
	go func() { done <- CollectHosts(context.Background(), hosts, 4, collector) }()
	for range 4 {
		<-started
	}
	select {
	case <-started:
		t.Fatal("fifth collector started before a worker was released")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	result := <-done

	if got := maximum.Load(); got != 4 {
		t.Fatalf("maximum concurrent collectors = %d, want 4", got)
	}
	for _, host := range hosts {
		if calls[host.Target] != 1 {
			t.Errorf("collector calls for %q = %d, want 1", host.Target, calls[host.Target])
		}
	}
	if len(result.Hosts) != len(hosts) {
		t.Fatalf("host results = %d, want %d", len(result.Hosts), len(hosts))
	}
	for index, host := range hosts {
		want := output.HostResult{Target: host.Target, Status: output.HostOK}
		if result.Hosts[index] != want {
			t.Errorf("host result %d = %#v, want %#v", index, result.Hosts[index], want)
		}
	}
}

func TestCollectHostsPropagatesContextAndClassifiesTimeout(t *testing.T) {
	type contextKey string
	const key contextKey = "request"
	deadline := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(context.WithValue(context.Background(), key, "value"), deadline)
	defer cancel()

	result := CollectHosts(ctx, []Host{{Target: "slow-host"}}, 4,
		func(got context.Context, _ Host) ([]session.Discovered, []provider.Result, runtime.Report, error) {
			if got.Value(key) != "value" {
				t.Error("collector context lost request value")
			}
			gotDeadline, ok := got.Deadline()
			if !ok || !gotDeadline.Equal(deadline) {
				t.Errorf("collector deadline = (%v, %v), want %v", gotDeadline, ok, deadline)
			}
			return nil, nil, runtime.Report{}, fmt.Errorf("remote detail must not leak: %w", context.DeadlineExceeded)
		})

	wantHosts := []output.HostResult{{Target: "slow-host", Status: output.HostStatusError}}
	if !reflect.DeepEqual(result.Hosts, wantHosts) {
		t.Fatalf("hosts = %#v, want %#v", result.Hosts, wantHosts)
	}
	wantErrors := []output.HostError{{Host: "slow-host", Code: "ssh_timeout", Message: "SSH collection timed out"}}
	if !reflect.DeepEqual(result.Errors, wantErrors) {
		t.Fatalf("errors = %#v, want %#v", result.Errors, wantErrors)
	}
}

func TestCollectHostsKeepsHealthyEmptyPartialAndPeerSessions(t *testing.T) {
	hosts := []Host{{Target: "empty"}, {Target: "partial"}, {Target: "down"}}
	candidate := aggregateCandidate(session.Claude, "123e4567-e89b-42d3-a456-426614174000", time.Date(2026, 7, 19, 1, 2, 3, 4, time.UTC))

	result := CollectHosts(context.Background(), hosts, 4,
		func(_ context.Context, host Host) ([]session.Discovered, []provider.Result, runtime.Report, error) {
			switch host.Target {
			case "empty":
				return nil, []provider.Result{{Provider: session.Claude, Status: provider.Absent}}, runtime.Report{Status: runtime.StatusOK}, nil
			case "partial":
				return aggregateDiscovered(candidate), []provider.Result{{
					Provider: session.Claude, Status: provider.Partial, Sessions: []session.Candidate{candidate},
					Seen: 2, Skipped: 1, ErrorCode: "corrupt",
				}}, runtime.Report{Status: runtime.StatusOK}, nil
			default:
				return nil, nil, runtime.Report{}, errors.New("dial failed\n/private/raw/transcript")
			}
		})

	wantHosts := []output.HostResult{
		{Target: "empty", Status: output.HostOK},
		{Target: "partial", Status: output.HostOK},
		{Target: "down", Status: output.HostStatusError},
	}
	if !reflect.DeepEqual(result.Hosts, wantHosts) {
		t.Fatalf("hosts = %#v, want %#v", result.Hosts, wantHosts)
	}
	wantSessions := []session.Session{{Host: "partial", Candidate: candidate, Runtime: session.Runtime{State: session.RuntimeSaved}}}
	if !reflect.DeepEqual(result.Sessions, wantSessions) {
		t.Fatalf("sessions = %#v, want %#v", result.Sessions, wantSessions)
	}
	wantErrors := []output.HostError{{Host: "down", Code: "ssh_failed", Message: "SSH collection failed"}}
	if !reflect.DeepEqual(result.Errors, wantErrors) {
		t.Fatalf("errors = %#v, want %#v", result.Errors, wantErrors)
	}
}

func TestCollectHostsReportsAllHostFailure(t *testing.T) {
	hosts := []Host{{Target: "one"}, {Target: "two"}}
	result := CollectHosts(context.Background(), hosts, 4,
		func(_ context.Context, _ Host) ([]session.Discovered, []provider.Result, runtime.Report, error) {
			return nil, nil, runtime.Report{}, errors.New("connection refused")
		})

	wantHosts := []output.HostResult{
		{Target: "one", Status: output.HostStatusError},
		{Target: "two", Status: output.HostStatusError},
	}
	if !reflect.DeepEqual(result.Hosts, wantHosts) {
		t.Fatalf("hosts = %#v, want %#v", result.Hosts, wantHosts)
	}
	if len(result.Sessions) != 0 || len(result.Errors) != 2 {
		t.Fatalf("result = %#v, want no sessions and two errors", result)
	}
}

func TestCollectHostsDeduplicatesAndSortsSessionsDeterministically(t *testing.T) {
	newest := time.Date(2026, 7, 19, 3, 0, 0, 1, time.UTC)
	tied := time.Date(2026, 7, 19, 2, 0, 0, 0, time.UTC)
	oldest := time.Date(2026, 7, 19, 1, 0, 0, 0, time.UTC)
	idA := "123e4567-e89b-42d3-a456-426614174000"
	idB := "123e4567-e89b-42d3-a456-426614174001"
	idC := "0195f5dc-9e3f-7c26-8000-0123456789ab"
	hosts := []Host{{Target: "z-host"}, {Target: "a-host"}}

	result := CollectHosts(context.Background(), hosts, 4,
		func(_ context.Context, host Host) ([]session.Discovered, []provider.Result, runtime.Report, error) {
			if host.Target == "z-host" {
				olderDuplicate := aggregateCandidate(session.Claude, idA, oldest)
				newerDuplicate := aggregateCandidate(session.Claude, idA, newest)
				return aggregateDiscovered(
					aggregateCandidate(session.Claude, idB, tied),
					olderDuplicate,
					newerDuplicate,
				), nil, runtime.Report{Status: runtime.StatusOK}, nil
			}
			return aggregateDiscovered(
				aggregateCandidate(session.Codex, idC, tied),
				aggregateCandidate(session.Claude, idA, tied),
			), nil, runtime.Report{Status: runtime.StatusOK}, nil
		})

	want := []session.Session{
		{Host: "z-host", Candidate: aggregateCandidate(session.Claude, idA, newest), Runtime: session.Runtime{State: session.RuntimeSaved}},
		{Host: "a-host", Candidate: aggregateCandidate(session.Claude, idA, tied), Runtime: session.Runtime{State: session.RuntimeSaved}},
		{Host: "a-host", Candidate: aggregateCandidate(session.Codex, idC, tied), Runtime: session.Runtime{State: session.RuntimeSaved}},
		{Host: "z-host", Candidate: aggregateCandidate(session.Claude, idB, tied), Runtime: session.Runtime{State: session.RuntimeSaved}},
	}
	if !reflect.DeepEqual(result.Sessions, want) {
		t.Fatalf("sessions =\n%#v\nwant\n%#v", result.Sessions, want)
	}
}

func TestCollectHostsRejectsInvalidCollectorOutputAndNormalizesErrors(t *testing.T) {
	tests := []struct {
		name      string
		collector Collector
		wantCode  string
		wantError string
	}{
		{
			name: "invalid candidate is protocol error",
			collector: func(context.Context, Host) ([]session.Discovered, []provider.Result, runtime.Report, error) {
				return []session.Discovered{{Candidate: session.Candidate{Provider: "unknown"}}}, nil, runtime.Report{Status: runtime.StatusOK}, nil
			},
			wantCode: "protocol_error", wantError: "Collector protocol failed",
		},
		{
			name: "joined host deadline",
			collector: func(context.Context, Host) ([]session.Discovered, []provider.Result, runtime.Report, error) {
				return nil, nil, runtime.Report{}, errors.Join(errors.New("process exit"), context.DeadlineExceeded)
			},
			wantCode: "ssh_timeout", wantError: "SSH collection timed out",
		},
		{
			name: "unsupported target",
			collector: func(context.Context, Host) ([]session.Discovered, []provider.Result, runtime.Report, error) {
				return nil, nil, runtime.Report{}, errors.New("unsupported SSH target FreeBSD/raw")
			},
			wantCode: "unsupported_target", wantError: "SSH target is unsupported",
		},
		{
			name: "resource limit",
			collector: func(context.Context, Host) ([]session.Discovered, []provider.Result, runtime.Report, error) {
				return nil, nil, runtime.Report{}, errors.New("collector stdout exceeds limit: /secret/source")
			},
			wantCode: "resource_limit", wantError: "Collector resource limit exceeded",
		},
		{
			name: "protocol decode",
			collector: func(context.Context, Host) ([]session.Discovered, []provider.Result, runtime.Report, error) {
				return nil, nil, runtime.Report{}, errors.New("collector protocol: malformed /private/transcript")
			},
			wantCode: "protocol_error", wantError: "Collector protocol failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := CollectHosts(context.Background(), []Host{{Target: "host"}}, 4, test.collector)
			want := []output.HostError{{Host: "host", Code: test.wantCode, Message: test.wantError}}
			if !reflect.DeepEqual(result.Errors, want) {
				t.Fatalf("errors = %#v, want %#v", result.Errors, want)
			}
			if len(result.Sessions) != 0 || result.Hosts[0].Status != output.HostStatusError {
				t.Fatalf("result = %#v, want failed host without sessions", result)
			}
		})
	}
}

func aggregateCandidate(providerName session.Provider, id string, updatedAt time.Time) session.Candidate {
	return session.Candidate{
		Provider: providerName, NativeID: id, UpdatedAt: updatedAt,
		CWD: "/work/project", Title: "Fix login",
	}
}

func aggregateDiscovered(candidates ...session.Candidate) []session.Discovered {
	discovered := make([]session.Discovered, len(candidates))
	for i, candidate := range candidates {
		discovered[i] = session.Discovered{Candidate: candidate, Runtime: session.Runtime{State: session.RuntimeSaved}}
	}
	return discovered
}
