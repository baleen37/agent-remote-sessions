package app

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/output"
	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/runtime"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func streamSession(host, nativeID, title string) session.Session {
	value, err := session.Bind(host, session.Candidate{
		Provider:  session.Claude,
		NativeID:  nativeID,
		UpdatedAt: cacheSession(host).UpdatedAt,
		CWD:       "/work/stream",
		Title:     title,
	})
	if err != nil {
		panic(err)
	}
	return value
}

func sequentialCollector(sessionsByTarget map[string][]session.Session, errByTarget map[string]error) Collector {
	return func(_ context.Context, host Host, _ func([]session.Discovered) error) ([]session.Discovered, []provider.Result, runtime.Report, error) {
		if err := errByTarget[host.Target]; err != nil {
			return nil, nil, runtime.Report{}, err
		}
		discovered := make([]session.Discovered, 0, len(sessionsByTarget[host.Target]))
		for _, item := range sessionsByTarget[host.Target] {
			discovered = append(discovered, session.Discovered{Candidate: item.Candidate, Runtime: item.Runtime})
		}
		return discovered, nil, runtime.Report{Status: runtime.StatusOK}, nil
	}
}

func TestCollectHostsStreamEmitsCacheThenRecentOverlayThenExhaustiveReplace(t *testing.T) {
	const (
		refreshedID = "123e4567-e89b-42d3-a456-426614174000"
		historyID   = "123e4567-e89b-42d3-a456-426614174001"
		earlyOnlyID = "123e4567-e89b-42d3-a456-426614174002"
		finalOnlyID = "123e4567-e89b-42d3-a456-426614174003"
	)
	host := Host{Target: "server"}
	cachedMatch := streamValue(
		host.Target, refreshedID, time.Unix(10, 0).UTC(), "/cached/cwd", "cached title",
		session.Runtime{State: session.RuntimeSaved},
	)
	cachedHistory := streamValue(
		host.Target, historyID, time.Unix(5, 0).UTC(), "/history", "cached history",
		session.Runtime{State: session.RuntimeSaved},
	)
	recentMatch := streamValue(
		host.Target, refreshedID, time.Unix(30, 0).UTC(), "/recent/cwd", "recent title",
		session.Runtime{State: session.RuntimeAttached, AttachedClients: 2, StartedAt: time.Unix(20, 0).UTC()},
	)
	recentOnly := streamValue(
		host.Target, earlyOnlyID, time.Unix(25, 0).UTC(), "/recent/only", "recent only",
		session.Runtime{State: session.RuntimeSaved},
	)
	finalMatch := streamValue(
		host.Target, refreshedID, time.Unix(40, 0).UTC(), "/final/cwd", "final title",
		session.Runtime{State: session.RuntimeRunning, StartedAt: time.Unix(35, 0).UTC()},
	)
	finalOnly := streamValue(
		host.Target, finalOnlyID, time.Unix(15, 0).UTC(), "/final/only", "final only",
		session.Runtime{State: session.RuntimeSaved},
	)

	saveCalls := 0
	var saved []session.Session
	cache := HostCache{
		Load: func(target string) ([]session.Session, bool) {
			if target != host.Target {
				t.Fatalf("cache target = %q", target)
			}
			return []session.Session{cachedMatch, cachedHistory}, true
		},
		Save: func(target string, sessions []session.Session) {
			if target != host.Target {
				t.Fatalf("cache target = %q", target)
			}
			saveCalls++
			saved = append([]session.Session(nil), sessions...)
		},
	}
	collector := func(
		_ context.Context,
		got Host,
		emitRecent func([]session.Discovered) error,
	) ([]session.Discovered, []provider.Result, runtime.Report, error) {
		if got != host {
			t.Fatalf("host = %#v", got)
		}
		if err := emitRecent(discoveredFromSessions(recentMatch, recentOnly)); err != nil {
			return nil, nil, runtime.Report{}, err
		}
		return discoveredFromSessions(finalMatch, finalOnly), nil, runtime.Report{Status: runtime.StatusOK}, nil
	}

	var snapshots []Snapshot
	CollectHostsStream(context.Background(), []Host{host}, 1, collector, cache, func(snapshot Snapshot) {
		snapshots = append(snapshots, snapshot)
		if len(snapshots) == 2 && saveCalls != 0 {
			t.Fatalf("early snapshot saved cache %d times", saveCalls)
		}
	})

	if len(snapshots) != 3 {
		t.Fatalf("snapshot count = %d, want cache, recent, final: %#v", len(snapshots), snapshots)
	}
	if first := snapshots[0]; first.Done || len(first.Result.Sessions) != 2 {
		t.Fatalf("cache snapshot = %#v", first)
	}
	recent := snapshots[1]
	if recent.Done || !slices.Equal(recent.Loading, []string{"server"}) {
		t.Fatalf("recent state = %#v", recent)
	}
	if got, ok := sessionByID(recent.Result.Sessions, refreshedID); !ok || got != recentMatch {
		t.Fatalf("matching identity after recent = (%#v, %v), want whole value %#v", got, ok, recentMatch)
	}
	if _, ok := sessionByID(recent.Result.Sessions, historyID); !ok {
		t.Fatal("recent overlay erased cached history")
	}
	final := snapshots[2]
	if !final.Done || len(final.Loading) != 0 {
		t.Fatalf("final state = %#v", final)
	}
	if got, ok := sessionByID(final.Result.Sessions, refreshedID); !ok || got != finalMatch {
		t.Fatalf("matching identity after final = (%#v, %v), want %#v", got, ok, finalMatch)
	}
	for _, removedID := range []string{historyID, earlyOnlyID} {
		if _, ok := sessionByID(final.Result.Sessions, removedID); ok {
			t.Fatalf("authoritative final retained removed identity %q", removedID)
		}
	}
	if _, ok := sessionByID(final.Result.Sessions, finalOnlyID); !ok {
		t.Fatal("authoritative final omitted final-only identity")
	}
	if saveCalls != 1 || !slices.Equal(saved, []session.Session{finalMatch, finalOnly}) {
		t.Fatalf("cache saves/value = %d/%#v, want one authoritative save", saveCalls, saved)
	}
}

