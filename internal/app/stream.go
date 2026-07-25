package app

import (
	"context"
	"fmt"
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

	type event struct {
		index      int
		collection hostCollection
		early      bool
		ack        chan struct{}
	}
	events := make(chan event)
	jobs := make(chan int)
	workers := min(workerLimit, len(hosts))
	var waitGroup sync.WaitGroup
	waitGroup.Add(workers)
	for range workers {
		go func() {
			defer waitGroup.Done()
			for index := range jobs {
				emitRecent := func(discovered []session.Discovered) error {
					sessions, err := bindSessions(hosts[index].Target, discovered)
					if err != nil {
						return fmt.Errorf("collector protocol: %w", err)
					}
					update := event{
						index: index,
						collection: hostCollection{
							host:     output.HostResult{Target: hosts[index].Target, Status: output.HostOK},
							sessions: sessions,
						},
						early: true,
						ack:   make(chan struct{}),
					}
					select {
					case events <- update:
					case <-ctx.Done():
						return ctx.Err()
					}
					select {
					case <-update.ack:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				collection := collectHost(ctx, hosts[index], collector, emitRecent)
				events <- event{index: index, collection: collection}
			}
		}()
	}
	go func() {
		for index := range hosts {
			jobs <- index
		}
		close(jobs)
		waitGroup.Wait()
		close(events)
	}()

	remaining := len(hosts)
	for update := range events {
		if update.early {
			update.collection.sessions = overlaySessions(collections[update.index].sessions, update.collection.sessions)
			collections[update.index] = update.collection
			hasData[update.index] = true
			emit(snapshot(false))
			close(update.ack)
			continue
		}

		collection := update.collection
		if collection.err == nil {
			if cache.Save != nil {
				cache.Save(hosts[update.index].Target, collection.sessions)
			}
		} else if hasData[update.index] {
			collection.sessions = collections[update.index].sessions
		}
		collections[update.index] = collection
		hasData[update.index] = true
		pending[update.index] = false
		remaining--
		emit(snapshot(remaining == 0))
	}
}
