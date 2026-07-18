package provider

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func TestCodexDiscoverRecursesAndFiltersSessionMeta(t *testing.T) {
	home := fixtureHome(t, "codex")
	installExecutable(t, "codex")

	cliPath := filepath.Join(home, ".codex", "sessions", "2026", "07", "19", "cli.jsonl")
	wantTime := time.Date(2026, 7, 19, 9, 45, 0, 0, time.UTC)
	if err := os.Chtimes(cliPath, wantTime, wantTime); err != nil {
		t.Fatal(err)
	}

	result := (codexAdapter{}).Discover(context.Background(), home)
	if result.Provider != session.Codex || result.Status != Partial || result.ErrorCode != "corrupt" {
		t.Fatalf("Discover() summary = %#v, want codex partial/corrupt", result)
	}
	if result.Seen != 6 || result.Skipped != 4 {
		t.Fatalf("Discover() counts = seen %d skipped %d, want 6/4", result.Seen, result.Skipped)
	}
	if len(result.Sessions) != 2 {
		t.Fatalf("len(Discover().Sessions) = %d, want 2", len(result.Sessions))
	}

	want := map[string]session.Candidate{
		"66666666-6666-6666-6666-666666666666": {
			Provider: session.Codex, NativeID: "66666666-6666-6666-6666-666666666666",
			UpdatedAt: wantTime, CWD: "/synthetic/codex/cli", Title: "",
		},
		"77777777-7777-7777-7777-777777777777": {
			Provider: session.Codex, NativeID: "77777777-7777-7777-7777-777777777777",
			CWD: "/synthetic/codex/vscode", Title: "",
		},
	}
	for _, got := range result.Sessions {
		entry, ok := want[got.NativeID]
		if !ok {
			t.Fatalf("unexpected session %#v", got)
		}
		if entry.UpdatedAt.IsZero() {
			entry.UpdatedAt = got.UpdatedAt
		}
		if got != entry {
			t.Fatalf("session = %#v, want %#v", got, entry)
		}
		if err := session.ValidateCandidate(got); err != nil {
			t.Fatalf("discovered candidate is invalid: %v", err)
		}
	}
}

func TestCodexDiscoverDeduplicatesByNativeIDUsingNewestFile(t *testing.T) {
	home := t.TempDir()
	installExecutable(t, "codex")
	oldPath := filepath.Join(home, ".codex", "sessions", "old.jsonl")
	newPath := filepath.Join(home, ".codex", "sessions", "nested", "new.jsonl")
	writeFile(t, oldPath, codexMeta("88888888-8888-8888-8888-888888888888", "/synthetic/codex/old", "cli", "user"))
	writeFile(t, newPath, codexMeta("88888888-8888-8888-8888-888888888888", "/synthetic/codex/new", "vscode", "user"))
	oldTime := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	newTime := oldTime.Add(time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPath, newTime, newTime); err != nil {
		t.Fatal(err)
	}

	result := (codexAdapter{}).Discover(context.Background(), home)
	if result.Status != OK || len(result.Sessions) != 1 {
		t.Fatalf("Discover() = %#v, want one OK session", result)
	}
	if got := result.Sessions[0]; got.CWD != "/synthetic/codex/new" || !got.UpdatedAt.Equal(newTime) {
		t.Fatalf("deduplicated session = %#v, want newest file", got)
	}
}

func TestCodexDiscoverIsAbsentWithoutExecutableOrMetadata(t *testing.T) {
	t.Run("executable", func(t *testing.T) {
		home := t.TempDir()
		writeFile(t, filepath.Join(home, ".codex", "sessions", "valid.jsonl"),
			codexMeta("99999999-9999-9999-9999-999999999999", "/synthetic/codex", "cli", "user"))
		t.Setenv("PATH", t.TempDir())
		assertAbsentResult(t, (codexAdapter{}).Discover(context.Background(), home), session.Codex)
	})

	t.Run("metadata", func(t *testing.T) {
		home := t.TempDir()
		installExecutable(t, "codex")
		assertAbsentResult(t, (codexAdapter{}).Discover(context.Background(), home), session.Codex)
	})
}

func codexMeta(id, cwd, source, threadSource string) string {
	return "{\"type\":\"session_meta\",\"payload\":{\"id\":\"" + id + "\",\"cwd\":\"" + cwd + "\",\"source\":\"" + source + "\",\"thread_source\":\"" + threadSource + "\"}}\n"
}
