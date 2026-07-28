package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

type externalRunner struct {
	output  []byte
	err     error
	command Command
}

func (runner *externalRunner) Output(_ context.Context, command Command) ([]byte, error) {
	runner.command = command
	return runner.output, runner.err
}

func (*externalRunner) Run(context.Context, Command, io.Reader, io.Writer, io.Writer) error {
	return errors.New("unexpected Run")
}

func TestResolveExternalReturnsNoneForCompleteEmptyScan(t *testing.T) {
	runner := &externalRunner{output: []byte("none\n")}
	target, found, err := ResolveExternal(
		context.Background(),
		runner,
		session.Claude,
		"123e4567-e89b-42d3-a456-426614174000",
	)
	if err != nil || found || target != (ExternalTarget{}) {
		t.Fatalf("ResolveExternal() = (%#v, %t, %v), want zero, false, nil", target, found, err)
	}
	if runner.command.Name != "/bin/sh" ||
		!bytes.Equal([]byte(runner.command.Args[1]), []byte(ExternalResolverScript())) ||
		!bytes.Equal([]byte(runner.command.Args[2]), []byte("ars-external")) ||
		!bytes.Equal([]byte(runner.command.Args[3]), []byte("claude")) ||
		!bytes.Equal([]byte(runner.command.Args[4]), []byte("123e4567-e89b-42d3-a456-426614174000")) {
		t.Fatalf("resolver command = %#v", runner.command)
	}
}

func TestResolveExternalReturnsOneValidatedTarget(t *testing.T) {
	runner := &externalRunner{
		output: []byte("match\t/private/tmp/tmux-502/default\t$19:0.1\n"),
	}
	target, found, err := ResolveExternal(
		context.Background(),
		runner,
		session.Codex,
		"019fa13c-3a32-7922-b8c2-4b4adf8eadac",
	)
	want := ExternalTarget{
		Socket: "/private/tmp/tmux-502/default",
		Pane:   "$19:0.1",
	}
	if err != nil || !found || target != want {
		t.Fatalf("ResolveExternal() = (%#v, %t, %v), want %#v, true, nil", target, found, err, want)
	}
}

func TestResolveExternalRejectsInvalidInputsAndResults(t *testing.T) {
	validID := "123e4567-e89b-42d3-a456-426614174000"
	for _, test := range []struct {
		name   string
		runner Runner
		nameID session.Provider
		id     string
		output string
	}{
		{name: "nil runner", nameID: session.Claude, id: validID},
		{name: "unsupported provider", runner: &externalRunner{}, nameID: "other", id: validID},
		{name: "non canonical ID", runner: &externalRunner{}, nameID: session.Claude, id: "123E4567-e89b-42d3-a456-426614174000"},
		{name: "missing newline", runner: &externalRunner{output: []byte("none")}, nameID: session.Claude, id: validID},
		{name: "extra line", runner: &externalRunner{output: []byte("none\nnone\n")}, nameID: session.Claude, id: validID},
		{name: "extra fields", runner: &externalRunner{output: []byte("match\t/socket\t$1:0.0\textra\n")}, nameID: session.Claude, id: validID},
		{name: "relative socket", runner: &externalRunner{output: []byte("match\tsocket\t$1:0.0\n")}, nameID: session.Claude, id: validID},
		{name: "tab in socket", runner: &externalRunner{output: []byte("match\t/socket\tpart\t$1:0.0\n")}, nameID: session.Claude, id: validID},
		{name: "NUL in socket", runner: &externalRunner{output: []byte("match\t/socket\x00part\t$1:0.0\n")}, nameID: session.Claude, id: validID},
		{name: "invalid pane", runner: &externalRunner{output: []byte("match\t/socket\t$0:0.0\n")}, nameID: session.Claude, id: validID},
		{name: "duplicate fields", runner: &externalRunner{output: []byte("match\t/socket\t$1:0.0\nmatch\t/socket\t$1:0.0\n")}, nameID: session.Claude, id: validID},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.output != "" {
				test.runner = &externalRunner{output: []byte(test.output)}
			}
			_, found, err := ResolveExternal(context.Background(), test.runner, test.nameID, test.id)
			if err == nil || found {
				t.Fatalf("ResolveExternal() = found %t, err %v, want validation error", found, err)
			}
		})
	}
}