func TestCollectHostsStreamFinalFailureKeepsCachedAndRecentRowsWithoutSaving(t *testing.T) {
	hosts := []Host{{Target: "server"}}
	cached := streamSession("server", "123e4567-e89b-42d3-a456-426614174000", "cached row")
	recent := streamSession("server", "123e4567-e89b-42d3-a456-426614174001", "recent row")
	saves := 0
	cache := HostCache{
		Load: func(string) ([]session.Session, bool) { return []session.Session{cached}, true },
		Save: func(string, []session.Session) { saves++ },
	}
	collector := func(
		_ context.Context,
		_ Host,
		emitRecent func([]session.Discovered) error,
	) ([]session.Discovered, []provider.Result, runtime.Report, error) {
		if err := emitRecent(discoveredFromSessions(recent)); err != nil {
			return nil, nil, runtime.Report{}, err
		}
		return nil, nil, runtime.Report{}, errors.New("ssh boom")
	}

	var snapshots []Snapshot
	CollectHostsStream(context.Background(), hosts, 1, collector, cache, func(snapshot Snapshot) {
		snapshots = append(snapshots, snapshot)
	})

	if first := snapshots[0]; len(first.Loading) != 1 || first.Loading[0] != "server" {
		t.Fatalf("initial loading = %#v", first.Loading)
	}
	last := snapshots[len(snapshots)-1]
	if !last.Done {
		t.Fatalf("final snapshot not done: %#v", last)
	}
	if len(last.Result.Sessions) != 2 {
		t.Fatalf("cached/recent rows dropped on failure: %#v", last.Result.Sessions)
	}
	if _, ok := sessionByID(last.Result.Sessions, cached.NativeID); !ok {
		t.Fatal("cached row dropped on failure")
	}
	if _, ok := sessionByID(last.Result.Sessions, recent.NativeID); !ok {
		t.Fatal("recent row dropped on failure")
	}
	if len(last.Loading) != 0 {
		t.Fatalf("failed host still loading: %#v", last.Loading)
	}
	if len(last.Result.Errors) != 1 || last.Result.Errors[0].Code != "ssh_failed" {
		t.Fatalf("failure not reported: %#v", last.Result.Errors)
	}
	if len(last.Result.Hosts) != 1 || last.Result.Hosts[0].Status != output.HostStatusError {
		t.Fatalf("host status = %#v", last.Result.Hosts)
	}
	if saves != 0 {
		t.Fatalf("failed collection wrote cache %d times", saves)
	}
}

