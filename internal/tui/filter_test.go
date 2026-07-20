package tui

import (
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func TestFilterSessionsMatchesCaseInsensitiveUnicodeSubstrings(t *testing.T) {
	item := session.Session{
		Host: "macbook",
		Candidate: session.Candidate{
			Provider:  session.Claude,
			NativeID:  "123e4567-e89b-42d3-a456-426614174000",
			UpdatedAt: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
			CWD:       "/work/Éclair-api",
			Title:     "배치 API 고치기 ΟΔΥΣΣΕΎΣ",
		},
	}

	for _, query := range []string{
		"배치", "api", "éCLAIR", "CLAUDE", "LOCAL", "éclair-api", "οδυσσεύς",
		"123e4567-e89b",
	} {
		t.Run(query, func(t *testing.T) {
			got := filterSessions([]session.Session{item}, query, "macbook")
			if len(got) != 1 || keyOf(got[0]) != keyOf(item) {
				t.Fatalf("filterSessions(query %q) = %#v", query, got)
			}
		})
	}
	if got := filterSessions([]session.Session{item}, "missing", "macbook"); len(got) != 0 {
		t.Fatalf("filterSessions(missing) = %#v", got)
	}
}

func TestFilterSessionsPreservesInputOrderAndCanonicalValues(t *testing.T) {
	items := twoSessions()
	got := filterSessions(items, "", "macbook")
	if len(got) != len(items) {
		t.Fatalf("filterSessions() len = %d, want %d", len(got), len(items))
	}
	for index := range items {
		if keyOf(got[index]) != keyOf(items[index]) {
			t.Fatalf("filterSessions()[%d] = %#v, want %#v", index, got[index], items[index])
		}
	}
}
