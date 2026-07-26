package app

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// settingKeyPreviewPct is the known key for the preview split percentage.
// SaveSettings only ever rewrites known keys, so unrecognized ones — written
// by a future version — survive a load/save round trip untouched.
const settingKeyPreviewPct = "preview_pct"

// SettingsPath resolves the plain-text settings file: $XDG_CONFIG_HOME/ars,
// falling back to ~/.config/ars, mirroring ConfigPath's inventory file.
func SettingsPath() (string, error) {
	return configFilePath("settings")
}

// LoadSettings reads path as key=value lines into a map. A missing file
// returns an empty map; a parse error is swallowed rather than surfaced, so
// callers can fall back to defaults instead of blocking the TUI on a broken
// settings file.
func LoadSettings(path string) map[string]string {
	settings := make(map[string]string)
	file, err := os.Open(path)
	if err != nil {
		return settings
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		settings[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if scanner.Err() != nil {
		return make(map[string]string)
	}
	return settings
}

// SaveSettings writes known keys into the settings at path, preserving any
// unrecognized key already there so a future version's settings survive an
// older version's save.
func SaveSettings(path string, updates map[string]string) error {
	settings := LoadSettings(path)
	for key, value := range updates {
		settings[key] = value
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	var builder strings.Builder
	for key, value := range settings {
		fmt.Fprintf(&builder, "%s=%s\n", key, value)
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return nil
}

// LoadPreviewPct reads the preview_pct setting, falling back to fallback when
// the file is missing, the key is absent, or the value fails to parse.
func LoadPreviewPct(path string, fallback int) int {
	settings := LoadSettings(path)
	raw, ok := settings[settingKeyPreviewPct]
	if !ok {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

// SavePreviewPct persists the preview_pct setting at path.
func SavePreviewPct(path string, pct int) error {
	return SaveSettings(path, map[string]string{settingKeyPreviewPct: strconv.Itoa(pct)})
}
