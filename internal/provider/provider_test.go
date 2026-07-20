package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

const canonicalID = "123e4567-e89b-42d3-a456-426614174000"

func TestBuiltinRegistersOnlyClaudeThenCodex(t *testing.T) {
	adapters := Builtin()
	if len(adapters) != 2 {
		t.Fatalf("len(Builtin()) = %d, want 2", len(adapters))
	}

	want := []session.Provider{session.Claude, session.Codex}
	seen := make(map[session.Provider]bool, len(adapters))
	for i, adapter := range adapters {
		name := adapter.Name()
		if name != want[i] {
			t.Fatalf("Builtin()[%d].Name() = %q, want %q", i, name, want[i])
		}
		if seen[name] {
			t.Fatalf("Builtin() contains duplicate provider %q", name)
		}
		seen[name] = true
	}
}

func TestDiscoverAllValidatesRegistryBoundsAndSorts(t *testing.T) {
	claudeFirst := discoveredCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")
	claudeSecond := discoveredCandidate(session.Claude, "33333333-3333-3333-3333-333333333333")
	codex := discoveredCandidate(session.Codex, "22222222-2222-2222-2222-222222222222")
	adapters := []Adapter{
		&discoveryAdapter{name: session.Codex, result: Result{Provider: session.Codex, Sessions: []session.Candidate{codex}, Status: OK, Seen: 1}},
		&discoveryAdapter{name: session.Claude, result: Result{Provider: session.Claude, Sessions: []session.Candidate{claudeSecond, claudeFirst}, Status: OK, Seen: 2}},
	}

	gotCandidates, gotResults, err := DiscoverAll(context.Background(), "/remote/home", adapters)
	if err != nil {
		t.Fatal(err)
	}
	wantCandidates := []session.Candidate{claudeFirst, claudeSecond, codex}
	if !reflect.DeepEqual(gotCandidates, wantCandidates) {
		t.Fatalf("DiscoverAll() candidates = %#v, want %#v", gotCandidates, wantCandidates)
	}
	if gotResults[0].Provider != session.Claude || gotResults[1].Provider != session.Codex {
		t.Fatalf("DiscoverAll() results = %#v, want provider order", gotResults)
	}

	if candidates, results, err := DiscoverAll(context.Background(), "/remote/home", adapters[:1]); err == nil || candidates != nil || results != nil {
		t.Fatalf("DiscoverAll(invalid registry) = (%#v, %#v, %v), want nil data and error", candidates, results, err)
	}

	excess := make([]session.Candidate, maxDiscoveredSessions+1)
	for i := range excess {
		excess[i] = discoveredCandidate(session.Claude, fmt.Sprintf("%08x-0000-0000-0000-%012x", i, i))
	}
	overLimit := []Adapter{
		&discoveryAdapter{name: session.Claude, result: Result{Provider: session.Claude, Sessions: excess, Status: OK, Seen: len(excess)}},
		&discoveryAdapter{name: session.Codex, result: Result{Provider: session.Codex, Status: Absent}},
	}
	if candidates, results, err := DiscoverAll(context.Background(), "/remote/home", overLimit); err == nil || candidates != nil || results != nil {
		t.Fatalf("DiscoverAll(over limit) = (%d candidates, %#v, %v), want nil data and error", len(candidates), results, err)
	}
}

func TestLookupFindsBuiltinAndRejectsUnknownProvider(t *testing.T) {
	for _, name := range []session.Provider{session.Claude, session.Codex} {
		adapter, ok := Lookup(name)
		if !ok || adapter.Name() != name {
			t.Fatalf("Lookup(%q) = (%v, %v), want matching adapter", name, adapter, ok)
		}
	}
	if adapter, ok := Lookup("other"); ok || adapter != nil {
		t.Fatalf("Lookup(other) = (%v, %v), want (nil, false)", adapter, ok)
	}
}

