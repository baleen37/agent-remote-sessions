package app

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/baleen37/agent-remote-sessions/internal/output"
)

func TestRunRoutesInteractiveAndJSONSeparately(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/ars-test-config")
	deps, stdout, stderr := appDependencies()
	deps.LoadTopology = func(path string) ([]Host, error) {
		want := filepath.Join("/tmp/ars-test-config", "ars", "hosts")
		if path != want {
			t.Fatalf("LoadTopology(%q), want %q", path, want)
		}
		return []Host{{Target: LocalhostTarget, Local: true}, {Target: "server"}}, nil
	}
	calls := 0
	deps.RunInteractive = func(_ context.Context, hosts []Host) error {
		calls++
		if len(hosts) != 2 || !hosts[0].Local {
			t.Fatalf("hosts = %#v", hosts)
		}
		return nil
	}
	if code := Run(context.Background(), nil, deps); code != 0 {
		t.Fatalf("interactive = %d: %s", code, stderr)
	}
	if code := Run(context.Background(), []string{"list", "--json"}, deps); code != 0 {
		t.Fatalf("json = %d", code)
	}
	if calls != 1 || !strings.Contains(stdout.String(), `"schema_version":1`) {
		t.Fatalf("calls/output = %d %q", calls, stdout)
	}
}

func TestRunDoesNotExposeLocalConfigurationCommand(t *testing.T) {
	deps, stdout, stderr := appDependencies()
	if code := Run(context.Background(), []string{"--help"}, deps); code != 0 {
		t.Fatalf("help code = %d", code)
	}
	if strings.Contains(stdout.String(), "local set") {
		t.Fatalf("help still exposes local command: %q", stdout.String())
	}
	if code := Run(context.Background(), []string{"local", "set", "devbox"}, deps); code != 2 {
		t.Fatalf("removed command code = %d, want 2; stderr = %q", code, stderr.String())
	}
}

func TestRunSupportsOnlyTheInteractiveAndJSONCommandShapes(t *testing.T) {
	allHosts := []Host{{Target: LocalhostTarget, Local: true}, {Target: "server"}}
	tests := []struct {
		name         string
		args         []string
		wantSelected []Host
		wantJSON     bool
	}{
		{name: "all hosts interactive", args: nil, wantSelected: allHosts},
		{name: "local only", args: []string{LocalhostTarget}, wantSelected: []Host{{Target: LocalhostTarget, Local: true}}},
		{name: "one host interactive", args: []string{"server"}, wantSelected: []Host{{Target: "server"}}},
		{name: "all hosts JSON", args: []string{"list", "--json"}, wantSelected: allHosts, wantJSON: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var selected []Host
			deps, stdout, stderr := appDependencies()
			deps.LoadTopology = func(string) ([]Host, error) { return allHosts, nil }
			deps.Collect = func(ctx context.Context, hosts []Host) Result {
				selected = append([]Host(nil), hosts...)
				return healthyAppCollection(ctx, hosts)
			}
			deps.RunInteractive = func(_ context.Context, hosts []Host) error {
				selected = append([]Host(nil), hosts...)
				return nil
			}

			if code := Run(context.Background(), test.args, deps); code != 0 {
				t.Fatalf("Run() = %d, want 0; stderr = %q", code, stderr.String())
			}
			if !reflect.DeepEqual(selected, test.wantSelected) {
				t.Fatalf("selected hosts = %#v, want %#v", selected, test.wantSelected)
			}
			if got := strings.Contains(stdout.String(), `"schema_version":1`); got != test.wantJSON {
				t.Fatalf("JSON output = %v, want %v; stdout = %q", got, test.wantJSON, stdout.String())
			}
		})
	}
}

