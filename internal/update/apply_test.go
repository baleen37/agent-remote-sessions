package update

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestIsNPMInstall(t *testing.T) {
	t.Parallel()

	npmPaths := []string{
		"/usr/local/lib/node_modules/@baleen37/ars/vendor/ars-darwin-arm64",
		"/home/user/.nvm/versions/node/v22.14.0/lib/node_modules/@baleen37/ars/vendor/ars-linux-amd64",
	}
	for _, path := range npmPaths {
		if !IsNPMInstall(path) {
			t.Errorf("IsNPMInstall(%q) = false", path)
		}
	}
	binaryPaths := []string{
		"/home/user/.local/bin/ars",
		"/usr/local/bin/ars",
		"/home/user/node_modules/other/ars",
	}
	for _, path := range binaryPaths {
		if IsNPMInstall(path) {
			t.Errorf("IsNPMInstall(%q) = true", path)
		}
	}
}

func TestApplyNPMRunsGlobalInstall(t *testing.T) {
	t.Parallel()

	var gotName string
	var gotArgs []string
	run := func(_ context.Context, name string, args ...string) error {
		gotName = name
		gotArgs = args
		return nil
	}
	if err := ApplyNPM(context.Background(), run, "1.3.0"); err != nil {
		t.Fatal(err)
	}
	if gotName != "npm" {
		t.Errorf("command = %q, want npm", gotName)
	}
	want := []string{"install", "-g", "@baleen37/ars@1.3.0"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("args = %#v, want %#v", gotArgs, want)
	}
}

func TestApplyNPMWrapsFailure(t *testing.T) {
	t.Parallel()

	run := func(_ context.Context, _ string, _ ...string) error {
		return errors.New("exit status 1")
	}
	if err := ApplyNPM(context.Background(), run, "1.3.0"); err == nil {
		t.Error("ApplyNPM = nil error")
	}
}
