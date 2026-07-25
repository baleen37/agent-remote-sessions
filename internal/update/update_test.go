package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

type maybeHarness struct {
	deps     Dependencies
	commands [][]string
	execs    [][]string
	choices  [][2]string
}

func newMaybeHarness(t *testing.T, tag, current string, accept bool, executable string) *maybeHarness {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"tag_name":"` + tag + `"}`))
	}))
	t.Cleanup(server.Close)

	harness := &maybeHarness{}
	harness.deps = Dependencies{
		CurrentVersion: current,
		Client:         server.Client(),
		ReleaseAPI:     server.URL,
		DownloadBase:   server.URL,
		GOOS:           "darwin",
		GOARCH:         "arm64",
		Executable:     func() (string, error) { return executable, nil },
		RunCommand: func(_ context.Context, name string, args ...string) error {
			harness.commands = append(harness.commands, append([]string{name}, args...))
			return nil
		},
		Exec: func(argv0 string, argv, env []string) error {
			harness.execs = append(harness.execs, append([]string{argv0}, argv...))
			return nil
		},
		Choose: func(current, latest string) bool {
			harness.choices = append(harness.choices, [2]string{current, latest})
			return accept
		},
		Args:         []string{"ars"},
		Environ:      []string{"HOME=/home/user"},
		CheckTimeout: time.Second,
	}
	return harness
}

const npmExecutable = "/usr/local/lib/node_modules/@baleen37/ars/vendor/ars-darwin-arm64"

func TestMaybeSkipsDevBuildsWithoutChecking(t *testing.T) {
	t.Parallel()

	deps := Dependencies{CurrentVersion: ""}
	if err := Maybe(context.Background(), deps); err != nil {
		t.Fatal(err)
	}
}

func TestMaybeIgnoresCheckFailures(t *testing.T) {
	t.Parallel()

	harness := newMaybeHarness(t, "v1.3.0", "1.2.0", true, npmExecutable)
	harness.deps.ReleaseAPI = "http://127.0.0.1:0/unreachable"
	if err := Maybe(context.Background(), harness.deps); err != nil {
		t.Fatal(err)
	}
	if len(harness.choices) != 0 || len(harness.commands) != 0 || len(harness.execs) != 0 {
		t.Errorf("update path ran after check failure: choices %v, commands %v, execs %v", harness.choices, harness.commands, harness.execs)
	}
}

func TestMaybeSkipsWhenUpToDate(t *testing.T) {
	t.Parallel()

	harness := newMaybeHarness(t, "v1.2.0", "1.2.0", true, npmExecutable)
	if err := Maybe(context.Background(), harness.deps); err != nil {
		t.Fatal(err)
	}
	if len(harness.choices) != 0 {
		t.Errorf("choice shown while up to date: %v", harness.choices)
	}
}

func TestMaybeSkipsWhenDeclined(t *testing.T) {
	t.Parallel()

	harness := newMaybeHarness(t, "v1.3.0", "1.2.0", false, npmExecutable)
	if err := Maybe(context.Background(), harness.deps); err != nil {
		t.Fatal(err)
	}
	wantChoices := [][2]string{{"1.2.0", "1.3.0"}}
	if !reflect.DeepEqual(harness.choices, wantChoices) {
		t.Errorf("choices = %#v, want %#v", harness.choices, wantChoices)
	}
	if len(harness.commands) != 0 || len(harness.execs) != 0 {
		t.Errorf("update ran after decline: %v %v", harness.commands, harness.execs)
	}
}

func TestMaybeAppliesNPMUpdateAndReExecs(t *testing.T) {
	t.Parallel()

	harness := newMaybeHarness(t, "v1.3.0", "1.2.0", true, npmExecutable)
	if err := Maybe(context.Background(), harness.deps); err != nil {
		t.Fatal(err)
	}
	wantCommands := [][]string{{"npm", "install", "-g", "@baleen37/ars@1.3.0"}}
	if !reflect.DeepEqual(harness.commands, wantCommands) {
		t.Errorf("commands = %#v, want %#v", harness.commands, wantCommands)
	}
	wantExecs := [][]string{{npmExecutable, "ars"}}
	if !reflect.DeepEqual(harness.execs, wantExecs) {
		t.Errorf("execs = %#v, want %#v", harness.execs, wantExecs)
	}
}

func TestMaybeReturnsApplyFailureWithoutExec(t *testing.T) {
	t.Parallel()

	harness := newMaybeHarness(t, "v1.3.0", "1.2.0", true, npmExecutable)
	harness.deps.RunCommand = func(_ context.Context, _ string, _ ...string) error {
		return errors.New("exit status 1")
	}
	if err := Maybe(context.Background(), harness.deps); err == nil {
		t.Fatal("Maybe = nil error")
	}
	if len(harness.execs) != 0 {
		t.Errorf("exec ran after apply failure: %v", harness.execs)
	}
}
