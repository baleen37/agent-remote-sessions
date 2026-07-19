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
