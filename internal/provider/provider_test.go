package provider

import (
	"context"
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
