package app

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/output"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func TestRunSupportsOnlyTheThreeCommandShapes(t *testing.T) {
	allHosts := []Host{{Target: "devbox"}, {Target: "agent-mac"}}
	item := appSession("devbox")
	tests := []struct {
		name          string
		args          []string
		wantCollected []Host
		wantPick      bool
		wantResume    bool
		wantJSON      bool
	}{
		{name: "all hosts interactive", args: nil, wantCollected: allHosts, wantPick: true, wantResume: true},
		{name: "one host interactive", args: []string{"devbox"}, wantCollected: []Host{{Target: "devbox"}}, wantPick: true, wantResume: true},
		{name: "all hosts JSON", args: []string{"list", "--json"}, wantCollected: allHosts, wantJSON: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var collected []Host
			pickCalls := 0
			resumeCalls := 0
			deps, stdout, stderr := appDependencies()
			deps.LoadHosts = func(string) ([]Host, error) { return allHosts, nil }
			deps.Collect = func(_ context.Context, hosts []Host) Result {
				collected = append([]Host(nil), hosts...)
				return Result{
					Hosts:    []output.HostResult{{Target: "devbox", Status: output.HostOK}},
					Sessions: []session.Session{item},
				}
			}
			deps.Pick = func(_ context.Context, sessions []session.Session) (session.Session, bool, error) {
				pickCalls++
				if !reflect.DeepEqual(sessions, []session.Session{item}) {
					t.Fatalf("Pick sessions = %#v, want selected sessions", sessions)
				}
				return item, true, nil
			}
			deps.Resume = func(_ context.Context, got session.Session) error {
				resumeCalls++
				if got != item {
					t.Fatalf("Resume session = %#v, want %#v", got, item)
				}
				return nil
			}

			if code := Run(context.Background(), test.args, deps); code != 0 {
				t.Fatalf("Run() = %d, want 0; stderr = %q", code, stderr.String())
			}
			if !reflect.DeepEqual(collected, test.wantCollected) {
				t.Fatalf("collected hosts = %#v, want %#v", collected, test.wantCollected)
			}
			if got := pickCalls > 0; got != test.wantPick {
				t.Fatalf("Pick called = %v, want %v", got, test.wantPick)
			}
			if got := resumeCalls > 0; got != test.wantResume {
				t.Fatalf("Resume called = %v, want %v", got, test.wantResume)
			}
			if got := strings.Contains(stdout.String(), `"schema_version":1`); got != test.wantJSON {
				t.Fatalf("JSON output = %v, want %v; stdout = %q", got, test.wantJSON, stdout.String())
			}
		})
	}
}

