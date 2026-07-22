package app

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

const cacheSchemaVersion = 1

type hostCacheFile struct {
	SchemaVersion int               `json:"schema_version"`
	CollectedAt   time.Time         `json:"collected_at"`
	Sessions      []session.Session `json:"sessions"`
}

func CachePath() (string, error) {
	if cacheHome := os.Getenv("XDG_CACHE_HOME"); cacheHome != "" {
		return filepath.Join(cacheHome, "ars", "hosts"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".cache", "ars", "hosts"), nil
}

func hostCacheFilePath(dir, target string) string {
	return filepath.Join(dir, url.PathEscape(target)+".json")
}

func LoadHostCache(dir, target string) ([]session.Session, bool) {
	contents, err := os.ReadFile(hostCacheFilePath(dir, target))
	if err != nil {
		return nil, false
	}
	var file hostCacheFile
	if err := json.Unmarshal(contents, &file); err != nil {
		return nil, false
	}
	if file.SchemaVersion != cacheSchemaVersion {
		return nil, false
	}
	sessions := make([]session.Session, 0, len(file.Sessions))
	for _, item := range file.Sessions {
		if item.Host != target {
			return nil, false
		}
		bound, err := session.BindDiscovered(target, session.Discovered{
			Candidate: item.Candidate,
			Runtime:   item.Runtime,
		})
		if err != nil {
			return nil, false
		}
		sessions = append(sessions, bound)
	}
	return sessions, true
}

func SaveHostCache(dir, target string, sessions []session.Session) error {
	contents, err := json.Marshal(hostCacheFile{
		SchemaVersion: cacheSchemaVersion,
		CollectedAt:   time.Now(),
		Sessions:      sessions,
	})
	if err != nil {
		return fmt.Errorf("encode host cache: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create host cache directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, "host-*.tmp")
	if err != nil {
		return fmt.Errorf("create host cache temp file: %w", err)
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("restrict host cache permissions: %w", err)
	}
	if _, err := temp.Write(contents); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write host cache: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close host cache: %w", err)
	}
	if err := os.Rename(temp.Name(), hostCacheFilePath(dir, target)); err != nil {
		return fmt.Errorf("replace host cache: %w", err)
	}
	return nil
}