func TestCollectHostsStreamFinalFailureKeepsRecentRowsWithoutCache(t *testing.T) {
	hosts := []Host{{Target: "server"}}
	recent := streamSession("server", "123e4567-e89b-42d3-a456-426614174000", "recent row")
	collector := func(
		_ context.Context,
		_ Host,
		emitRecent func([]session.Discovered) error,
	) ([]session.Discovered, []provider.Result, runtime.Report, error) {
		if err := emitRecent(discoveredFromSessions(recent)); err != nil {
			return nil, nil, runtime.Report{}, err
		}
		return nil, nil, runtime.Report{}, errors.New("ssh boom")
	}
	saves := 0

	var snapshots []Snapshot
	CollectHostsStream(context.Background(), hosts, 1, collector, HostCache{
		Save: func(string, []session.Session) { saves++ },
	}, func(snapshot Snapshot) {
		snapshots = append(snapshots, snapshot)
	})

	if len(snapshots) != 3 {
		t.Fatalf("snapshot count = %d, want initial, recent, failure", len(snapshots))
	}
	if len(snapshots[0].Result.Hosts) != 0 || snapshots[0].Done {
		t.Fatalf("initial snapshot leaked pending host: %#v", snapshots[0])
	}
	if len(snapshots[0].Loading) != 1 || snapshots[0].Loading[0] != "server" {
		t.Fatalf("initial loading = %#v", snapshots[0].Loading)
	}
	if snapshots[1].Done || len(snapshots[1].Result.Sessions) != 1 || !slices.Equal(snapshots[1].Loading, []string{"server"}) {
		t.Fatalf("recent snapshot = %#v", snapshots[1])
	}
	last := snapshots[2]
	if !last.Done || len(last.Result.Sessions) != 1 || last.Result.Sessions[0] != recent {
		t.Fatalf("failure snapshot = %#v", last)
	}
	if len(last.Result.Errors) != 1 || last.Result.Errors[0].Code != "ssh_failed" {
		t.Fatalf("failure error = %#v", last.Result.Errors)
	}
	if saves != 0 {
		t.Fatalf("failed no-cache collection saved %d times", saves)
	}
}

func TestCollectHostsStreamRejectsInvalidRecentAsProtocolError(t *testing.T) {
	saves := 0
	collector := func(
		_ context.Context,
		_ Host,
		emitRecent func([]session.Discovered) error,
	) ([]session.Discovered, []provider.Result, runtime.Report, error) {
		err := emitRecent([]session.Discovered{{
			Candidate: session.Candidate{
				Provider:  session.Provider("unknown"),
				NativeID:  "123e4567-e89b-42d3-a456-426614174000",
				UpdatedAt: time.Unix(10, 0).UTC(),
				CWD:       "/work",
			},
			Runtime: session.Runtime{State: session.RuntimeSaved},
		}})
		return nil, nil, runtime.Report{}, err
	}

	var snapshots []Snapshot
	CollectHostsStream(
		context.Background(),
		[]Host{{Target: "server"}},
		1,
		collector,
		HostCache{Save: func(string, []session.Session) { saves++ }},
		func(snapshot Snapshot) { snapshots = append(snapshots, snapshot) },
	)

	if len(snapshots) != 2 {
		t.Fatalf("snapshot count = %d, want initial and failure", len(snapshots))
	}
	final := snapshots[1]
	if len(final.Result.Sessions) != 0 || len(final.Result.Errors) != 1 || final.Result.Errors[0].Code != "protocol_error" {
		t.Fatalf("invalid recent result = %#v", final.Result)
	}
	if saves != 0 {
		t.Fatalf("invalid recent saved cache %d times", saves)
	}
}