func TestResolveExternalRejectsOversizedRunnerOutput(t *testing.T) {
	output := append([]byte("none\n"), bytes.Repeat([]byte("x"), maxInspectOutputBytes)...)
	_, found, err := ResolveExternal(context.Background(), &externalRunner{output: output}, session.Claude, "123e4567-e89b-42d3-a456-426614174000")
	if err == nil || found {
		t.Fatalf("ResolveExternal() = found %t, err %v, want output limit error", found, err)
	}
}

type externalFixture struct {
	processes  string
	args       map[int]string
	panes      map[string]string
	failSocket string
}

type externalSystemRunner struct{}

func (externalSystemRunner) Output(ctx context.Context, value Command) ([]byte, error) {
	command := systemCommand(ctx, value)
	var output boundedOutput
	var stderr bytes.Buffer
	command.Stdout = &output
	command.Stderr = &stderr
	err := command.Run()
	if output.exceeded || output.Len() > maxInspectOutputBytes {
		return nil, errInspectOutputLimit
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return output.Bytes(), nil
}

func (externalSystemRunner) Run(ctx context.Context, value Command, stdin io.Reader, stdout, stderr io.Writer) error {
	return SystemRunner{}.Run(ctx, value, stdin, stdout, stderr)
}

func TestExternalResolverScript(t *testing.T) {
	const selected = "123e4567-e89b-42d3-a456-426614174000"
	tests := []struct {
		name       string
		provider   session.Provider
		fixture    externalFixture
		wantPane   string
		wantSocket string
		wantFound  bool
		wantError  string
	}{
		{
			name:     "exact Claude descendant",
			provider: session.Claude,
			fixture: externalFixture{
				processes: "100 1 zsh\n101 100 claude\n",
				args:      map[int]string{101: "claude --resume " + selected},
				panes:     map[string]string{"default": "$1:0.0\t100\n"},
			},
			wantPane: "$1:0.0", wantSocket: "default", wantFound: true,
		},
		{
			name:     "exact Codex through wrapper",
			provider: session.Codex,
			fixture: externalFixture{
				processes: "200 1 zsh\n201 200 env\n202 201 codex\n",
				args:      map[int]string{202: "codex resume " + selected},
				panes:     map[string]string{"default": "$2:1.0\t200\n"},
			},
			wantPane: "$2:1.0", wantSocket: "default", wantFound: true,
		},
		{
			name: "bare provider", provider: session.Claude,
			fixture: externalFixture{
				processes: "300 1 claude\n", args: map[int]string{300: "claude"},
				panes: map[string]string{"default": "$3:0.0\t300\n"},
			},
		},
		{
			name: "shell text is not provider process", provider: session.Claude,
			fixture: externalFixture{
				processes: "400 1 sh\n", args: map[int]string{400: "sh -c echo claude --resume " + selected},
				panes: map[string]string{"default": "$4:0.0\t400\n"},
			},
		},
		{
			name: "multiple exact panes", provider: session.Claude,
			fixture: externalFixture{
				processes: "500 1 claude\n600 1 claude\n",
				args:      map[int]string{500: "claude --resume " + selected, 600: "claude --resume " + selected},
				panes:     map[string]string{"default": "$5:0.0\t500\n", "other": "$6:0.0\t600\n"},
			},
			wantError: "external tmux conflict",
		},
		{
			name: "eligible socket inspection failure", provider: session.Claude,
			fixture:   externalFixture{panes: map[string]string{"default": ""}, failSocket: "default"},
			wantError: "resolve external tmux",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target, found, err := runExternalFixture(t, test.fixture, test.provider, selected)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil || found != test.wantFound || target.Pane != test.wantPane ||
				(found && filepath.Base(target.Socket) != test.wantSocket) {
				t.Fatalf("ResolveExternal() = (%#v, %t, %v), want socket %q pane %q found %t", target, found, err, test.wantSocket, test.wantPane, test.wantFound)
			}
		})
	}
}

