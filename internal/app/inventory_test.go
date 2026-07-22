package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestConfigPathUsesXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/ars-xdg")
	t.Setenv("HOME", "/tmp/ars-home")

	got, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath() error = %v", err)
	}
	want := filepath.Join("/tmp/ars-xdg", "ars", "hosts")
	if got != want {
		t.Fatalf("ConfigPath() = %q, want %q", got, want)
	}
}

func TestConfigPathFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/tmp/ars-home")

	got, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath() error = %v", err)
	}
	want := filepath.Join("/tmp/ars-home", ".config", "ars", "hosts")
	if got != want {
		t.Fatalf("ConfigPath() = %q, want %q", got, want)
	}
}

func TestLoadTopologyPrependsImplicitLocalhost(t *testing.T) {
	path := writeInventory(t, "devbox\nserver\n")
	got, err := LoadTopology(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []Host{
		{Target: LocalhostTarget, Local: true},
		{Target: "devbox"},
		{Target: "server"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadTopology() = %#v, want %#v", got, want)
	}
}

func TestLoadTopologyAllowsMissingRemoteInventory(t *testing.T) {
	got, err := LoadTopology(filepath.Join(t.TempDir(), "missing", "hosts"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Host{{Target: LocalhostTarget, Local: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadTopology() = %#v, want %#v", got, want)
	}
}

func TestLoadTopologyRejectsDanglingRemoteInventorySymlink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.Symlink("missing-hosts", path); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	got, err := LoadTopology(path)
	if err == nil || !strings.Contains(err.Error(), "open host inventory") {
		t.Fatalf("LoadTopology() error = %v, want open host inventory error", err)
	}
	if got != nil {
		t.Fatalf("LoadTopology() hosts = %#v, want nil", got)
	}
}

func TestLoadTopologyRejectsDanglingRemoteInventoryParentSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("missing-ars", filepath.Join(dir, "ars")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	got, err := LoadTopology(filepath.Join(dir, "ars", "hosts"))
	if err == nil || !strings.Contains(err.Error(), "open host inventory") {
		t.Fatalf("LoadTopology() error = %v, want open host inventory error", err)
	}
	if got != nil {
		t.Fatalf("LoadTopology() hosts = %#v, want nil", got)
	}
}

func TestLoadTopologyAllowsMissingRemoteInventoryUnderExistingParentSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "existing-ars"), 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.Symlink("existing-ars", filepath.Join(dir, "ars")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	got, err := LoadTopology(filepath.Join(dir, "ars", "hosts"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Host{{Target: LocalhostTarget, Local: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadTopology() = %#v, want %#v", got, want)
	}
}

func TestRemoteInventoryRejectsReservedLocalhost(t *testing.T) {
	path := writeInventory(t, "devbox\nlocalhost\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "localhost is reserved") {
		t.Fatalf("Load() error = %v", err)
	}
	if err := Add(filepath.Join(t.TempDir(), "hosts"), LocalhostTarget); err == nil ||
		!strings.Contains(err.Error(), "localhost is reserved") {
		t.Fatalf("Add() error = %v", err)
	}
}

func TestLoadSkipsBlankLinesAndCommentsAndPreservesOrder(t *testing.T) {
	path := writeInventory(t, "# managed hosts\n\ndevbox\nuser@agent-mac\nagent;$literal\n")

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []Host{
		{Target: "devbox"},
		{Target: "user@agent-mac"},
		{Target: "agent;$literal"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestLoadRejectsDuplicateTargets(t *testing.T) {
	path := writeInventory(t, "devbox\nagent-mac\ndevbox\n")

	got, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want non-nil")
	}
	if got != nil {
		t.Fatalf("Load() hosts = %#v, want nil", got)
	}
}

func TestLoadRejectsInvalidTargetAndReturnsNoPartialInventory(t *testing.T) {
	tests := []struct {
		name   string
		target string
	}{
		{name: "begins with dash", target: "-oProxyCommand=evil"},
		{name: "leading whitespace", target: " devbox"},
		{name: "trailing whitespace", target: "devbox "},
		{name: "embedded whitespace", target: "dev box"},
		{name: "unicode whitespace", target: "dev\u00a0box"},
		{name: "control character", target: "dev\u007fbox"},
		{name: "invalid UTF-8", target: string([]byte{'d', 'e', 'v', 0xff})},
		{name: "too long", target: strings.Repeat("a", 256)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeInventory(t, "valid-host\n"+tt.target+"\nlater-host\n")
			got, err := Load(path)
			if err == nil {
				t.Fatal("Load() error = nil, want non-nil")
			}
			if got != nil {
				t.Fatalf("Load() hosts = %#v, want nil", got)
			}
			if !strings.Contains(err.Error(), "line 2") {
				t.Fatalf("Load() error = %q, want line number", err)
			}
		})
	}
}

func TestAddCreatesInventoryAndParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "ars", "hosts")
	if err := Add(path, "devbox"); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := string(contents), "devbox\n"; got != want {
		t.Fatalf("inventory = %q, want %q", got, want)
	}
}

func TestAddAppendsAndPreservesExistingInventory(t *testing.T) {
	tests := []struct {
		name, existing, want string
	}{
		{"trailing newline", "# managed\ndevbox\n", "# managed\ndevbox\nagent-mac\n"},
		{"missing trailing newline", "# managed\ndevbox", "# managed\ndevbox\nagent-mac\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeInventory(t, test.existing)
			if err := Add(path, "agent-mac"); err != nil {
				t.Fatalf("Add() error = %v", err)
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if got := string(contents); got != test.want {
				t.Fatalf("inventory = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAddRejectsDuplicateAndInvalidTargetsWithoutChangingInventory(t *testing.T) {
	tests := []struct {
		name, target, wantError string
	}{
		{"duplicate", "devbox", "already configured"},
		{"invalid", "bad host", "whitespace"},
		{"comment-like", "#devbox", "hash"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const original = "# managed\ndevbox\n"
			path := writeInventory(t, original)
			err := Add(path, test.target)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Add() error = %v, want %q", err, test.wantError)
			}
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("ReadFile() error = %v", readErr)
			}
			if got := string(contents); got != original {
				t.Fatalf("inventory changed to %q", got)
			}
		})
	}
}

func TestSelect(t *testing.T) {
	hosts := []Host{{Target: "devbox"}, {Target: "user@agent-mac"}}

	t.Run("empty target selects all in order", func(t *testing.T) {
		got, err := Select(hosts, "")
		if err != nil {
			t.Fatalf("Select() error = %v", err)
		}
		if !reflect.DeepEqual(got, hosts) {
			t.Fatalf("Select() = %#v, want %#v", got, hosts)
		}
	})

	t.Run("known target selects exactly one host", func(t *testing.T) {
		got, err := Select(hosts, "user@agent-mac")
		if err != nil {
			t.Fatalf("Select() error = %v", err)
		}
		want := []Host{{Target: "user@agent-mac"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Select() = %#v, want %#v", got, want)
		}
	})

	t.Run("unknown target fails", func(t *testing.T) {
		got, err := Select(hosts, "unknown")
		if err == nil {
			t.Fatal("Select() error = nil, want non-nil")
		}
		if got != nil {
			t.Fatalf("Select() = %#v, want nil", got)
		}
	})
}

func writeInventory(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
