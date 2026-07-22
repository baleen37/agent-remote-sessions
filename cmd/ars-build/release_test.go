package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestValidateReleaseVersion(t *testing.T) {
	t.Parallel()

	for _, version := range []string{"0.0.0", "1.0.0", "12.34.56"} {
		if err := validateReleaseVersion(version); err != nil {
			t.Errorf("validateReleaseVersion(%q): %v", version, err)
		}
	}
	for _, version := range []string{"", "v1.0.0", "1.0", "01.0.0", "1.0.0-beta.1", "../1.0.0"} {
		if err := validateReleaseVersion(version); err == nil {
			t.Errorf("validateReleaseVersion(%q) = nil", version)
		}
	}
}

func TestBuildReleaseBuildsExactTargetsAndNpmPackage(t *testing.T) {
	t.Parallel()

	root := newReleaseRoot(t)
	var calls []commandCall
	execute := func(_ context.Context, directory string, args, env []string) error {
		calls = append(calls, commandCall{
			directory: directory,
			args:      append([]string(nil), args...),
			env:       append([]string(nil), env...),
		})
		return os.WriteFile(outputArgument(t, args), []byte(strings.Join(env, "\n")), 0o755)
	}

	if err := buildRelease(context.Background(), root, "1.2.3", execute); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("ars build calls = %d, want 3", len(calls))
	}

	var targets []string
	for _, call := range calls {
		if call.directory != root {
			t.Errorf("command directory = %q, want %q", call.directory, root)
		}
		environment := envMap(call.env)
		targets = append(targets, environment["GOOS"]+"/"+environment["GOARCH"])
		if environment["CGO_ENABLED"] != "0" {
			t.Errorf("CGO_ENABLED = %q, want 0", environment["CGO_ENABLED"])
		}
		wantPrefix := []string{"go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-buildid= -X main.version=1.2.3", "-o"}
		if len(call.args) != len(wantPrefix)+2 || !reflect.DeepEqual(call.args[:len(wantPrefix)], wantPrefix) || call.args[len(call.args)-1] != "./cmd/ars" {
			t.Errorf("ars command args = %#v", call.args)
		}
	}
	wantTargets := []string{"darwin/arm64", "linux/amd64", "linux/arm64"}
	if !reflect.DeepEqual(targets, wantTargets) {
		t.Fatalf("targets = %#v, want %#v", targets, wantTargets)
	}

	for _, relative := range []string{
		"npm/package.json",
		"npm/README.md",
		"npm/LICENSE",
		"npm/bin/ars.js",
		"npm/vendor/ars-darwin-arm64",
		"npm/vendor/ars-linux-amd64",
		"npm/vendor/ars-linux-arm64",
	} {
		info, err := os.Stat(filepath.Join(root, "dist", relative))
		if err != nil {
			t.Errorf("missing %s: %v", relative, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", relative)
		}
	}
	launcher, err := os.Stat(filepath.Join(root, "dist", "npm", "bin", "ars.js"))
	if err != nil {
		t.Fatal(err)
	}
	if launcher.Mode().Perm() != 0o755 {
		t.Errorf("launcher mode = %o, want 755", launcher.Mode().Perm())
	}
	packageBytes, err := os.ReadFile(filepath.Join(root, "dist", "npm", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var packageDocument struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(packageBytes, &packageDocument); err != nil {
		t.Fatal(err)
	}
	if packageDocument.Version != "1.2.3" {
		t.Errorf("npm package version = %q, want 1.2.3", packageDocument.Version)
	}
}

func TestBuildReleaseCreatesArchivesAndChecksums(t *testing.T) {
	t.Parallel()

	root := newReleaseRoot(t)
	execute := func(_ context.Context, _ string, args, _ []string) error {
		return os.WriteFile(outputArgument(t, args), []byte("native ars"), 0o755)
	}
	if err := buildRelease(context.Background(), root, "1.2.3", execute); err != nil {
		t.Fatal(err)
	}

	archives := []string{
		"ars_1.2.3_darwin_arm64.tar.gz",
		"ars_1.2.3_linux_amd64.tar.gz",
		"ars_1.2.3_linux_arm64.tar.gz",
	}
	for _, name := range archives {
		members, modes := archiveMembers(t, filepath.Join(root, "dist", name))
		wantMembers := []string{"ars", "README.md", "LICENSE"}
		if !reflect.DeepEqual(members, wantMembers) {
			t.Errorf("%s members = %#v, want %#v", name, members, wantMembers)
		}
		if !reflect.DeepEqual(modes, []int64{0o755, 0o644, 0o644}) {
			t.Errorf("%s modes = %#v", name, modes)
		}
	}

	checksumBytes, err := os.ReadFile(filepath.Join(root, "dist", "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(checksumBytes)), "\n")
	if len(lines) != 3 {
		t.Fatalf("checksum lines = %d, want 3", len(lines))
	}
	for index, name := range archives {
		contents, err := os.ReadFile(filepath.Join(root, "dist", name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(contents)
		want := hex.EncodeToString(sum[:]) + "  " + name
		if lines[index] != want {
			t.Errorf("checksum line %d = %q, want %q", index, lines[index], want)
		}
	}
}

func TestBuildReleaseRemovesOnlyDist(t *testing.T) {
	t.Parallel()

	root := newReleaseRoot(t)
	outside := filepath.Join(root, "keep-me")
	stale := filepath.Join(root, "dist", "stale")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	execute := func(_ context.Context, _ string, args, _ []string) error {
		return os.WriteFile(outputArgument(t, args), []byte("native ars"), 0o755)
	}

	if err := buildRelease(context.Background(), root, "1.0.0", execute); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file changed: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale dist file still exists: %v", err)
	}
}

func newReleaseRoot(t *testing.T) string {
	t.Helper()

	root := newBuildRoot(t)
	files := map[string]struct {
		contents string
		mode     os.FileMode
	}{
		"README.md":        {contents: "readme\n", mode: 0o644},
		"LICENSE":          {contents: "license\n", mode: 0o644},
		"npm/package.json": {contents: "{\"name\":\"@baleen37/ars\",\"version\":\"0.0.0\"}\n", mode: 0o644},
		"npm/bin/ars.js":   {contents: "#!/usr/bin/env node\n", mode: 0o755},
	}
	for name, file := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(file.contents), file.mode); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func archiveMembers(t *testing.T, path string) ([]string, []int64) {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	var names []string
	var modes []int64
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return names, modes
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
		modes = append(modes, header.Mode)
	}
}
