package app

import (
	"context"
	"errors"
	"testing"

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
	return func(_ context.Context, host Host) ([]session.Discovered, []provider.Result, runtime.Report, error) {
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

func TestCollectHostsStreamEmitsCacheFirstThenPerHostLiveUpdates(t *testing.T) {
	hosts := []Host{{Target: "localhost", Local: true}, {Target: "server"}}
	cachedLocal := streamSession("localhost", "123e4567-e89b-42d3-a456-426614174000", "cached local")
	liveLocal := streamSession("localhost", "123e4567-e89b-42d3-a456-426614174001", "live local")
	liveServer := streamSession("server", "123e4567-e89b-42d3-a456-426614174002", "live server")

	saved := make(map[string][]session.Session)
	cache := HostCache{
		Load: func(target string) ([]session.Session, bool) {
			if target == "localhost" {
				return []session.Session{cachedLocal}, true
			}
			return nil, false
		},
		Save: func(target string, sessions []session.Session) { saved[target] = sessions },
	}
	collector := sequentialCollector(map[string][]session.Session{
		"localhost": {liveLocal},
		"server":    {liveServer},
	}, nil)

	var snapshots []Snapshot
	CollectHostsStream(context.Background(), hosts, 1, collector, cache, func(snapshot Snapshot) {
		snapshots = append(snapshots, snapshot)
	})

	if len(snapshots) != 3 {
		t.Fatalf("snapshot count = %d, want 3: %#v", len(snapshots), snapshots)
	}
	first := snapshots[0]
	if first.Done || len(first.Result.Sessions) != 1 || first.Result.Sessions[0].Title != "cached local" {
		t.Fatalf("initial snapshot = %#v", first)
	}
	if len(first.Stale) != 1 || first.Stale[0] != "localhost" {
		t.Fatalf("initial stale = %#v", first.Stale)
	}
	last := snapshots[2]
	if !last.Done || len(last.Result.Sessions) != 2 || len(last.Stale) != 0 {
		t.Fatalf("final snapshot = %#v", last)
	}
	if len(saved["localhost"]) != 1 || saved["localhost"][0].Title != "live local" {
		t.Fatalf("localhost cache save = %#v", saved["localhost"])
	}
	if len(saved["server"]) != 1 || saved["server"][0].Title != "live server" {
		t.Fatalf("server cache save = %#v", saved["server"])
	}
}

func TestCollectHostsStreamKeepsCachedRowsAndStaleMarkOnFailure(t *testing.T) {
	hosts := []Host{{Target: "server"}}
	cached := streamSession("server", "123e4567-e89b-42d3-a456-426614174000", "cached row")
	saves := 0
	cache := HostCache{
		Load: func(string) ([]session.Session, bool) { return []session.Session{cached}, true },
		Save: func(string, []session.Session) { saves++ },
	}
	collector := sequentialCollector(nil, map[string]error{"server": errors.New("ssh boom")})

	var snapshots []Snapshot
	CollectHostsStream(context.Background(), hosts, 1, collector, cache, func(snapshot Snapshot) {
		snapshots = append(snapshots, snapshot)
	})

	last := snapshots[len(snapshots)-1]
	if !last.Done {
		t.Fatalf("final snapshot not done: %#v", last)
	}
	if len(last.Result.Sessions) != 1 || last.Result.Sessions[0].Title != "cached row" {
		t.Fatalf("cached rows dropped on failure: %#v", last.Result.Sessions)
	}
	if len(last.Stale) != 1 || last.Stale[0] != "server" {
		t.Fatalf("failed host lost stale mark: %#v", last.Stale)
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

func TestCollectHostsStreamWithoutCacheOmitsPendingHosts(t *testing.T) {
	hosts := []Host{{Target: "server"}}
	live := streamSession("server", "123e4567-e89b-42d3-a456-426614174000", "live row")
	collector := sequentialCollector(map[string][]session.Session{"server": {live}}, nil)

	var snapshots []Snapshot
	CollectHostsStream(context.Background(), hosts, 1, collector, HostCache{}, func(snapshot Snapshot) {
		snapshots = append(snapshots, snapshot)
	})

	if len(snapshots) != 2 {
		t.Fatalf("snapshot count = %d, want 2", len(snapshots))
	}
	if len(snapshots[0].Result.Hosts) != 0 || len(snapshots[0].Stale) != 0 || snapshots[0].Done {
		t.Fatalf("initial snapshot leaked pending host: %#v", snapshots[0])
	}
	if !snapshots[1].Done || len(snapshots[1].Result.Sessions) != 1 {
		t.Fatalf("final snapshot = %#v", snapshots[1])
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
