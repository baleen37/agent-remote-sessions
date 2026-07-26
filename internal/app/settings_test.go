package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsPathUsesXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/ars-xdg")
	t.Setenv("HOME", "/tmp/ars-home")

	got, err := SettingsPath()
	if err != nil {
		t.Fatalf("SettingsPath() error = %v", err)
	}
	want := filepath.Join("/tmp/ars-xdg", "ars", "settings")
	if got != want {
		t.Fatalf("SettingsPath() = %q, want %q", got, want)
	}
}

func TestSettingsPathFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/tmp/ars-home")

	got, err := SettingsPath()
	if err != nil {
		t.Fatalf("SettingsPath() error = %v", err)
	}
	want := filepath.Join("/tmp/ars-home", ".config", "ars", "settings")
	if got != want {
		t.Fatalf("SettingsPath() = %q, want %q", got, want)
	}
}

func TestSavePreviewPctThenLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ars", "settings")
	if err := SavePreviewPct(path, 70); err != nil {
		t.Fatalf("SavePreviewPct() error = %v", err)
	}
	got := LoadPreviewPct(path, 65)
	if got != 70 {
		t.Fatalf("LoadPreviewPct() = %d, want 70", got)
	}
}

func TestSaveSettingsPreservesUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initial := "preview_pct=65\nfuture_key=some_value\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial settings: %v", err)
	}

	if err := SavePreviewPct(path, 40); err != nil {
		t.Fatalf("SavePreviewPct() error = %v", err)
	}

	settings := LoadSettings(path)
	if settings["preview_pct"] != "40" {
		t.Fatalf("preview_pct = %q, want 40", settings["preview_pct"])
	}
	if settings["future_key"] != "some_value" {
		t.Fatalf("future_key = %q, want it preserved as some_value", settings["future_key"])
	}
}

func TestLoadPreviewPctFallsBackWhenFileMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist")
	got := LoadPreviewPct(path, 65)
	if got != 65 {
		t.Fatalf("LoadPreviewPct() = %d, want fallback 65", got)
	}
}

func TestLoadPreviewPctFallsBackWhenValueUnparsable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings")
	if err := os.WriteFile(path, []byte("preview_pct=not-a-number\n"), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	got := LoadPreviewPct(path, 65)
	if got != 65 {
		t.Fatalf("LoadPreviewPct() = %d, want fallback 65", got)
	}
}

func TestLoadSettingsIgnoresCommentsAndBlankLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings")
	content := "# a comment\n\npreview_pct=55\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	settings := LoadSettings(path)
	if settings["preview_pct"] != "55" {
		t.Fatalf("preview_pct = %q, want 55", settings["preview_pct"])
	}
	if len(settings) != 1 {
		t.Fatalf("settings = %#v, want exactly one key", settings)
	}
}
