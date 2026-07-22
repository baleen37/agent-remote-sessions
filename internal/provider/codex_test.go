package provider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

func TestCodexDiscoverRejectsMultipleValidSessionMeta(t *testing.T) {
	tests := []struct {
		name     string
		secondID string
	}{
		{name: "different IDs", secondID: "22222222-2222-2222-2222-222222222222"},
		{name: "same ID", secondID: "11111111-1111-1111-1111-111111111111"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			installExecutable(t, "codex")
			writeFile(t, filepath.Join(home, ".codex", "sessions", "duplicate.jsonl"),
				codexMeta("11111111-1111-1111-1111-111111111111", "/synthetic/codex/first", "cli", "user")+
					codexMeta(tt.secondID, "/synthetic/codex/second", "vscode", "user"))

			result := (codexAdapter{}).Discover(context.Background(), home)
			if result.Status != Error || result.ErrorCode != "incompatible" || result.Seen != 1 || result.Skipped != 1 {
				t.Fatalf("Discover() = %#v, want error/incompatible with seen 1 skipped 1", result)
			}
			if len(result.Sessions) != 0 {
				t.Fatalf("len(Discover().Sessions) = %d, want 0", len(result.Sessions))
			}
		})
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

func TestCodexDiscoverBoundsUniqueSessions(t *testing.T) {
	home := t.TempDir()
	installExecutable(t, "codex")
	ids := []string{
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
		"33333333-3333-3333-3333-333333333333",
	}
	for _, id := range ids {
		writeFile(t, filepath.Join(home, ".codex", "sessions", id+".jsonl"),
			codexMeta(id, "/synthetic/codex/"+id, "cli", "user"))
	}

	result := (codexAdapter{}).discover(context.Background(), home, 2)
	if result.Status != Partial || result.ErrorCode != "resource_limit" || result.Seen != 3 || result.Skipped != 1 {
		t.Fatalf("discover() = %#v, want partial/resource_limit with seen 3 skipped 1", result)
	}
	if len(result.Sessions) != 2 {
		t.Fatalf("len(discover().Sessions) = %d, want 2", len(result.Sessions))
	}
}

func TestCodexDiscoverEnumeratesSessionsInBatches(t *testing.T) {
	home := t.TempDir()
	installExecutable(t, "codex")
	for i := 1; i <= directoryBatchSize+1; i++ {
		id := fixtureID(i)
		writeFile(t, filepath.Join(home, ".codex", "sessions", id+".jsonl"),
			codexMeta(id, "/synthetic/codex/"+id, "cli", "user"))
	}

	result := (codexAdapter{}).Discover(context.Background(), home)
	want := directoryBatchSize + 1
	if result.Status != OK || result.ErrorCode != "" || result.Seen != want || len(result.Sessions) != want {
		t.Fatalf("Discover() = %#v, want %d sessions across directory batches", result, want)
	}
}

func TestCodexDiscoverRejectsTraversalAboveMaxDepth(t *testing.T) {
	home := t.TempDir()
	installExecutable(t, "codex")
	directory := filepath.Join(home, ".codex", "sessions")
	for range maxCodexSessionDepth + 1 {
		directory = filepath.Join(directory, "nested")
	}
	id := fixtureID(1)
	writeFile(t, filepath.Join(directory, id+".jsonl"), codexMeta(id, "/synthetic/codex/deep", "cli", "user"))

	result := (codexAdapter{}).Discover(context.Background(), home)
	if result.Status != Error || result.ErrorCode != "resource_limit" || result.Seen != 0 || len(result.Sessions) != 0 {
		t.Fatalf("Discover() = %#v, want error/resource_limit without deep sessions", result)
	}
}

func TestCodexDiscoverDoesNotFollowDirectorySymlinks(t *testing.T) {
	home := t.TempDir()
	installExecutable(t, "codex")
	external := t.TempDir()
	id := fixtureID(1)
	writeFile(t, filepath.Join(external, id+".jsonl"), codexMeta(id, "/synthetic/codex/symlink", "cli", "user"))
	root := filepath.Join(home, ".codex", "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}

	assertAbsentResult(t, (codexAdapter{}).Discover(context.Background(), home), session.Codex)
}

func TestCodexDiscoverSkipsFIFOHistoryWithoutOpeningIt(t *testing.T) {
	home := t.TempDir()
	makeFIFO(t, filepath.Join(home, ".codex", "sessions", "blocked.jsonl"))
	installExecutable(t, "codex")

	result := discoverWithinTimeout(t, func() Result {
		return (codexAdapter{}).Discover(context.Background(), home)
	})
	assertAbsentResult(t, result, session.Codex)
}

func TestCodexDiscoverTitlesSessionsFromFirstUserMessage(t *testing.T) {
	home := t.TempDir()
	installExecutable(t, "codex")
	id := fixtureID(1)
	writeFile(t, filepath.Join(home, ".codex", "sessions", "titled.jsonl"),
		codexMeta(id, "/synthetic/codex/titled", "cli", "user")+
			codexUserMessage("fix the flaky test\nplus more context")+
			codexUserMessage("second message is ignored"))

	result := (codexAdapter{}).Discover(context.Background(), home)
	if result.Status != OK || len(result.Sessions) != 1 {
		t.Fatalf("Discover() = %#v, want one OK session", result)
	}
	if got := result.Sessions[0].Title; got != "fix the flaky test" {
		t.Fatalf("Title = %q, want first line of first user message", got)
	}
}

func TestCodexTitleNormalizesAndBounds(t *testing.T) {
	tests := []struct{ name, message, want string }{
		{name: "first line only", message: "line one\nline two", want: "line one"},
		{name: "controls become spaces", message: "\t do\tthing \r", want: "do thing"},
		{name: "whitespace only", message: "   \n\t", want: ""},
		{name: "leading blank lines", message: "\n\n do this\nrest", want: "do this"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexTitle(tt.message); got != tt.want {
				t.Fatalf("codexTitle(%q) = %q, want %q", tt.message, got, tt.want)
			}
		})
	}

	long := strings.Repeat("가", session.MaxTitleBytes)
	got := codexTitle(long)
	if got == "" || len(got) > session.MaxTitleBytes || !utf8.ValidString(got) || !strings.HasPrefix(long, got) {
		t.Fatalf("codexTitle(long) = %d bytes, want non-empty bounded valid UTF-8 prefix", len(got))
	}
	if err := session.ValidateCandidate(session.Candidate{
		Provider: session.Codex, NativeID: fixtureID(1),
		UpdatedAt: time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC),
		CWD:       "/synthetic/codex", Title: got,
	}); err != nil {
		t.Fatalf("normalized title fails validation: %v", err)
	}
}

func codexMeta(id, cwd, source, threadSource string) string {
	return "{\"type\":\"session_meta\",\"payload\":{\"id\":\"" + id + "\",\"cwd\":\"" + cwd + "\",\"source\":\"" + source + "\",\"thread_source\":\"" + threadSource + "\"}}\n"
}

func codexUserMessage(message string) string {
	payload, err := json.Marshal(map[string]any{"type": "user_message", "message": message})
	if err != nil {
		panic(err)
	}
	return `{"type":"event_msg","payload":` + string(payload) + "}\n"
}
