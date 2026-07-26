package tui

import (
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func TestFilterSessionsMatchesCaseInsensitiveUnicodeSubstrings(t *testing.T) {
	item := session.Session{
		Host: "localhost",
		Candidate: session.Candidate{
			Provider:  session.Claude,
			NativeID:  "123e4567-e89b-42d3-a456-426614174000",
			UpdatedAt: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
			CWD:       "/work/Éclair-api",
			Title:     "배치 API 고치기 ΟΔΥΣΣΕΎΣ",
		},
	}

	for _, query := range []string{
		"배치", "api", "éCLAIR", "CLAUDE", "éclair-api", "οδυσσεύς",
		"123e4567-e89b",
	} {
		t.Run(query, func(t *testing.T) {
			got := filterSessions([]session.Session{item}, query, "localhost")
			if len(got) != 1 || keyOf(got[0]) != keyOf(item) {
				t.Fatalf("filterSessions(query %q) = %#v", query, got)
			}
		})
	}
	if got := filterSessions([]session.Session{item}, "missing", "localhost"); len(got) != 0 {
		t.Fatalf("filterSessions(missing) = %#v", got)
	}
}

func TestFilterSessionsPreservesInputOrderAndCanonicalValues(t *testing.T) {
	items := twoSessions()
	got := filterSessions(items, "", "localhost")
	if len(got) != len(items) {
		t.Fatalf("filterSessions() len = %d, want %d", len(got), len(items))
	}
	for index := range items {
		if keyOf(got[index]) != keyOf(items[index]) {
			t.Fatalf("filterSessions()[%d] = %#v, want %#v", index, got[index], items[index])
		}
	}
}

func TestFilterSessionsDoesNotExposeLocalTarget(t *testing.T) {
	item := twoSessions()[0]
	item.Host = "localhost"
	for _, query := range []string{"local", "localhost"} {
		if got := filterSessions([]session.Session{item}, query, "localhost"); len(got) != 0 {
			t.Fatalf("hidden local target matched search %q: %#v", query, got)
		}
	}
}

func TestLocationBracketsRemoteHostAndLeavesLocalBlank(t *testing.T) {
	local := twoSessions()[0]
	local.Host = "localhost"
	if got := location(local, "localhost"); got != "" {
		t.Fatalf("location(local) = %q, want empty", got)
	}

	remote := twoSessions()[0]
	remote.Host = "server"
	if got := location(remote, "localhost"); got != "[server]" {
		t.Fatalf("location(remote) = %q, want %q", got, "[server]")
	}
}

func TestFilterSessionsMatchesRemoteHostInsideBrackets(t *testing.T) {
	item := twoSessions()[0]
	item.Host = "server"
	got := filterSessions([]session.Session{item}, "server", "localhost")
	if len(got) != 1 || keyOf(got[0]) != keyOf(item) {
		t.Fatalf("filterSessions(server) = %#v", got)
	}
}

func staleFilterSession(id string, state session.RuntimeState, updated time.Time) session.Session {
	return session.Session{
		Host: "localhost",
		Candidate: session.Candidate{
			Provider:  session.Claude,
			NativeID:  id,
			UpdatedAt: updated,
			CWD:       "/work/ars",
			Title:     id,
		},
		Runtime: session.Runtime{State: state},
	}
}

// filterByStale hides saved sessions whose latest activity is older than
// staleAfter, unless showAll is on or the session is pinned. Live sessions
// (running/attached) are never hidden regardless of age.
func TestFilterByStaleHidesOldSavedSessionsByDefault(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	fresh := staleFilterSession("fresh", session.RuntimeSaved, now.Add(-6*24*time.Hour))
	old := staleFilterSession("old", session.RuntimeSaved, now.Add(-8*24*time.Hour))
	items := []session.Session{fresh, old}

	visible, hidden := filterByStale(items, now, false, nil)
	if len(visible) != 1 || visible[0].NativeID != "fresh" {
		t.Fatalf("filterByStale visible = %+v, want only the fresh session", visible)
	}
	if hidden != 1 {
		t.Fatalf("filterByStale hidden = %d, want 1", hidden)
	}
}

func TestFilterByStaleKeepsLiveSessionsRegardlessOfAge(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	oldRunning := staleFilterSession("old-running", session.RuntimeRunning, now.Add(-30*24*time.Hour))
	oldAttached := staleFilterSession("old-attached", session.RuntimeAttached, now.Add(-30*24*time.Hour))
	items := []session.Session{oldRunning, oldAttached}

	visible, hidden := filterByStale(items, now, false, nil)
	if len(visible) != 2 {
		t.Fatalf("filterByStale visible = %+v, want both live sessions kept", visible)
	}
	if hidden != 0 {
		t.Fatalf("filterByStale hidden = %d, want 0", hidden)
	}
}

func TestFilterByStaleKeepsPinnedSessionsRegardlessOfAge(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	old := staleFilterSession("old", session.RuntimeSaved, now.Add(-30*24*time.Hour))
	pins := map[sessionKey]bool{keyOf(old): true}

	visible, hidden := filterByStale([]session.Session{old}, now, false, pins)
	if len(visible) != 1 {
		t.Fatalf("filterByStale visible = %+v, want the pinned session kept", visible)
	}
	if hidden != 0 {
		t.Fatalf("filterByStale hidden = %d, want 0", hidden)
	}
}

func TestFilterByStaleShowAllRevealsEverything(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	old := staleFilterSession("old", session.RuntimeSaved, now.Add(-30*24*time.Hour))

	visible, hidden := filterByStale([]session.Session{old}, now, true, nil)
	if len(visible) != 1 {
		t.Fatalf("filterByStale showAll visible = %+v, want the old session shown", visible)
	}
	if hidden != 0 {
		t.Fatalf("filterByStale showAll hidden = %d, want 0", hidden)
	}
}

func TestFilterByStaleBoundaryAtExactlySevenDaysIsNotHidden(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	boundary := staleFilterSession("boundary", session.RuntimeSaved, now.Add(-session.RecentWindow))

	visible, hidden := filterByStale([]session.Session{boundary}, now, false, nil)
	if len(visible) != 1 {
		t.Fatalf("filterByStale at exact boundary visible = %+v, want kept", visible)
	}
	if hidden != 0 {
		t.Fatalf("filterByStale at exact boundary hidden = %d, want 0", hidden)
	}

	older := staleFilterSession("older", session.RuntimeSaved, now.Add(-session.RecentWindow-time.Nanosecond))
	visible, hidden = filterByStale([]session.Session{older}, now, false, nil)
	if len(visible) != 0 || hidden != 1 {
		t.Fatalf("filterByStale one nanosecond past boundary = %+v/%d, want hidden", visible, hidden)
	}
}