func TestCollectHostsStreamHostsEmitRecentIndependentlyWithinWorkerLimit(t *testing.T) {
	hosts := []Host{{Target: "blocked"}, {Target: "fast"}}
	blockedRecent := streamSession("blocked", "123e4567-e89b-42d3-a456-426614174000", "blocked recent")
	fastRecent := streamSession("fast", "123e4567-e89b-42d3-a456-426614174001", "fast recent")
	blockedAfterRecent := make(chan struct{})
	releaseBlocked := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	collector := func(
		_ context.Context,
		host Host,
		emitRecent func([]session.Discovered) error,
	) ([]session.Discovered, []provider.Result, runtime.Report, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		switch host.Target {
		case "blocked":
			if err := emitRecent(discoveredFromSessions(blockedRecent)); err != nil {
				return nil, nil, runtime.Report{}, err
			}
			close(blockedAfterRecent)
			<-releaseBlocked
			return discoveredFromSessions(blockedRecent), nil, runtime.Report{Status: runtime.StatusOK}, nil
		case "fast":
			<-blockedAfterRecent
			if err := emitRecent(discoveredFromSessions(fastRecent)); err != nil {
				return nil, nil, runtime.Report{}, err
			}
			return discoveredFromSessions(fastRecent), nil, runtime.Report{Status: runtime.StatusOK}, nil
		default:
			t.Fatalf("unexpected host %q", host.Target)
			return nil, nil, runtime.Report{}, nil
		}
	}

	snapshots := make(chan Snapshot, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		CollectHostsStream(context.Background(), hosts, 2, collector, HostCache{}, func(snapshot Snapshot) {
			snapshots <- snapshot
		})
	}()

	fastEmittedWhileBlocked := false
	for !fastEmittedWhileBlocked {
		select {
		case snapshot := <-snapshots:
			_, fastEmittedWhileBlocked = sessionByID(snapshot.Result.Sessions, fastRecent.NativeID)
		case <-time.After(time.Second):
			t.Fatal("fast host recent snapshot was held by blocked host")
		}
	}
	if got := maximum.Load(); got != 2 {
		t.Fatalf("maximum concurrent collectors = %d, want 2", got)
	}
	close(releaseBlocked)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not finish after releasing blocked host")
	}
}

func TestCollectHostsStreamZeroHostsEmitsSingleDoneSnapshot(t *testing.T) {
	var snapshots []Snapshot
	CollectHostsStream(context.Background(), nil, 1, sequentialCollector(nil, nil), HostCache{}, func(snapshot Snapshot) {
		snapshots = append(snapshots, snapshot)
	})
	if len(snapshots) != 1 || !snapshots[0].Done {
		t.Fatalf("snapshots = %#v", snapshots)
	}
}

func TestCollectHostsStreamInvalidWorkerLimitFailsAllHostsOnce(t *testing.T) {
	hosts := []Host{{Target: "server"}}
	var snapshots []Snapshot
	CollectHostsStream(context.Background(), hosts, 0, sequentialCollector(nil, nil), HostCache{}, func(snapshot Snapshot) {
		snapshots = append(snapshots, snapshot)
	})
	if len(snapshots) != 1 || !snapshots[0].Done {
		t.Fatalf("snapshots = %#v", snapshots)
	}
	if len(snapshots[0].Result.Errors) != 1 || snapshots[0].Result.Errors[0].Code != "resource_limit" {
		t.Fatalf("errors = %#v", snapshots[0].Result.Errors)
	}
}

func streamValue(
	host, nativeID string,
	updatedAt time.Time,
	cwd, title string,
	state session.Runtime,
) session.Session {
	return session.Session{
		Host: host,
		Candidate: session.Candidate{
			Provider: session.Claude, NativeID: nativeID, UpdatedAt: updatedAt, CWD: cwd, Title: title,
		},
		Runtime: state,
	}
}

func discoveredFromSessions(values ...session.Session) []session.Discovered {
	discovered := make([]session.Discovered, len(values))
	for index, value := range values {
		discovered[index] = session.Discovered{Candidate: value.Candidate, Runtime: value.Runtime}
	}
	return discovered
}

func sessionByID(values []session.Session, nativeID string) (session.Session, bool) {
	for _, value := range values {
		if value.NativeID == nativeID {
			return value, true
		}
	}
	return session.Session{}, false
}
