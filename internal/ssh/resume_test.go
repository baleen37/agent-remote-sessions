package ssh

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func TestResumeRunsClaudeWithInteractiveSSHAndQuotedData(t *testing.T) {
	adapter, ok := provider.Lookup(session.Claude)
	if !ok {
		t.Fatal("Claude adapter is unavailable")
	}
	runner := &resumeRunner{}
	item := resumeSession("user@host;$literal", session.Claude, "/work/it's app")

	err := Resume(context.Background(), runner, item.Host, item, adapter)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	wantArgs := []string{
		"-tt",
		"user@host;$literal",
		"cd '/work/it'\\''s app' && exec claude --resume '123e4567-e89b-42d3-a456-426614174000'",
	}
	if runner.name != "ssh" || !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("runner = (%q, %#v), want (ssh, %#v)", runner.name, runner.args, wantArgs)
	}
	if runner.stdin != os.Stdin || runner.stdout != os.Stdout || runner.stderr != os.Stderr {
		t.Fatal("Resume() did not preserve interactive standard streams")
	}
}

func TestResumeRunsCodexWithFixedNativeCommand(t *testing.T) {
	adapter, ok := provider.Lookup(session.Codex)
	if !ok {
		t.Fatal("Codex adapter is unavailable")
	}
	runner := &resumeRunner{}
	item := resumeSession("devbox", session.Codex, "/work/app")

	err := Resume(context.Background(), runner, item.Host, item, adapter)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	want := []string{
		"-tt",
		"devbox",
		"cd '/work/app' && exec codex resume '123e4567-e89b-42d3-a456-426614174000'",
	}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("ssh args = %#v, want %#v", runner.args, want)
	}
}

func TestResumeRejectsUntrustedSessionAndAdapterBeforeSSH(t *testing.T) {
	claude, _ := provider.Lookup(session.Claude)
	codex, _ := provider.Lookup(session.Codex)
	tests := []struct {
		name    string
		target  string
		item    session.Session
		adapter provider.Adapter
	}{
		{name: "session host differs from target", target: "other", item: resumeSession("devbox", session.Claude, "/work/app"), adapter: claude},
		{name: "adapter differs from provider", target: "devbox", item: resumeSession("devbox", session.Claude, "/work/app"), adapter: codex},
		{name: "unsupported provider", target: "devbox", item: resumeSession("devbox", "other", "/work/app"), adapter: unsafeAdapter{name: "other"}},
		{name: "noncanonical UUID", target: "devbox", item: withResumeID(resumeSession("devbox", session.Claude, "/work/app"), "123E4567-E89B-42D3-A456-426614174000"), adapter: claude},
		{name: "relative CWD", target: "devbox", item: resumeSession("devbox", session.Claude, "work/app"), adapter: claude},
		{name: "unsafe resume spec", target: "devbox", item: resumeSession("devbox", session.Claude, "/work/app"), adapter: unsafeAdapter{name: session.Claude}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &resumeRunner{}
			if err := Resume(context.Background(), runner, test.target, test.item, test.adapter); err == nil {
				t.Fatal("Resume() error = nil, want non-nil")
			}
			if runner.called {
				t.Fatal("Resume() invoked SSH for invalid input")
			}
		})
	}
}

func TestResumePreservesSSHExitCode(t *testing.T) {
	adapter, _ := provider.Lookup(session.Claude)
	runErr := resumeExitError{code: 42}
	err := Resume(
		context.Background(),
		&resumeRunner{err: runErr},
		"devbox",
		resumeSession("devbox", session.Claude, "/work/app"),
		adapter,
	)
	if err == nil {
		t.Fatal("Resume() error = nil, want non-nil")
	}
	if !errors.Is(err, runErr) {
		t.Fatalf("Resume() error = %v, want wrapped SSH error", err)
	}
	exitError, ok := err.(interface{ ExitCode() int })
	if !ok || exitError.ExitCode() != 42 {
		t.Fatalf("Resume() error exit code = (%v, %v), want 42", exitError, ok)
	}
}

type resumeRunner struct {
	called bool
	name   string
	args   []string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	err    error
}

func (runner *resumeRunner) Run(_ context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	runner.called = true
	runner.name = name
	runner.args = append([]string(nil), args...)
	runner.stdin = stdin
	runner.stdout = stdout
	runner.stderr = stderr
	return runner.err
}

type resumeExitError struct{ code int }

func (err resumeExitError) Error() string { return "ssh exited" }
func (err resumeExitError) ExitCode() int { return err.code }

type unsafeAdapter struct{ name session.Provider }

func (adapter unsafeAdapter) Name() session.Provider { return adapter.name }
func (unsafeAdapter) Discover(context.Context, string) provider.Result {
	return provider.Result{}
}
func (unsafeAdapter) ValidateID(string) error { return nil }
func (unsafeAdapter) Resume(string) (provider.ResumeSpec, error) {
	return provider.ResumeSpec{Executable: "sh", Args: []string{"-c", "evil"}}, nil
}

func resumeSession(host string, providerName session.Provider, cwd string) session.Session {
	return session.Session{Host: host, Candidate: session.Candidate{
		Provider:  providerName,
		NativeID:  "123e4567-e89b-42d3-a456-426614174000",
		UpdatedAt: time.Unix(1, 0),
		CWD:       cwd,
		Title:     "title",
	}}
}

func withResumeID(item session.Session, id string) session.Session {
	item.NativeID = id
	return item
}
