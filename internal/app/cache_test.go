package app

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func cacheSession(host string) session.Session {
	return session.Session{
		Host: host,
		Candidate: session.Candidate{
			Provider:  session.Claude,
			NativeID:  "123e4567-e89b-42d3-a456-426614174000",
			UpdatedAt: time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC),
			CWD:       "/work/ars",
			Title:     "cache roundtrip",
		},
		Runtime: session.Runtime{State: session.RuntimeSaved},
	}
}

func TestCachePathPrefersXDGCacheHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/custom/cache")
	path, err := CachePath()
	if err != nil || path != filepath.Join("/custom/cache", "ars", "hosts") {
		t.Fatalf("CachePath() = %q, %v", path, err)
	}

	t.Setenv("XDG_CACHE_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}
	path, err = CachePath()
	if err != nil || path != filepath.Join(home, ".cache", "ars", "hosts") {
		t.Fatalf("CachePath() fallback = %q, %v", path, err)
	}
}

func TestHostCacheRoundTripEncodesTargetAndRestrictsPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hosts")
	target := "user@build/box:2222"
	want := []session.Session{cacheSession(target)}

	if err := SaveHostCache(dir, target, want); err != nil {
		t.Fatalf("SaveHostCache() error = %v", err)
	}

	path := filepath.Join(dir, url.PathEscape(target)+".json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cache file mode = %v, err = %v", info, err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("cache dir mode = %v, err = %v", dirInfo, err)
	}

	got, ok := LoadHostCache(dir, target)
	if !ok || len(got) != 1 || got[0] != want[0] {
		t.Fatalf("LoadHostCache() = %#v, %t", got, ok)
	}
}

func TestHostCacheSaveOverwritesAtomically(t *testing.T) {
	dir := t.TempDir()
	target := "server"
	if err := SaveHostCache(dir, target, []session.Session{cacheSession(target)}); err != nil {
		t.Fatalf("first SaveHostCache() error = %v", err)
	}
	if err := SaveHostCache(dir, target, nil); err != nil {
		t.Fatalf("second SaveHostCache() error = %v", err)
	}
	got, ok := LoadHostCache(dir, target)
	if !ok || len(got) != 0 {
		t.Fatalf("LoadHostCache() after overwrite = %#v, %t", got, ok)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("cache dir entries = %v, err = %v", entries, err)
	}
}

func TestHostCacheMissOnAbsentCorruptOrForeignData(t *testing.T) {
	dir := t.TempDir()
	target := "server"

	if _, ok := LoadHostCache(dir, target); ok {
		t.Fatal("absent file was not a miss")
	}

	path := filepath.Join(dir, url.PathEscape(target)+".json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt cache: %v", err)
	}
	if _, ok := LoadHostCache(dir, target); ok {
		t.Fatal("corrupt file was not a miss")
	}

	stale, err := json.Marshal(map[string]any{"schema_version": 99, "sessions": nil})
	if err != nil {
		t.Fatalf("marshal stale schema: %v", err)
	}
	if err := os.WriteFile(path, stale, 0o600); err != nil {
		t.Fatalf("write stale schema: %v", err)
	}
	if _, ok := LoadHostCache(dir, target); ok {
		t.Fatal("schema mismatch was not a miss")
	}

	foreign := cacheSession("other-host")
	if err := SaveHostCache(dir, "other-host", []session.Session{foreign}); err != nil {
		t.Fatalf("SaveHostCache() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "other-host.json"))
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("copy foreign cache: %v", err)
	}
	if _, ok := LoadHostCache(dir, target); ok {
		t.Fatal("session bound to a different host was not a miss")
	}
}

func TestHostCacheMissOnInvalidSessionPayload(t *testing.T) {
	dir := t.TempDir()
	target := "server"
	invalid := cacheSession(target)
	invalid.NativeID = "not-a-uuid"
	contents, err := json.Marshal(hostCacheFile{
		SchemaVersion: cacheSchemaVersion,
		CollectedAt:   time.Now(),
		Sessions:      []session.Session{invalid},
	})
	if err != nil {
		t.Fatalf("marshal invalid payload: %v", err)
	}
	path := filepath.Join(dir, url.PathEscape(target)+".json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write invalid payload: %v", err)
	}
	if _, ok := LoadHostCache(dir, target); ok {
		t.Fatal("invalid session payload was not a miss")
	}
}
