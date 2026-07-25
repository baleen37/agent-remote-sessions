package provider

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

type Phase uint8

const (
	PhaseRecent Phase = iota + 1
	PhaseComplete
)

type Snapshot struct {
	Phase      Phase
	Candidates []session.Candidate
	Results    []Result
}

type historyFile struct {
	path     string
	modified time.Time
}

type historyOutcome struct {
	candidate session.Candidate
	include   bool
	issue     string
}

type adapterEvent struct {
	index  int
	phase  Phase
	result Result
	ack    chan error
}

func discoverHistoryStream(
	ctx context.Context,
	name session.Provider,
	files []historyFile,
	inventoryIssue string,
	recentAfter time.Time,
	sessionLimit int,
	readHistory func(string) (session.Candidate, bool, string),
	emit func(Phase, Result) error,
) error {
	outcomes := make([]historyOutcome, len(files))
	recent := make([]int, 0, len(files))
	old := make([]int, 0, len(files))
	for index, file := range files {
		if file.modified.Before(recentAfter) {
			old = append(old, index)
		} else {
			recent = append(recent, index)
		}
	}

	if err := readHistoryPartition(ctx, files, outcomes, recent, readHistory); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := emit(PhaseRecent, aggregateHistory(name, outcomes, recent, sessionLimit, "")); err != nil {
		return err
	}
	if err := readHistoryPartition(ctx, files, outcomes, old, readHistory); err != nil {
		return err
	}
	all := make([]int, len(files))
	for index := range all {
		all[index] = index
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return emit(PhaseComplete, aggregateHistory(name, outcomes, all, sessionLimit, inventoryIssue))
}

func readHistoryPartition(
	ctx context.Context,
	files []historyFile,
	outcomes []historyOutcome,
	indexes []int,
	readHistory func(string) (session.Candidate, bool, string),
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, index := range indexes {
		if err := ctx.Err(); err != nil {
			return err
		}
		candidate, include, issue := readHistory(files[index].path)
		outcomes[index] = historyOutcome{candidate: candidate, include: include, issue: issue}
	}
	return nil
}

func aggregateHistory(
	name session.Provider,
	outcomes []historyOutcome,
	indexes []int,
	sessionLimit int,
	errorCode string,
) Result {
	result := Result{Provider: name}
	candidates := make(map[string]session.Candidate)
	for _, index := range indexes {
		outcome := outcomes[index]
		result.Seen++
		errorCode = strongerError(errorCode, outcome.issue)
		if !outcome.include {
			result.Skipped++
			continue
		}
		if !newerCandidate(candidates, outcome.candidate, sessionLimit) {
			result.Skipped++
			errorCode = strongerError(errorCode, "resource_limit")
		}
	}
	return finishResult(result, candidates, errorCode)
}

func DiscoverAllStream(
	ctx context.Context,
	home string,
	adapters []Adapter,
	recentAfter time.Time,
	emit func(Snapshot) error,
) error {
	if !validRegistry(adapters) {
		return fmt.Errorf("invalid provider registry")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	streamCtx, cancel := context.WithCancel(ctx)
	events := make(chan adapterEvent, len(adapters))
	done := make(chan error, len(adapters))
	var workers sync.WaitGroup
	for index, adapter := range adapters {
		workers.Add(1)
		go func(index int, adapter Adapter) {
			defer workers.Done()
			err := adapter.DiscoverStream(streamCtx, home, recentAfter, func(phase Phase, result Result) error {
				event := adapterEvent{
					index:  index,
					phase:  phase,
					result: result,
					ack:    make(chan error, 1),
				}
				select {
				case events <- event:
				case <-streamCtx.Done():
					return streamCtx.Err()
				}
				select {
				case err := <-event.ack:
					return err
				case <-streamCtx.Done():
					return streamCtx.Err()
				}
			})
			done <- err
		}(index, adapter)
	}
	defer func() {
		cancel()
		workers.Wait()
	}()

	for _, phase := range []Phase{PhaseRecent, PhaseComplete} {
		phaseEvents := make([]adapterEvent, len(adapters))
		received := make([]bool, len(adapters))
		for count := 0; count < len(adapters); {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case err := <-done:
				if err != nil {
					return err
				}
				return fmt.Errorf("provider stream ended before complete phase")
			case event := <-events:
				if event.index < 0 || event.index >= len(adapters) || received[event.index] || event.phase != phase {
					return fmt.Errorf("invalid provider phase")
				}
				received[event.index] = true
				phaseEvents[event.index] = event
				count++
			}
		}

		results := make([]Result, len(adapters))
		for index, event := range phaseEvents {
			results[index] = event.result
		}
		snapshot, err := buildSnapshot(phase, adapters, results)
		if err == nil {
			err = emit(snapshot)
		}
		for _, event := range phaseEvents {
			event.ack <- err
		}
		if err != nil {
			return err
		}
	}

	for range adapters {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func buildSnapshot(phase Phase, adapters []Adapter, results []Result) (Snapshot, error) {
	candidates := make([]session.Candidate, 0)
	for index, result := range results {
		if result.Provider != adapters[index].Name() {
			return Snapshot{}, fmt.Errorf("invalid provider result")
		}
		for _, candidate := range result.Sessions {
			if candidate.Provider != result.Provider || session.ValidateCandidate(candidate) != nil {
				return Snapshot{}, fmt.Errorf("invalid provider candidate")
			}
			if len(candidates) >= maxDiscoveredSessions {
				return Snapshot{}, fmt.Errorf("combined session count exceeds limit")
			}
			candidates = append(candidates, candidate)
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Provider != candidates[j].Provider {
			return providerOrder(candidates[i].Provider) < providerOrder(candidates[j].Provider)
		}
		return candidates[i].NativeID < candidates[j].NativeID
	})
	snapshot := Snapshot{Phase: phase, Candidates: candidates}
	if phase == PhaseComplete {
		snapshot.Results = append([]Result(nil), results...)
		sort.Slice(snapshot.Results, func(i, j int) bool {
			return providerOrder(snapshot.Results[i].Provider) < providerOrder(snapshot.Results[j].Provider)
		})
	}
	return snapshot, nil
}
