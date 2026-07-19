package provider

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func fixtureID(value int) string {
	return fmt.Sprintf("%08x-0000-0000-0000-%012x", value, value)
}

func fixtureHome(t *testing.T, provider string) string {
	t.Helper()
	home := t.TempDir()
	source := filepath.Join("testdata", provider)
	if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(home, rel)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, data, 0o600)
	}); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	return home
}

func installExecutable(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("synthetic executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertAbsentResult(t *testing.T, result Result, provider session.Provider) {
	t.Helper()
	if result.Provider != provider || result.Status != Absent || result.ErrorCode != "" ||
		result.Seen != 0 || result.Skipped != 0 || len(result.Sessions) != 0 {
		t.Fatalf("result = %#v, want empty absent result for %q", result, provider)
	}
}