func TestRunRejectsInvalidUsageBeforeLoadingInventory(t *testing.T) {
	tests := [][]string{
		{"list"},
		{"--json"},
		{"list", "--json", "devbox"},
		{"devbox", "extra"},
	}
	for _, args := range tests {
		deps, _, stderr := appDependencies()
		deps.LoadHosts = func(string) ([]Host, error) {
			t.Fatal("LoadHosts called for invalid usage")
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

func TestRunReportsUnknownHostAsInvalidArgument(t *testing.T) {
	deps, _, stderr := appDependencies()
	deps.LoadHosts = func(string) ([]Host, error) { return []Host{{Target: "devbox"}}, nil }
	deps.Collect = func(context.Context, []Host) Result {
		t.Fatal("Collect called for unknown host")
		return Result{}
	}

	if code := Run(context.Background(), []string{"unknown"}, deps); code != 2 {
		t.Fatalf("Run() = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "not configured") {
		t.Fatalf("stderr = %q, want configured-host error", stderr.String())
	}
}

func TestRunKeepsHealthySessionsWhenOneHostFails(t *testing.T) {
	item := appSession("healthy")
	deps, _, stderr := appDependencies()
	deps.Collect = func(context.Context, []Host) Result {
		return Result{
			Hosts: []output.HostResult{
				{Target: "healthy", Status: output.HostOK},
				{Target: "down", Status: output.HostStatusError},
			},
			Sessions: []session.Session{item},
			Errors:   []output.HostError{{Host: "down", Code: "ssh_failed", Message: "SSH collection failed"}},
		}
	}
	picked := false
	deps.Pick = func(_ context.Context, sessions []session.Session) (session.Session, bool, error) {
		picked = true
		return sessions[0], true, nil
	}

	if code := Run(context.Background(), nil, deps); code != 0 {
		t.Fatalf("Run() = %d, want 0; stderr = %q", code, stderr.String())
	}
	if !picked || !strings.Contains(stderr.String(), "down: SSH collection failed (ssh_failed)") {
		t.Fatalf("partial failure = (picked %v, stderr %q)", picked, stderr.String())
	}
}

func TestRunStopsBeforePickerWhenAllHostsFail(t *testing.T) {
	deps, _, stderr := appDependencies()
	deps.Collect = func(context.Context, []Host) Result {
		return Result{
			Hosts:  []output.HostResult{{Target: "down", Status: output.HostStatusError}},
			Errors: []output.HostError{{Host: "down", Code: "ssh_timeout", Message: "SSH collection timed out"}},
		}
	}
	deps.Pick = func(context.Context, []session.Session) (session.Session, bool, error) {
		t.Fatal("Pick called when all hosts failed")
		return session.Session{}, false, nil
	}

	if code := Run(context.Background(), nil, deps); code != 1 {
		t.Fatalf("Run() = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "down: SSH collection timed out (ssh_timeout)") ||
		!strings.Contains(stderr.String(), "all selected hosts failed") {
		t.Fatalf("stderr = %q, want host and aggregate errors", stderr.String())
	}
}

func TestRunWritesHealthyEmptyJSONSuccessfully(t *testing.T) {
	deps, stdout, stderr := appDependencies()
	deps.Collect = func(context.Context, []Host) Result {
		return Result{Hosts: []output.HostResult{{Target: "devbox", Status: output.HostOK}}}
	}
	deps.Pick = func(context.Context, []session.Session) (session.Session, bool, error) {
		t.Fatal("Pick called in JSON mode")
		return session.Session{}, false, nil
	}

	if code := Run(context.Background(), []string{"list", "--json"}, deps); code != 0 {
		t.Fatalf("Run() = %d, want 0; stderr = %q", code, stderr.String())
	}
	want := "{\"schema_version\":1,\"hosts\":[{\"target\":\"devbox\",\"status\":\"ok\"}],\"sessions\":[],\"errors\":[]}\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunJSONDoesNotRequireInteractiveDependencies(t *testing.T) {
	deps, _, stderr := appDependencies()
	deps.Pick = nil
	deps.Resume = nil

	if code := Run(context.Background(), []string{"list", "--json"}, deps); code != 0 {
		t.Fatalf("Run() = %d, want 0; stderr = %q", code, stderr.String())
	}
}

func TestRunReportsInteractiveNoSessions(t *testing.T) {
	deps, stdout, stderr := appDependencies()
	deps.Collect = func(context.Context, []Host) Result {
		return Result{Hosts: []output.HostResult{{Target: "devbox", Status: output.HostOK}}}
	}
	deps.Pick = func(context.Context, []session.Session) (session.Session, bool, error) {
		t.Fatal("Pick called without sessions")
		return session.Session{}, false, nil
	}

	if code := Run(context.Background(), nil, deps); code != 0 {
		t.Fatalf("Run() = %d, want 0", code)
	}
	if stdout.String() != "No sessions found.\n" || stderr.Len() != 0 {
		t.Fatalf("output = (stdout %q, stderr %q)", stdout.String(), stderr.String())
	}
}

func TestRunHandlesPickerCancellationAndFailure(t *testing.T) {
	tests := []struct {
		name     string
		pick     func(context.Context, []session.Session) (session.Session, bool, error)
		wantCode int
	}{
		{name: "cancellation", pick: func(context.Context, []session.Session) (session.Session, bool, error) {
			return session.Session{}, false, nil
		}, wantCode: 0},
		{name: "failure", pick: func(context.Context, []session.Session) (session.Session, bool, error) {
			return session.Session{}, false, errors.New("fzf missing")
		}, wantCode: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps, _, stderr := appDependencies()
			deps.Collect = healthyAppCollection
			deps.Pick = test.pick
			deps.Resume = func(context.Context, session.Session) error {
				t.Fatal("Resume called after picker cancellation or failure")
				return nil
			}
			if code := Run(context.Background(), nil, deps); code != test.wantCode {
				t.Fatalf("Run() = %d, want %d", code, test.wantCode)
			}
			if test.wantCode == 1 && !strings.Contains(stderr.String(), "fzf missing") {
				t.Fatalf("stderr = %q, want picker error", stderr.String())
			}
		})
	}
}

func TestRunMapsResumeFailuresToGenericOrExactExitCode(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{name: "generic", err: errors.New("resume failed"), wantCode: 1},
		{name: "SSH exit", err: appExitError{code: 42}, wantCode: 42},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps, _, stderr := appDependencies()
			deps.Collect = healthyAppCollection
			deps.Resume = func(context.Context, session.Session) error { return test.err }
			if code := Run(context.Background(), nil, deps); code != test.wantCode {
				t.Fatalf("Run() = %d, want %d", code, test.wantCode)
			}
			if !strings.Contains(stderr.String(), test.err.Error()) {
				t.Fatalf("stderr = %q, want resume error", stderr.String())
			}
		})
	}
}

func TestRunMapsInventoryAndJSONWriteFailuresToGenericExit(t *testing.T) {
	t.Run("inventory", func(t *testing.T) {
		deps, _, stderr := appDependencies()
		deps.LoadHosts = func(string) ([]Host, error) { return nil, errors.New("inventory unavailable") }
		if code := Run(context.Background(), nil, deps); code != 1 {
			t.Fatalf("Run() = %d, want 1", code)
		}
		if !strings.Contains(stderr.String(), "inventory unavailable") {
			t.Fatalf("stderr = %q, want inventory error", stderr.String())
		}
	})

	t.Run("JSON writer", func(t *testing.T) {
		deps, _, stderr := appDependencies()
		deps.Collect = func(context.Context, []Host) Result {
			return Result{Hosts: []output.HostResult{{Target: "devbox", Status: output.HostOK}}}
		}
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
	item := appSession("devbox")
	return Dependencies{
		LoadHosts: func(string) ([]Host, error) { return []Host{{Target: "devbox"}}, nil },
		Collect:   healthyAppCollection,
		Pick: func(context.Context, []session.Session) (session.Session, bool, error) {
			return item, true, nil
		},
		Resume: func(context.Context, session.Session) error { return nil },
		Stdout: stdout,
		Stderr: stderr,
	}, stdout, stderr
}

func healthyAppCollection(context.Context, []Host) Result {
	return Result{
		Hosts:    []output.HostResult{{Target: "devbox", Status: output.HostOK}},
		Sessions: []session.Session{appSession("devbox")},
	}
}

func appSession(host string) session.Session {
	return session.Session{Host: host, Candidate: session.Candidate{
		Provider:  session.Claude,
		NativeID:  "123e4567-e89b-42d3-a456-426614174000",
		UpdatedAt: time.Unix(1, 0).UTC(),
		CWD:       "/work/app",
		Title:     "title",
	}}
}

type appExitError struct{ code int }

func (err appExitError) Error() string { return "ssh exited" }
func (err appExitError) ExitCode() int { return err.code }

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