func TestRunPrintsHelpWithoutApplicationDependencies(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"top level", []string{"--help"}, "ars [host]"},
		{"remote", []string{"remote", "--help"}, "Usage:\n  ars remote add <host>"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), test.args, Dependencies{Stdout: &stdout, Stderr: &stderr})
			if code != 0 {
				t.Fatalf("Run() = %d, want 0; stderr = %q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), test.want) {
				t.Fatalf("stdout = %q, want %q", stdout.String(), test.want)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestRunAddsRemoteWithoutLoadingOrCollecting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/ars-test-config")
	var gotPath, gotTarget string
	deps, stdout, stderr := appDependencies()
	deps.AddHost = func(path, target string) error {
		gotPath, gotTarget = path, target
		return nil
	}
	deps.LoadTopology = func(string) ([]Host, error) {
		t.Fatal("LoadTopology called for remote add")
		return nil, nil
	}
	deps.Collect = func(context.Context, []Host) Result {
		t.Fatal("Collect called for remote add")
		return Result{}
	}
	deps.RunInteractive = func(context.Context, []Host) error {
		t.Fatal("TUI called for remote add")
		return nil
	}

	if code := Run(context.Background(), []string{"remote", "add", "devbox"}, deps); code != 0 {
		t.Fatalf("Run() = %d, want 0; stderr = %q", code, stderr.String())
	}
	wantPath := filepath.Join("/tmp/ars-test-config", "ars", "hosts")
	if gotPath != wantPath || gotTarget != "devbox" {
		t.Fatalf("AddHost(%q, %q), want (%q, %q)", gotPath, gotTarget, wantPath, "devbox")
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q; want empty", stdout.String(), stderr.String())
	}
}

func TestRunReportsConfigurationWriteFailures(t *testing.T) {
	deps, _, stderr := appDependencies()
	deps.AddHost = func(string, string) error { return errors.New("inventory unavailable") }
	if code := Run(context.Background(), []string{"remote", "add", "devbox"}, deps); code != 1 {
		t.Fatalf("Run() = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "unavailable") {
		t.Fatalf("stderr = %q, want write error", stderr.String())
	}
}

func TestRunRejectsInvalidUsageBeforeLoadingTopology(t *testing.T) {
	tests := [][]string{
		{"list"},
		{"--json"},
		{"list", "--json", "devbox"},
		{"devbox", "extra"},
		{"remote", "add"},
		{"remote", "add", "devbox", "extra"},
		{"local", "set"},
		{"local", "set", "devbox", "extra"},
	}
	for _, args := range tests {
		deps, _, stderr := appDependencies()
		deps.LoadTopology = func(string) ([]Host, error) {
			t.Fatal("LoadTopology called for invalid usage")
			return nil, nil
		}
		if code := Run(context.Background(), args, deps); code != 2 {
			t.Fatalf("Run(%q) = %d, want 2", args, code)
		}
		if !strings.Contains(stderr.String(), "usage:") {
			t.Fatalf("stderr = %q, want usage", stderr.String())
		}
	}
}

func TestRunInvalidUsageIncludesConfigurationSyntax(t *testing.T) {
	deps, _, stderr := appDependencies()
	if code := Run(context.Background(), []string{"remote", "add"}, deps); code != 2 {
		t.Fatalf("Run() = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "ars remote add <host>") ||
		strings.Contains(stderr.String(), "ars local set <host>") {
		t.Fatalf("stderr = %q, want only remote add configuration syntax", stderr.String())
	}
}

func TestRunReportsUnknownHostAsInvalidArgument(t *testing.T) {
	deps, _, stderr := appDependencies()
	deps.LoadTopology = func(string) ([]Host, error) { return []Host{{Target: LocalhostTarget, Local: true}}, nil }
	deps.Collect = func(context.Context, []Host) Result {
		t.Fatal("Collect called for unknown host")
		return Result{}
	}
	deps.RunInteractive = func(context.Context, []Host) error {
		t.Fatal("TUI called for unknown host")
		return nil
	}

	if code := Run(context.Background(), []string{"unknown"}, deps); code != 2 {
		t.Fatalf("Run() = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "not configured") {
		t.Fatalf("stderr = %q, want configured-host error", stderr.String())
	}
}

func TestRunOpensInteractiveWithoutPrecollecting(t *testing.T) {
	deps, _, stderr := appDependencies()
	deps.Collect = func(context.Context, []Host) Result {
		t.Fatal("Collect called before TUI")
		return Result{}
	}
	called := false
	deps.RunInteractive = func(_ context.Context, hosts []Host) error {
		called = len(hosts) == 2
		return nil
	}
	if code := Run(context.Background(), nil, deps); code != 0 || !called {
		t.Fatalf("code/called = %d/%v: %s", code, called, stderr)
	}
}

func TestRunJSONCollectsOnceWithoutInteractiveDependencies(t *testing.T) {
	deps, stdout, stderr := appDependencies()
	deps.RunInteractive = nil
	collects := 0
	deps.Collect = func(ctx context.Context, hosts []Host) Result {
		collects++
		return healthyAppCollection(ctx, hosts)
	}

	if code := Run(context.Background(), []string{"list", "--json"}, deps); code != 0 {
		t.Fatalf("Run() = %d, want 0; stderr = %q", code, stderr.String())
	}
	if collects != 1 || !strings.Contains(stdout.String(), `"schema_version":1`) {
		t.Fatalf("collects/output = %d/%q", collects, stdout.String())
	}
}

func TestRunJSONWritesAllHostFailureBeforeReturningFailure(t *testing.T) {
	deps, stdout, stderr := appDependencies()
	deps.Collect = func(context.Context, []Host) Result {
		return Result{
			Hosts:  []output.HostResult{{Target: "down", Status: output.HostStatusError}},
			Errors: []output.HostError{{Host: "down", Code: "ssh_timeout", Message: "SSH collection timed out"}},
		}
	}
	if code := Run(context.Background(), []string{"list", "--json"}, deps); code != 1 {
		t.Fatalf("Run() = %d, want 1", code)
	}
	if !strings.Contains(stdout.String(), `"schema_version":1`) ||
		!strings.Contains(stderr.String(), "all selected hosts failed") {
		t.Fatalf("stdout/stderr = %q/%q", stdout.String(), stderr.String())
	}
}

func TestRunReportsInteractiveFailure(t *testing.T) {
	deps, _, stderr := appDependencies()
	deps.RunInteractive = func(context.Context, []Host) error { return errors.New("terminal unavailable") }
	if code := Run(context.Background(), nil, deps); code != 1 {
		t.Fatalf("Run() = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "run TUI: terminal unavailable") {
		t.Fatalf("stderr = %q, want TUI error", stderr.String())
	}
}

func TestRunMapsTopologyAndJSONWriteFailuresToGenericExit(t *testing.T) {
	t.Run("topology", func(t *testing.T) {
		deps, _, stderr := appDependencies()
		deps.LoadTopology = func(string) ([]Host, error) { return nil, errors.New("topology unavailable") }
		if code := Run(context.Background(), nil, deps); code != 1 {
			t.Fatalf("Run() = %d, want 1", code)
		}
		if !strings.Contains(stderr.String(), "topology unavailable") {
			t.Fatalf("stderr = %q, want topology error", stderr.String())
		}
	})

	t.Run("JSON writer", func(t *testing.T) {
		deps, _, stderr := appDependencies()
		deps.Stdout = failingWriter{}
		if code := Run(context.Background(), []string{"list", "--json"}, deps); code != 1 {
			t.Fatalf("Run() = %d, want 1", code)
		}
		if !strings.Contains(stderr.String(), "write JSON output") {
			t.Fatalf("stderr = %q, want JSON error", stderr.String())
		}
	})
}

func appDependencies() (Dependencies, *bytes.Buffer, *bytes.Buffer) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	return Dependencies{
		LoadTopology: func(string) ([]Host, error) {
			return []Host{{Target: LocalhostTarget, Local: true}, {Target: "server"}}, nil
		},
		AddHost: func(string, string) error { return nil },
		Collect: healthyAppCollection,
		RunInteractive: func(context.Context, []Host) error {
			return nil
		},
		Stdout: stdout,
		Stderr: stderr,
	}, stdout, stderr
}

func healthyAppCollection(_ context.Context, hosts []Host) Result {
	results := make([]output.HostResult, len(hosts))
	for index, host := range hosts {
		results[index] = output.HostResult{Target: host.Target, Status: output.HostOK}
	}
	return Result{Hosts: results}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
