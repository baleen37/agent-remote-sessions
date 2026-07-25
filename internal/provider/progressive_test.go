package provider

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func TestDiscoverAllStreamStartsProvidersConcurrentlyAndKeepsRegistryOrder(t *testing.T) {
	started := make(chan session.Provider, 2)
	release := make(chan struct{})
	adapters := progressiveAdapters(started, release)
	var snapshots []Snapshot
	done := make(chan error, 1)
	go func() {
		done <- DiscoverAllStream(context.Background(), "/home", adapters, time.Unix(100, 0), func(value Snapshot) error {
			snapshots = append(snapshots, value)
			return nil
		})
	}()

	var first, second session.Provider
	for index := range 2 {
		select {
		case provider := <-started:
			if index == 0 {
				first = provider
			} else {
				second = provider
			}
		case <-time.After(2 * time.Second):
			t.Fatal("providers did not start concurrently")
		}
	}
	if first == second {
		t.Fatalf("providers did not start independently: %q %q", first, second)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 || snapshots[0].Phase != PhaseRecent || snapshots[1].Phase != PhaseComplete {
		t.Fatalf("phases = %#v", snapshots)
	}
	if snapshots[0].Results != nil {
		t.Fatalf("recent results = %#v, want nil", snapshots[0].Results)
	}
	if snapshots[1].Results[0].Provider != session.Claude || snapshots[1].Results[1].Provider != session.Codex {
		t.Fatalf("result order = %#v", snapshots[1].Results)
	}
}

func TestDiscoverAllStreamPropagatesCallbackAndContextErrors(t *testing.T) {
	t.Run("callback", func(t *testing.T) {
		want := errors.New("stop snapshots")
		err := DiscoverAllStream(
			context.Background(),
			"/home",
			progressiveAdapters(nil, nil),
			time.Unix(100, 0),
			func(Snapshot) error { return want },
		)
		if !errors.Is(err, want) {
			t.Fatalf("DiscoverAllStream() error = %v, want %v", err, want)
		}
	})

	t.Run("context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := DiscoverAllStream(
			ctx,
			"/home",
			progressiveAdapters(nil, nil),
			time.Unix(100, 0),
			func(Snapshot) error {
				t.Fatal("callback ran after cancellation")
				return nil
			},
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("DiscoverAllStream() error = %v, want context canceled", err)
		}
	})
}

func TestDiscoverAllKeepsFinalOnlyCompatibility(t *testing.T) {
	claudeRecent := discoveredCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")
	claudeOld := discoveredCandidate(session.Claude, "33333333-3333-3333-3333-333333333333")
	codexOld := discoveredCandidate(session.Codex, "22222222-2222-2222-2222-222222222222")
	adapters := []Adapter{
		&progressiveAdapter{
			name:   session.Codex,
			recent: Result{Provider: session.Codex, Status: Absent},
			final:  Result{Provider: session.Codex, Sessions: []session.Candidate{codexOld}, Status: OK, Seen: 1},
		},
		&progressiveAdapter{
			name:   session.Claude,
			recent: Result{Provider: session.Claude, Sessions: []session.Candidate{claudeRecent}, Status: OK, Seen: 1},
			final:  Result{Provider: session.Claude, Sessions: []session.Candidate{claudeOld, claudeRecent}, Status: OK, Seen: 2},
		},
	}

	candidates, results, err := DiscoverAll(context.Background(), "/home", adapters)
	if err != nil {
		t.Fatal(err)
	}
	wantCandidates := []session.Candidate{claudeRecent, claudeOld, codexOld}
	if !reflect.DeepEqual(candidates, wantCandidates) {
		t.Fatalf("DiscoverAll() candidates = %#v, want %#v", candidates, wantCandidates)
	}
	if len(results) != 2 || results[0].Provider != session.Claude || results[1].Provider != session.Codex {
		t.Fatalf("DiscoverAll() results = %#v, want final provider order", results)
	}
}

func TestBuiltinDiscoverStreamStopsBeforeCallbackWhenContextCanceled(t *testing.T) {
	for _, adapter := range []Adapter{claudeAdapter{}, codexAdapter{}} {
		t.Run(string(adapter.Name()), func(t *testing.T) {
			installExecutable(t, string(adapter.Name()))
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			called := false
			err := adapter.DiscoverStream(ctx, t.TempDir(), time.Unix(100, 0), func(Phase, Result) error {
				called = true
				return nil
			})
			if !errors.Is(err, context.Canceled) || called {
				t.Fatalf("DiscoverStream() = %v, callback called = %v; want context canceled before callback", err, called)
			}
		})
	}
}

type progressiveAdapter struct {
	name    session.Provider
	recent  Result
	final   Result
	started chan<- session.Provider
	release <-chan struct{}
}

func progressiveAdapters(started chan<- session.Provider, release <-chan struct{}) []Adapter {
	return []Adapter{
		&progressiveAdapter{
			name: session.Claude,
			recent: Result{
				Provider: session.Claude,
				Sessions: []session.Candidate{discoveredCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")},
				Status:   OK,
				Seen:     1,
			},
			final: Result{
				Provider: session.Claude,
				Sessions: []session.Candidate{discoveredCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")},
				Status:   OK,
				Seen:     1,
			},
			started: started,
			release: release,
		},
		&progressiveAdapter{
			name: session.Codex,
			recent: Result{
				Provider: session.Codex,
				Sessions: []session.Candidate{discoveredCandidate(session.Codex, "22222222-2222-2222-2222-222222222222")},
				Status:   OK,
				Seen:     1,
			},
			final: Result{
				Provider: session.Codex,
				Sessions: []session.Candidate{discoveredCandidate(session.Codex, "22222222-2222-2222-2222-222222222222")},
				Status:   OK,
				Seen:     1,
			},
			started: started,
			release: release,
		},
	}
}

func (adapter *progressiveAdapter) Name() session.Provider { return adapter.name }

func (adapter *progressiveAdapter) Discover(context.Context, string) Result { return adapter.final }

func (adapter *progressiveAdapter) DiscoverStream(
	ctx context.Context,
	_ string,
	_ time.Time,
	emit func(Phase, Result) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if adapter.started != nil {
		adapter.started <- adapter.name
	}
	if adapter.release != nil {
		select {
		case <-adapter.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := emit(PhaseRecent, adapter.recent); err != nil {
		return err
	}
	return emit(PhaseComplete, adapter.final)
}

func (adapter *progressiveAdapter) ValidateID(string) error { return nil }

func (adapter *progressiveAdapter) Resume(string) (ResumeSpec, error) { return ResumeSpec{}, nil }
