package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

const (
	claudeID = "123e4567-e89b-42d3-a456-426614174000"
	codexID  = "0195f5dc-9e3f-7c26-8000-0123456789ab"
)

func TestKeyIsStableAndSeparatesProvider(t *testing.T) {
	a := Key("claude", claudeID)
	b := Key("codex", claudeID)
	if a == b || !strings.HasPrefix(a, "ars-") || len(a) != 68 {
		t.Fatalf("keys = %q %q", a, b)
	}
	if a != Key("claude", claudeID) {
		t.Fatal("unstable key")
	}
}

func TestInspectMapsExactRuntimeRows(t *testing.T) {
	candidates := []session.Candidate{
		candidate(session.Claude, claudeID),
		candidate(session.Codex, codexID),
	}
	runner := &fakeRunner{output: []byte(
		Key("claude", claudeID) + "\t0\t10\n" +
			Key("codex", codexID) + "\t2\t20\n" +
			"unowned\t1\t30\n")}

	got, report := Inspect(context.Background(), runner, candidates)

	if report != (Report{Status: StatusOK}) {
		t.Fatalf("report = %#v", report)
	}
	if got[Key("claude", claudeID)] != (session.Runtime{
		State:     session.RuntimeRunning,
		StartedAt: time.Unix(10, 0).UTC(),
	}) {
		t.Fatalf("claude = %#v", got)
	}
	if got[Key("codex", codexID)] != (session.Runtime{
		State:           session.RuntimeAttached,
		AttachedClients: 2,
		StartedAt:       time.Unix(20, 0).UTC(),
	}) {
		t.Fatalf("codex = %#v", got)
	}
	if _, ok := got["unowned"]; ok {
		t.Fatalf("unowned tmux session returned: %#v", got)
	}
	wantCommand := Command{
		Name: "tmux",
		Args: []string{"-L", SocketName, "-f", "/dev/null", "list-sessions", "-F",
			"#{session_name}\t#{session_attached}\t#{session_created}"},
		Env: []string{"TMUX=", "TMUX_PANE=", "TMUX_TMPDIR=/tmp"},
	}
	if !reflect.DeepEqual(runner.command, wantCommand) {
		t.Fatalf("command = %#v, want %#v", runner.command, wantCommand)
	}
}

func TestInspectDistinguishesEmptyUnavailableAndFailed(t *testing.T) {
	candidates := []session.Candidate{candidate(session.Claude, claudeID)}
	key := Key("claude", claudeID)
	tests := []struct {
		name       string
		runner     Runner
		wantReport Report
	}{
		{
			name:       "empty ARS server",
			runner:     &fakeRunner{err: inspectExitError{code: 1}},
			wantReport: Report{Status: StatusOK},
		},
		{
			name:       "tmux unavailable",
			runner:     &fakeRunner{err: errors.Join(errors.New("start tmux"), exec.ErrNotFound)},
			wantReport: Report{Status: StatusUnavailable, ErrorCode: "tmux_unavailable"},
		},
		{
			name:       "tmux command failed",
			runner:     &fakeRunner{err: inspectExitError{code: 2}},
			wantReport: Report{Status: StatusFailed, ErrorCode: "tmux_failed"},
		},
		{
			name:       "nil runner",
			runner:     nil,
			wantReport: Report{Status: StatusFailed, ErrorCode: "tmux_failed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, report := Inspect(context.Background(), tt.runner, candidates)
			if report != tt.wantReport {
				t.Fatalf("report = %#v, want %#v", report, tt.wantReport)
			}
			if !reflect.DeepEqual(got, map[string]session.Runtime{
				key: {State: session.RuntimeSaved},
			}) {
				t.Fatalf("runtimes = %#v", got)
			}
		})
	}
}