func TestAdaptersValidateCanonicalUUIDAndReturnFixedResumeSpec(t *testing.T) {
	tests := []struct {
		provider session.Provider
		want     ResumeSpec
	}{
		{provider: session.Claude, want: ResumeSpec{Executable: "claude", Args: []string{"--resume", canonicalID}}},
		{provider: session.Codex, want: ResumeSpec{Executable: "codex", Args: []string{"resume", canonicalID}}},
	}

	invalid := []string{
		"",
		"123E4567-E89B-42D3-A456-426614174000",
		"123e4567e89b42d3a456426614174000",
		"not-a-session-id",
	}

	for _, tt := range tests {
		t.Run(string(tt.provider), func(t *testing.T) {
			adapter, _ := Lookup(tt.provider)
			if err := adapter.ValidateID(canonicalID); err != nil {
				t.Fatalf("ValidateID(%q) error = %v", canonicalID, err)
			}
			got, err := adapter.Resume(canonicalID)
			if err != nil {
				t.Fatalf("Resume(%q) error = %v", canonicalID, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Resume(%q) = %#v, want %#v", canonicalID, got, tt.want)
			}

			for _, id := range invalid {
				if err := adapter.ValidateID(id); err == nil {
					t.Errorf("ValidateID(%q) error = nil, want non-nil", id)
				}
				if got, err := adapter.Resume(id); err == nil || !reflect.DeepEqual(got, ResumeSpec{}) {
					t.Errorf("Resume(%q) = (%#v, %v), want zero spec and error", id, got, err)
				}
			}
		})
	}
}

func TestValidResumeSpecAcceptsOnlyFixedProviderCommands(t *testing.T) {
	tests := []struct {
		name     string
		provider session.Provider
		id       string
		spec     ResumeSpec
		want     bool
	}{
		{name: "Claude", provider: session.Claude, id: canonicalID, spec: ResumeSpec{Executable: "claude", Args: []string{"--resume", canonicalID}}, want: true},
		{name: "Codex", provider: session.Codex, id: canonicalID, spec: ResumeSpec{Executable: "codex", Args: []string{"resume", canonicalID}}, want: true},
		{name: "unknown provider", provider: "other", id: canonicalID, spec: ResumeSpec{Executable: "claude", Args: []string{"--resume", canonicalID}}},
		{name: "wrong executable", provider: session.Claude, id: canonicalID, spec: ResumeSpec{Executable: "sh", Args: []string{"--resume", canonicalID}}},
		{name: "extra argument", provider: session.Claude, id: canonicalID, spec: ResumeSpec{Executable: "claude", Args: []string{"--resume", canonicalID, "extra"}}},
		{name: "different ID", provider: session.Codex, id: canonicalID, spec: ResumeSpec{Executable: "codex", Args: []string{"resume", "00000000-0000-0000-0000-000000000000"}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ValidResumeSpec(test.provider, test.id, test.spec); got != test.want {
				t.Fatalf("ValidResumeSpec() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestNewerCandidateBoundsUniqueSessionsAndKeepsExistingIDUpdates(t *testing.T) {
	first := session.Candidate{NativeID: "first", UpdatedAt: time.Unix(1, 0)}
	second := session.Candidate{NativeID: "second", UpdatedAt: time.Unix(2, 0)}
	excess := session.Candidate{NativeID: "excess", UpdatedAt: time.Unix(3, 0)}
	candidates := make(map[string]session.Candidate)

	if !newerCandidate(candidates, first, 2) || !newerCandidate(candidates, second, 2) {
		t.Fatal("newerCandidate() rejected a candidate below the limit")
	}
	if newerCandidate(candidates, excess, 2) {
		t.Fatal("newerCandidate() accepted a new candidate at the limit")
	}
	if len(candidates) != 2 {
		t.Fatalf("len(candidates) = %d, want 2", len(candidates))
	}
	if _, exists := candidates[excess.NativeID]; exists {
		t.Fatal("excess candidate was retained")
	}

	updated := first
	updated.UpdatedAt = time.Unix(4, 0)
	if !newerCandidate(candidates, updated, 2) {
		t.Fatal("newerCandidate() rejected an existing ID update at the limit")
	}
	if got := candidates[first.NativeID].UpdatedAt; !got.Equal(updated.UpdatedAt) {
		t.Fatalf("existing candidate UpdatedAt = %v, want %v", got, updated.UpdatedAt)
	}
}

func TestReadDirBatchesVisitsEveryEntry(t *testing.T) {
	directory := t.TempDir()
	for i := range directoryBatchSize + 1 {
		writeFile(t, filepath.Join(directory, fixtureID(i)), "synthetic")
	}

	visited := 0
	err := readDirBatches(context.Background(), directory, func(os.DirEntry) error {
		visited++
		return nil
	})
	if err != nil {
		t.Fatalf("readDirBatches() error = %v", err)
	}
	if visited != directoryBatchSize+1 {
		t.Fatalf("readDirBatches() visited %d entries, want %d", visited, directoryBatchSize+1)
	}
}

type discoveryAdapter struct {
	name   session.Provider
	result Result
}

func (adapter *discoveryAdapter) Name() session.Provider { return adapter.name }

func (adapter *discoveryAdapter) Discover(context.Context, string) Result { return adapter.result }

func (adapter *discoveryAdapter) ValidateID(string) error { return nil }

func (adapter *discoveryAdapter) Resume(string) (ResumeSpec, error) { return ResumeSpec{}, nil }

func discoveredCandidate(name session.Provider, id string) session.Candidate {
	return session.Candidate{
		Provider: name, NativeID: id, UpdatedAt: time.Unix(10, 0).UTC(),
		CWD: "/work/project", Title: "Synthetic",
	}
}
