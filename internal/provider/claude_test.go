package provider

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func TestClaudeDiscoverStreamsDirectProjectHistories(t *testing.T) {
	home := fixtureHome(t, "claude")
	installExecutable(t, "claude")

	validPath := filepath.Join(home, ".claude", "projects", "project-one", "valid.jsonl")
	wantTime := time.Date(2026, 7, 19, 8, 30, 0, 0, time.UTC)
	if err := os.Chtimes(validPath, wantTime, wantTime); err != nil {
		t.Fatal(err)
	}

	result := (claudeAdapter{}).Discover(context.Background(), home)
	if result.Provider != session.Claude || result.Status != Partial || result.ErrorCode != "corrupt" {
		t.Fatalf("Discover() summary = %#v, want claude partial/corrupt", result)
	}
	if result.Seen != 4 || result.Skipped != 3 {
		t.Fatalf("Discover() counts = seen %d skipped %d, want 4/3", result.Seen, result.Skipped)
	}
	if len(result.Sessions) != 1 {
		t.Fatalf("len(Discover().Sessions) = %d, want 1", len(result.Sessions))
	}
	want := session.Candidate{
		Provider:  session.Claude,
		NativeID:  "11111111-1111-1111-1111-111111111111",
		UpdatedAt: wantTime,
		CWD:       "/synthetic/claude/latest",
		Title:     "Synthetic native title",
	}
	if got := result.Sessions[0]; got != want {
		t.Fatalf("Discover().Sessions[0] = %#v, want %#v", got, want)
	}
	if err := session.ValidateCandidate(result.Sessions[0]); err != nil {
		t.Fatalf("discovered candidate is invalid: %v", err)
	}
}

func TestClaudeDiscoverLeavesTitleEmptyWithoutNativeTitle(t *testing.T) {
	home := t.TempDir()
	installExecutable(t, "claude")
	writeFile(t, filepath.Join(home, ".claude", "projects", "project", "untitled.jsonl"),
		"{\"type\":\"user\",\"sessionId\":\"55555555-5555-5555-5555-555555555555\",\"cwd\":\"/synthetic/claude/untitled\"}\n")

	result := (claudeAdapter{}).Discover(context.Background(), home)
	if result.Status != OK || result.ErrorCode != "" || len(result.Sessions) != 1 {
		t.Fatalf("Discover() = %#v, want one OK session", result)
	}
	if result.Sessions[0].Title != "" {
		t.Fatalf("Discover().Sessions[0].Title = %q, want empty", result.Sessions[0].Title)
	}
}

func TestClaudeDiscoverKeepsLatestValidCWDAndReportsInvalidRecord(t *testing.T) {
	home := t.TempDir()
	installExecutable(t, "claude")
	writeFile(t, filepath.Join(home, ".claude", "projects", "project", "cwd.jsonl"),
		"{\"type\":\"user\",\"sessionId\":\"5a5a5a5a-5a5a-5a5a-5a5a-5a5a5a5a5a5a\",\"cwd\":\"/synthetic/claude/valid\"}\n"+
			"{\"type\":\"user\",\"sessionId\":\"5a5a5a5a-5a5a-5a5a-5a5a-5a5a5a5a5a5a\",\"cwd\":\"relative/invalid\"}\n")

	result := (claudeAdapter{}).Discover(context.Background(), home)
	if result.Status != Partial || result.ErrorCode != "incompatible" || len(result.Sessions) != 1 {
		t.Fatalf("Discover() = %#v, want one partial/incompatible session", result)
	}
	if got := result.Sessions[0].CWD; got != "/synthetic/claude/valid" {
		t.Fatalf("Discover().Sessions[0].CWD = %q, want latest valid CWD", got)
	}
}

func TestClaudeDiscoverExcludesMixedSessionIDs(t *testing.T) {
	home := t.TempDir()
	installExecutable(t, "claude")
	writeFile(t, filepath.Join(home, ".claude", "projects", "project", "mixed.jsonl"),
		"{\"type\":\"user\",\"sessionId\":\"5b5b5b5b-5b5b-5b5b-5b5b-5b5b5b5b5b5b\",\"cwd\":\"/synthetic/claude/session-a\"}\n"+
			"{\"type\":\"custom-title\",\"sessionId\":\"5c5c5c5c-5c5c-5c5c-5c5c-5c5c5c5c5c5c\",\"cwd\":\"/synthetic/claude/session-b\",\"customTitle\":\"Synthetic session B title\"}\n")

	result := (claudeAdapter{}).Discover(context.Background(), home)
	if result.Status != Error || result.ErrorCode != "incompatible" {
		t.Fatalf("Discover() summary = %#v, want error/incompatible", result)
	}
	if len(result.Sessions) != 0 || result.Seen != 1 || result.Skipped != 1 {
		t.Fatalf("Discover() = %#v, want mixed-ID history excluded", result)
	}
}

func TestClaudeDiscoverIsAbsentWithoutExecutableOrMetadata(t *testing.T) {
	t.Run("executable", func(t *testing.T) {
		home := t.TempDir()
		writeFile(t, filepath.Join(home, ".claude", "projects", "project", "valid.jsonl"),
			"{\"type\":\"user\",\"sessionId\":\"55555555-5555-5555-5555-555555555555\",\"cwd\":\"/synthetic/claude\"}\n")
		t.Setenv("PATH", t.TempDir())
		assertAbsentResult(t, (claudeAdapter{}).Discover(context.Background(), home), session.Claude)
	})

	t.Run("metadata", func(t *testing.T) {
		home := t.TempDir()
		installExecutable(t, "claude")
		assertAbsentResult(t, (claudeAdapter{}).Discover(context.Background(), home), session.Claude)
	})
}

func TestClaudeDiscoverRejectsOverlongJSONLRecord(t *testing.T) {
	home := t.TempDir()
	installExecutable(t, "claude")
	path := filepath.Join(home, ".claude", "projects", "project", "large.jsonl")
	writeFile(t, path, "{\"ignored\":\""+string(make([]byte, 1<<20))+"\"}\n")

	result := (claudeAdapter{}).Discover(context.Background(), home)
	if result.Status != Error || result.ErrorCode != "resource_limit" || len(result.Sessions) != 0 {
		t.Fatalf("Discover() = %#v, want error/resource_limit with no sessions", result)
	}
}