func TestInspectRejectsMalformedDuplicateAndOversizedOutput(t *testing.T) {
	key := Key("claude", claudeID)
	tests := []struct {
		name   string
		output []byte
	}{
		{name: "missing field", output: []byte(key + "\t0\n")},
		{name: "extra field", output: []byte(key + "\t0\t10\textra\n")},
		{name: "negative clients", output: []byte(key + "\t-1\t10\n")},
		{name: "invalid clients", output: []byte(key + "\tx\t10\n")},
		{name: "zero creation time", output: []byte(key + "\t0\t0\n")},
		{name: "invalid creation time", output: []byte(key + "\t0\tx\n")},
		{name: "duplicate row", output: []byte(key + "\t0\t10\n" + key + "\t0\t10\n")},
		{name: "blank row", output: []byte("\n")},
		{name: "oversized", output: bytes.Repeat([]byte("x"), maxInspectOutputBytes+1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, report := Inspect(context.Background(), &fakeRunner{output: tt.output}, []session.Candidate{
				candidate(session.Claude, claudeID),
			})
			if report != (Report{Status: StatusFailed, ErrorCode: "tmux_failed"}) {
				t.Fatalf("report = %#v", report)
			}
			if got[key] != (session.Runtime{State: session.RuntimeSaved}) {
				t.Fatalf("runtimes = %#v", got)
			}
		})
	}
}

func TestSystemRunnerBoundsOutput(t *testing.T) {
	runner := SystemRunner{}
	help := Command{
		Name: os.Args[0],
		Args: []string{"-test.run=TestRuntimeHelperProcess", "--"},
		Env:  []string{"GO_WANT_RUNTIME_HELPER=1"},
	}

	output, err := runner.Output(context.Background(), help)
	if err != nil || string(output) != "small output" {
		t.Fatalf("Output() = (%q, %v)", output, err)
	}

	help.Env = append(help.Env, "GO_RUNTIME_HELPER_LARGE=1")
	if _, err := runner.Output(context.Background(), help); !errors.Is(err, errInspectOutputLimit) {
		t.Fatalf("Output() error = %v, want output limit", err)
	}
}

func TestSystemRunnerRunsWithExplicitIO(t *testing.T) {
	help := Command{
		Name: os.Args[0],
		Args: []string{"-test.run=TestRuntimeHelperProcess", "--"},
		Env:  []string{"GO_WANT_RUNTIME_HELPER=1", "GO_RUNTIME_HELPER_COPY=1"},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := (SystemRunner{}).Run(context.Background(), help, strings.NewReader("input"), &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stdout.String() != "input" || stderr.String() != "copied" {
		t.Fatalf("Run() output = (%q, %q)", stdout.String(), stderr.String())
	}
}

func TestRuntimeHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_RUNTIME_HELPER") != "1" {
		return
	}
	if os.Getenv("GO_RUNTIME_HELPER_LARGE") == "1" {
		_, _ = io.CopyN(os.Stdout, zeroReader{}, maxInspectOutputBytes+1)
		os.Exit(0)
	}
	if os.Getenv("GO_RUNTIME_HELPER_COPY") == "1" {
		_, _ = io.Copy(os.Stdout, os.Stdin)
		_, _ = io.WriteString(os.Stderr, "copied")
		os.Exit(0)
	}
	_, _ = io.WriteString(os.Stdout, "small output")
	os.Exit(0)
}

func candidate(provider session.Provider, nativeID string) session.Candidate {
	return session.Candidate{Provider: provider, NativeID: nativeID}
}

type fakeRunner struct {
	output  []byte
	err     error
	command Command
}

func (runner *fakeRunner) Output(_ context.Context, command Command) ([]byte, error) {
	runner.command = command
	return runner.output, runner.err
}

func (*fakeRunner) Run(context.Context, Command, io.Reader, io.Writer, io.Writer) error {
	return errors.New("unexpected Run call")
}

type inspectExitError struct{ code int }

func (err inspectExitError) Error() string { return "tmux exited" }
func (err inspectExitError) ExitCode() int { return err.code }

type zeroReader struct{}

func (zeroReader) Read(output []byte) (int, error) {
	clear(output)
	return len(output), nil
}