func TestExternalResolverScriptRejectsBoundaries(t *testing.T) {
	const selected = "123e4567-e89b-42d3-a456-426614174000"
	tests := []struct {
		name    string
		fixture externalFixture
	}{
		{name: "65 sockets", fixture: externalFixture{panes: externalPanes(65, "$1:0.0\t1\n")}},
		{name: "16385 panes", fixture: externalFixture{panes: map[string]string{"default": externalRows(16_385, "$1:0.", "\t1\n")}}},
		{name: "65537 processes", fixture: externalFixture{processes: externalProcesses(65_537)}},
		{name: "257 deep chain", fixture: externalFixture{processes: externalChain(258, selected), args: map[int]string{258: "claude --resume " + selected}, panes: map[string]string{"default": "$1:0.0\t1\n"}}},
		{name: "malformed process", fixture: externalFixture{processes: "bad row\n"}},
		{name: "malformed pane", fixture: externalFixture{panes: map[string]string{"default": "$1:0.0 bad\n"}}},
		{name: "oversized pane output", fixture: externalFixture{panes: map[string]string{"default": strings.Repeat("$1:0.0\t1\n", maxInspectOutputBytes/9+1)}}},
		{name: "oversized process output", fixture: externalFixture{processes: strings.Repeat("1 0 sh\n", maxInspectOutputBytes/7+1)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, found, err := runExternalFixture(t, test.fixture, session.Claude, selected)
			if err == nil || found {
				t.Fatalf("ResolveExternal() = found %t, err %v, want bounded resolver error", found, err)
			}
		})
	}
}

func runExternalFixture(t *testing.T, fixture externalFixture, name session.Provider, nativeID string) (ExternalTarget, bool, error) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "ars-external-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	bin := filepath.Join(root, "bin")
	panes := filepath.Join(root, "panes")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(panes, 0o755); err != nil {
		t.Fatal(err)
	}
	uid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(filepath.Join(root, "processes"), []byte(fixture.processes), 0o600); err != nil {
		t.Fatal(err)
	}
	var args strings.Builder
	for pid, value := range fixture.args {
		args.WriteString(strconv.Itoa(pid) + "\t" + value + "\n")
	}
	if err := os.WriteFile(filepath.Join(root, "args"), []byte(args.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	for socket, rows := range fixture.panes {
		if err := os.WriteFile(filepath.Join(panes, socket), []byte(rows), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "tmux-"+uid, socket)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
	}
	writeExternalExecutable(t, filepath.Join(bin, "id"), "#!/bin/sh\necho "+uid+"\n")
	writeExternalExecutable(t, filepath.Join(bin, "ps"), "#!/bin/sh\nif [ \"$1\" = \"-U\" ]; then cat \"$ARS_EXTERNAL_ROOT/processes\"; exit 0; fi\nawk -F '\\t' -v pid=\"$2\" '$1 == pid {sub(/^[^\\t]*\\t/, \"\"); print; exit}' \"$ARS_EXTERNAL_ROOT/args\"\n")
	tmux := "#!/bin/sh\nsocket=$2\nname=$(basename \"$socket\")\nprintf '%s\\n' \"$@\" >>\"$ARS_EXTERNAL_ROOT/tmux-args\"\nif [ \"$name\" = \"$ARS_FAIL_SOCKET\" ]; then exit 1; fi\ncat \"$ARS_EXTERNAL_PANES/$name\"\n"
	writeExternalExecutable(t, filepath.Join(bin, "tmux"), tmux)
	t.Setenv("ARS_EXTERNAL_ROOT", root)
	t.Setenv("ARS_EXTERNAL_PANES", panes)
	t.Setenv("ARS_FAIL_SOCKET", fixture.failSocket)
	t.Setenv("TMUX_TMPDIR", root)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return ResolveExternal(context.Background(), externalSystemRunner{}, name, nativeID)
}

func writeExternalExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func externalPanes(count int, rows string) map[string]string {
	panes := make(map[string]string, count)
	for index := range count {
		panes["socket"+strconv.Itoa(index)] = rows
	}
	return panes
}

func externalRows(count int, prefix, suffix string) string {
	var rows strings.Builder
	for index := range count {
		rows.WriteString(prefix + strconv.Itoa(index) + suffix)
	}
	return rows.String()
}

func externalProcesses(count int) string {
	var rows strings.Builder
	for index := 1; index <= count; index++ {
		rows.WriteString(strconv.Itoa(index) + " 0 sh\n")
	}
	return rows.String()
}

func externalChain(depth int, selected string) string {
	var rows strings.Builder
	for pid := 1; pid <= depth; pid++ {
		parent := pid - 1
		name := "sh"
		if pid == depth {
			name = "claude"
		}
		rows.WriteString(strconv.Itoa(pid) + " " + strconv.Itoa(parent) + " " + name + "\n")
	}
	return rows.String()
}
