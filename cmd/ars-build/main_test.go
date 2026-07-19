package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type commandCall struct {
	directory string
	args      []string
	env       []string
}

func TestRunAssetsOnlyBuildsExactCollectorTargets(t *testing.T) {
	t.Parallel()

	root := newBuildRoot(t)
	var calls []commandCall
	command := func(_ context.Context, directory string, args, env []string) error {
		calls = append(calls, commandCall{directory: directory, args: append([]string(nil), args...), env: append([]string(nil), env...)})
		output := outputArgument(t, args)
		return os.WriteFile(output, []byte(strings.Join(env, "\n")), 0o755)
	}
	if code := run(context.Background(), []string{"--assets-only"}, root, command, io.Discard); code != 0 {
		t.Fatalf("run() = %d, want 0", code)
	}
	if len(calls) != 3 {
		t.Fatalf("command calls = %d, want exactly three collectors", len(calls))
	}

	gotTargets := make([]string, 0, len(calls))
	for _, call := range calls {
		if call.directory != root {
			t.Errorf("command directory = %q, want %q", call.directory, root)
		}
		wantArgsPrefix := []string{"go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-buildid=", "-o"}
		if len(call.args) != len(wantArgsPrefix)+2 || !reflect.DeepEqual(call.args[:len(wantArgsPrefix)], wantArgsPrefix) || call.args[len(call.args)-1] != "./cmd/ars-collector" {
			t.Errorf("collector command args = %#v", call.args)
		}
		environment := envMap(call.env)
		if environment["CGO_ENABLED"] != "0" {
			t.Errorf("CGO_ENABLED = %q, want 0", environment["CGO_ENABLED"])
		}
		gotTargets = append(gotTargets, environment["GOOS"]+"/"+environment["GOARCH"])
		wantOutput := filepath.Join(root, "internal", "ssh", "generated", "ars-collector-"+environment["GOOS"]+"-"+environment["GOARCH"])
		if outputArgument(t, call.args) != wantOutput {
			t.Errorf("output = %q, want %q", outputArgument(t, call.args), wantOutput)
		}
	}
	sort.Strings(gotTargets)
	wantTargets := []string{"darwin/arm64", "linux/amd64", "linux/arm64"}
	if !reflect.DeepEqual(gotTargets, wantTargets) {
		t.Fatalf("targets = %#v, want %#v", gotTargets, wantTargets)
	}
}

func TestRunDefaultBuildsLocalARSAfterAssets(t *testing.T) {
	t.Parallel()

	root := newBuildRoot(t)
	var calls []commandCall
	command := func(_ context.Context, directory string, args, env []string) error {
		calls = append(calls, commandCall{directory: directory, args: append([]string(nil), args...), env: append([]string(nil), env...)})
		if len(calls) <= 3 {
			return os.WriteFile(outputArgument(t, args), []byte("collector"), 0o755)
		}
		return nil
	}
	if code := run(context.Background(), nil, root, command, io.Discard); code != 0 {
		t.Fatalf("run() = %d, want 0", code)
	}
	if len(calls) != 4 {
		t.Fatalf("command calls = %d, want three collectors and local ars", len(calls))
	}
	want := []string{"go", "build", "-trimpath", "-o", filepath.Join(root, "ars"), "./cmd/ars"}
	if !reflect.DeepEqual(calls[3].args, want) {
		t.Fatalf("local ars args = %#v, want %#v", calls[3].args, want)
	}
}

func TestRunFailsWhenACollectorAssetIsAbsent(t *testing.T) {
	t.Parallel()

	root := newBuildRoot(t)
	var stderr strings.Builder
	call := 0
	command := func(_ context.Context, _ string, args, _ []string) error {
		call++
		if call != 2 {
			return os.WriteFile(outputArgument(t, args), []byte("collector"), 0o755)
		}
		return nil
	}
	if code := run(context.Background(), []string{"--assets-only"}, root, command, &stderr); code == 0 {
		t.Fatal("run() = 0, want failure for absent target")
	}
	if !strings.Contains(stderr.String(), "missing collector asset") {
		t.Fatalf("stderr = %q, want missing collector asset", stderr.String())
	}
}

func TestRunRejectsUnknownArguments(t *testing.T) {
	t.Parallel()

	called := false
	command := func(context.Context, string, []string, []string) error {
		called = true
		return nil
	}
	if code := run(context.Background(), []string{"--other"}, t.TempDir(), command, io.Discard); code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
	if called {
		t.Fatal("command called for invalid arguments")
	}
}

func newBuildRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal", "ssh", "generated"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func outputArgument(t *testing.T, args []string) string {
	t.Helper()
	for index := range args {
		if args[index] == "-o" && index+1 < len(args) {
			return args[index+1]
		}
	}
	t.Fatalf("output argument missing from %#v", args)
	return ""
}

func envMap(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, item := range environment {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}
