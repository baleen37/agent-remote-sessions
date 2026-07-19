package output

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func TestFZFSelectUsesFixedArgumentsAndOpaqueIndex(t *testing.T) {
	sessions := []session.Session{
		fzfSession("first", session.Claude, "123e4567-e89b-42d3-a456-426614174000", "/work/same", "duplicate"),
		fzfSession("second", session.Claude, "123e4567-e89b-42d3-a456-426614174001", "/work/same", "duplicate"),
	}
	runner := &fzfRunner{output: "1\n"}

	got, selected, err := (FZF{Runner: runner}).Select(context.Background(), sessions)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if !selected {
		t.Fatal("Select() selected = false, want true")
	}
	if !reflect.DeepEqual(got, sessions[1]) {
		t.Fatalf("Select() = %#v, want %#v", got, sessions[1])
	}
	if runner.name != "fzf" {
		t.Fatalf("runner name = %q, want fzf", runner.name)
	}
	wantArgs := []string{"--no-multi", "--delimiter=\t", "--with-nth=2..", "--accept-nth=1"}
	if !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("runner args = %#v, want %#v", runner.args, wantArgs)
	}
	if runner.stdin == nil || runner.stdout == nil || runner.stderr == nil {
		t.Fatal("runner did not receive all standard streams")
	}
}

func TestFZFSelectSanitizesUntrustedDisplayFields(t *testing.T) {
	sessions := []session.Session{{
		Host: "host\twith\nrows\x1b[31m|",
		Candidate: session.Candidate{
			Provider:  session.Claude,
			NativeID:  "123e4567-e89b-42d3-a456-426614174000",
			UpdatedAt: time.Unix(1, 0),
			CWD:       "/work/project\tname\r",
			Title:     "title\n\x1b[2J|tail",
		},
	}}
	runner := &fzfRunner{output: "0\n"}

	_, selected, err := (FZF{Runner: runner}).Select(context.Background(), sessions)
	if err != nil || !selected {
		t.Fatalf("Select() = (_, %v, %v), want selected", selected, err)
	}
	if strings.ContainsAny(runner.input, "\r\x1b") {
		t.Fatalf("fzf input contains terminal control: %q", runner.input)
	}
	lines := strings.Split(strings.TrimSuffix(runner.input, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("fzf input rows = %d, want 1: %q", len(lines), runner.input)
	}
	if strings.Count(lines[0], "\t") != 1 || !strings.HasPrefix(lines[0], "0\t") {
		t.Fatalf("fzf input row = %q, want one trusted delimiter after index", lines[0])
	}
}

func TestFZFSelectRejectsUntrustedOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{name: "empty", output: ""},
		{name: "malformed", output: "devbox\n"},
		{name: "negative", output: "-1\n"},
		{name: "out of range", output: "1\n"},
		{name: "extra field", output: "0\tdevbox\n"},
		{name: "extra line", output: "0\n1\n"},
	}
	sessions := []session.Session{fzfSession("host", session.Codex, "123e4567-e89b-42d3-a456-426614174000", "/work/app", "title")}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, selected, err := (FZF{Runner: &fzfRunner{output: test.output}}).Select(context.Background(), sessions)
			if err == nil {
				t.Fatal("Select() error = nil, want non-nil")
			}
			if selected || got != (session.Session{}) {
				t.Fatalf("Select() = (%#v, %v, _), want zero, false", got, selected)
			}
		})
	}
}

func TestFZFSelectTreatsExitOneAnd130AsCancellation(t *testing.T) {
	for _, runErr := range []error{fzfExitError{code: 1}, fmt.Errorf("wrapped: %w", fzfExitError{code: 130})} {
		t.Run(runErr.Error(), func(t *testing.T) {
			got, selected, err := (FZF{Runner: &fzfRunner{err: runErr}}).Select(
				context.Background(),
				[]session.Session{fzfSession("host", session.Claude, "123e4567-e89b-42d3-a456-426614174000", "/work/app", "title")},
			)
			if err != nil || selected || got != (session.Session{}) {
				t.Fatalf("Select() = (%#v, %v, %v), want zero, false, nil", got, selected, err)
			}
		})
	}
}

func TestFZFSelectReportsMissingExecutableAndOtherFailures(t *testing.T) {
	missing := errors.New("executable file not found")
	for _, runErr := range []error{missing, fzfExitError{code: 2}} {
		_, selected, err := (FZF{Runner: &fzfRunner{err: runErr}}).Select(
			context.Background(),
			[]session.Session{fzfSession("host", session.Claude, "123e4567-e89b-42d3-a456-426614174000", "/work/app", "title")},
		)
		if err == nil || selected {
			t.Fatalf("Select() = (_, %v, %v), want failure", selected, err)
		}
	}
}

type fzfRunner struct {
	name   string
	args   []string
	input  string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	output string
	err    error
}

func (runner *fzfRunner) Run(_ context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	runner.name = name
	runner.args = append([]string(nil), args...)
	runner.stdin = stdin
	runner.stdout = stdout
	runner.stderr = stderr
	if stdin != nil {
		data, _ := io.ReadAll(stdin)
		runner.input = string(data)
	}
	if runner.output != "" {
		_, _ = io.Copy(stdout, bytes.NewBufferString(runner.output))
	}
	return runner.err
}

type fzfExitError struct{ code int }

func (err fzfExitError) Error() string { return "fzf exited" }
func (err fzfExitError) ExitCode() int { return err.code }

func fzfSession(host string, providerName session.Provider, id, cwd, title string) session.Session {
	return session.Session{Host: host, Candidate: session.Candidate{
		Provider: providerName, NativeID: id, UpdatedAt: time.Unix(1, 0), CWD: cwd, Title: title,
	}}
}
