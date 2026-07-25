package app

import (
	"context"
	"sync"

	"github.com/baleen37/agent-remote-sessions/internal/output"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

// DefaultWorkerLimit bounds concurrent host collections.
const DefaultWorkerLimit = 8

type Snapshot struct {
	Result  Result
	Loading []string
	Done    bool
}

type HostCache struct {
	Load func(target string) ([]session.Session, bool)
	Save func(target string, sessions []session.Session)
}

func CollectHostsStream(ctx context.Context, hosts []Host, workerLimit int, collector Collector, cache HostCache, emit func(Snapshot)) {
	collections := make([]hostCollection, len(hosts))
	hasData := make([]bool, len(hosts))
	fromCache := make([]bool, len(hosts))
	pending := make([]bool, len(hosts))
	for index := range pending {
		pending[index] = true
	}

	if workerLimit <= 0 || collector == nil {
		for index, host := range hosts {
			collections[index] = failedCollection(host.Target, "resource_limit", "Collector resource limit exceeded")
		}
		emit(Snapshot{Result: mergeCollections(collections), Loading: []string{}, Done: true})
		return
	}

	for index, host := range hosts {
		if cache.Load == nil {
			break
		}
		sessions, ok := cache.Load(host.Target)
		if !ok {
			continue
		}
		collections[index] = hostCollection{
			host:     output.HostResult{Target: host.Target, Status: output.HostOK},
			sessions: sessions,
		}
		hasData[index] = true
		fromCache[index] = true
	}

	snapshot := func(done bool) Snapshot {
		present := make([]hostCollection, 0, len(collections))
		loading := make([]string, 0, len(hosts))
		for index := range collections {
			if pending[index] {
				loading = append(loading, hosts[index].Target)
			}
			if !hasData[index] {
				continue
			}
			present = append(present, collections[index])
		}
		return Snapshot{Result: mergeCollections(present), Loading: loading, Done: done}
	}

	emit(snapshot(len(hosts) == 0))
	if len(hosts) == 0 {
		return
	}

	type completion struct {
		index      int
		collection hostCollection
	}
	completions := make(chan completion)
	jobs := make(chan int)
	workers := min(workerLimit, len(hosts))
	var waitGroup sync.WaitGroup
	waitGroup.Add(workers)
	for range workers {
		go func() {
			defer waitGroup.Done()
			for index := range jobs {
				completions <- completion{index: index, collection: collectHost(ctx, hosts[index], collector)}
			}
		}()
	}
	go func() {
		for index := range hosts {
			jobs <- index
		}
		close(jobs)
		waitGroup.Wait()
		close(completions)
	}()

	remaining := len(hosts)
	for done := range completions {
		collection := done.collection
		if collection.err == nil {
			if cache.Save != nil {
				cache.Save(hosts[done.index].Target, collection.sessions)
			}
		} else if fromCache[done.index] {
			collection.sessions = collections[done.index].sessions
		}
		collections[done.index] = collection
		hasData[done.index] = true
		pending[done.index] = false
		remaining--
		emit(snapshot(remaining == 0))
	}
}
