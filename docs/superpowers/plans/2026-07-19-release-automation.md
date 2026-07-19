# Release Automation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish verified ars binaries to GitHub Releases and the public `@baleen37/ars` npm package automatically from releasable commits on `main`.

**Architecture:** Extend the existing Go build command to produce three deterministic native archives, checksums, and one self-contained npm package. Keep semantic-release as a private Node toolchain that computes versions, publishes npm through OIDC, and creates GitHub Releases only after the existing CI matrix passes.

**Tech Stack:** Go 1.26 standard library, Node.js built-in test runner, Node 24.15.0, npm, semantic-release 25.0.8, GitHub Actions, npm Trusted Publishing.

## Global Constraints

- Publish exactly `darwin/arm64`, `linux/amd64`, and `linux/arm64` ars binaries.
- Embed exactly the existing three collector targets before every native ars build.
- Publish one public package named `@baleen37/ars`; do not add platform packages or install-time downloads.
- `feat` releases minor, `fix` and `perf` release patch, and `BREAKING CHANGE` releases major.
- `docs`, `chore`, and `test` commits do not release.
- The first stable release is `v1.0.0`; do not add pre-release or maintenance branches.
- Keep Homebrew, Windows, release PRs, changelog commits, and version commits out of scope.
- Give `contents: write` and `id-token: write` only to the `main` release job.
- Use npm Trusted Publishing after one `0.0.0` bootstrap publish with the `bootstrap` dist-tag.
- Do not change ars commands, SSH behavior, session discovery, or public JSON.
- Do not commit `dist`, generated collectors, native binaries, npm tarballs, or `node_modules`.

---

## File map

~~~text
LICENSE                              MIT license for archives and npm
package.json                         private semantic-release toolchain
package-lock.json                    exact Node dependency graph
release.config.cjs                   release rules and publisher ordering
npm/package.json                     public package template at version 0.0.0
npm/bin/ars.js                       native binary selector and launcher
npm/bin/ars.test.cjs                 launcher contract tests
test/release-config.test.cjs         commit classification/config tests
test/release-workflow.test.cjs       CI permission and gate regression test
cmd/ars-build/main.go                parse --release and route build mode
cmd/ars-build/main_test.go           existing mode regression tests
cmd/ars-build/release.go             archives, checksums, and npm assembly
cmd/ars-build/release_test.go        release output contract tests
.github/workflows/ci.yml             verify gate and main-only release job
.gitignore                           release and Node build outputs
README.md                            user install and operator release runbook
~~~

### Task 1: Add the public npm package template and native launcher

**Files:**

- Create: `LICENSE`
- Create: `npm/package.json`
- Create: `npm/bin/ars.js`
- Create: `npm/bin/ars.test.cjs`

**Interfaces:**

- Produces: `binaryName(platform, arch) string|null` exported from `npm/bin/ars.js`.
- Produces: `run(runtime) number` exported from `npm/bin/ars.js`, where `runtime` supplies `platform`, `arch`, `dirname`, `spawnSync`, `stderr`, `kill`, and `pid`.
- Produces: package bin mapping `ars -> bin/ars.js` and vendor names consumed by `cmd/ars-build/release.go`.

- [ ] **Step 1: Write the failing launcher tests**

Create `npm/bin/ars.test.cjs`:

~~~js
const assert = require("node:assert/strict");
const test = require("node:test");

const { binaryName, run } = require("./ars.js");

test("binaryName maps only supported targets", () => {
  assert.equal(binaryName("darwin", "arm64"), "ars-darwin-arm64");
  assert.equal(binaryName("linux", "x64"), "ars-linux-amd64");
  assert.equal(binaryName("linux", "arm64"), "ars-linux-arm64");
  assert.equal(binaryName("darwin", "x64"), null);
  assert.equal(binaryName("win32", "x64"), null);
});

test("run inherits stdio and returns the native exit code", () => {
  let call;
  const code = run({
    platform: "linux",
    arch: "x64",
    dirname: "/package/bin",
    spawnSync(binary, args, options) {
      call = { binary, args, options };
      return { status: 23, signal: null };
    },
    stderr: { write() { throw new Error("unexpected stderr"); } },
    kill() { throw new Error("unexpected signal"); },
    pid: 100,
  });

  assert.equal(code, 23);
  assert.deepEqual(call, {
    binary: "/package/vendor/ars-linux-amd64",
    args: process.argv.slice(2),
    options: { stdio: "inherit" },
  });
});

test("run mirrors a terminating signal", () => {
  let killed;
  const code = run({
    platform: "darwin",
    arch: "arm64",
    dirname: "/package/bin",
    spawnSync() { return { status: null, signal: "SIGTERM" }; },
    stderr: { write() {} },
    kill(pid, signal) { killed = { pid, signal }; },
    pid: 101,
  });

  assert.equal(code, 1);
  assert.deepEqual(killed, { pid: 101, signal: "SIGTERM" });
});

test("run rejects unsupported platforms before spawning", () => {
  let stderr = "";
  const code = run({
    platform: "win32",
    arch: "x64",
    dirname: "/package/bin",
    spawnSync() { throw new Error("must not spawn"); },
    stderr: { write(value) { stderr += value; } },
    kill() {},
    pid: 102,
  });

  assert.equal(code, 1);
  assert.match(stderr, /unsupported platform win32\/x64/);
  assert.match(stderr, /darwin\/arm64, linux\/amd64, linux\/arm64/);
});
~~~

- [ ] **Step 2: Run the tests and verify RED**

Run:

~~~sh
node --test npm/bin/ars.test.cjs
~~~

Expected: FAIL with `Cannot find module './ars.js'`.

- [ ] **Step 3: Implement the minimal launcher**

Create executable `npm/bin/ars.js`:

~~~js
#!/usr/bin/env node

const path = require("node:path");
const { spawnSync } = require("node:child_process");

const binaries = new Map([
  ["darwin/arm64", "ars-darwin-arm64"],
  ["linux/x64", "ars-linux-amd64"],
  ["linux/arm64", "ars-linux-arm64"],
]);

function binaryName(platform, arch) {
  return binaries.get(`${platform}/${arch}`) ?? null;
}

function run(runtime = {
  platform: process.platform,
  arch: process.arch,
  dirname: __dirname,
  spawnSync,
  stderr: process.stderr,
  kill: process.kill.bind(process),
  pid: process.pid,
}) {
  const name = binaryName(runtime.platform, runtime.arch);
  if (name === null) {
    runtime.stderr.write(
      `ars: unsupported platform ${runtime.platform}/${runtime.arch}; ` +
      "supported: darwin/arm64, linux/amd64, linux/arm64\n",
    );
    return 1;
  }

  const binary = path.join(runtime.dirname, "..", "vendor", name);
  const result = runtime.spawnSync(binary, process.argv.slice(2), { stdio: "inherit" });
  if (result.error) {
    runtime.stderr.write(`ars: start native binary: ${result.error.message}\n`);
    return 1;
  }
  if (result.signal) {
    runtime.kill(runtime.pid, result.signal);
    return 1;
  }
  return result.status ?? 1;
}

if (require.main === module) {
  process.exitCode = run();
}

module.exports = { binaryName, run };
~~~

Run `chmod 0755 npm/bin/ars.js` so git records the executable bit.

- [ ] **Step 4: Add package metadata and the MIT license**

Create `npm/package.json`:

~~~json
{
  "name": "@baleen37/ars",
  "version": "0.0.0",
  "description": "Search and resume Claude Code and Codex sessions across SSH hosts",
  "license": "MIT",
  "repository": {
    "type": "git",
    "url": "git+https://github.com/baleen37/agent-remote-sessions.git"
  },
  "homepage": "https://github.com/baleen37/agent-remote-sessions#readme",
  "bugs": "https://github.com/baleen37/agent-remote-sessions/issues",
  "bin": {
    "ars": "bin/ars.js"
  },
  "files": [
    "bin/ars.js",
    "vendor/ars-*",
    "README.md",
    "LICENSE"
  ],
  "engines": {
    "node": ">=18"
  },
  "publishConfig": {
    "access": "public",
    "provenance": true
  }
}
~~~

Create `LICENSE` with the standard MIT text and `Copyright (c) 2026 baleen37`.

- [ ] **Step 5: Run the launcher tests and inspect package metadata**

Run:

~~~sh
node --test npm/bin/ars.test.cjs
node -e 'const p=require("./npm/package.json"); if(p.name!=="@baleen37/ars"||p.bin.ars!=="bin/ars.js"||p.publishConfig.access!=="public") process.exit(1)'
~~~

Expected: four Node tests PASS and the metadata command exits 0.

- [ ] **Step 6: Commit the npm package boundary**

~~~sh
git add LICENSE npm/package.json npm/bin/ars.js npm/bin/ars.test.cjs
git commit -m "feat: add npm launcher"
~~~

### Task 2: Extend ars-build with native archives, checksums, and npm assembly

**Files:**

- Modify: `cmd/ars-build/main.go:35-89`
- Modify: `cmd/ars-build/main_test.go:144-158`
- Create: `cmd/ars-build/release.go`
- Create: `cmd/ars-build/release_test.go`
- Modify: `.gitignore`

**Interfaces:**

- Consumes: the three `collectorTargets` from `cmd/ars-build/main.go`.
- Consumes: `npm/package.json`, `npm/bin/ars.js`, `README.md`, and `LICENSE`.
- Produces: `buildRelease(context.Context, root, version string, execute commandExecutor) error`.
- Produces: `go run ./cmd/ars-build --release MAJOR.MINOR.PATCH` and the exact `dist` tree in the design.

- [ ] **Step 1: Write failing version and target tests**

Create `cmd/ars-build/release_test.go` with these tests and helpers:

~~~go
package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
		calls = append(calls, commandCall{directory: directory, args: append([]string(nil), args...), env: append([]string(nil), env...)})
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
		environment := envMap(call.env)
		targets = append(targets, environment["GOOS"]+"/"+environment["GOARCH"])
		if environment["CGO_ENABLED"] != "0" {
			t.Errorf("CGO_ENABLED = %q", environment["CGO_ENABLED"])
		}
		if call.args[len(call.args)-1] != "./cmd/ars" {
			t.Errorf("command = %#v", call.args)
		}
	}
	wantTargets := []string{"darwin/arm64", "linux/amd64", "linux/arm64"}
	if !reflect.DeepEqual(targets, wantTargets) {
		t.Fatalf("targets = %#v, want %#v", targets, wantTargets)
	}

	for _, relative := range []string{
		"npm/package.json", "npm/README.md", "npm/LICENSE", "npm/bin/ars.js",
		"npm/vendor/ars-darwin-arm64", "npm/vendor/ars-linux-amd64", "npm/vendor/ars-linux-arm64",
	} {
		if _, err := os.Stat(filepath.Join(root, "dist", relative)); err != nil {
			t.Errorf("missing %s: %v", relative, err)
		}
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
		members := archiveMembers(t, filepath.Join(root, "dist", name))
		want := []string{"ars", "README.md", "LICENSE"}
		if !reflect.DeepEqual(members, want) {
			t.Errorf("%s members = %#v, want %#v", name, members, want)
		}
	}

	checksumBytes, err := os.ReadFile(filepath.Join(root, "dist", "SHA256SUMS"))
	if err != nil { t.Fatal(err) }
	lines := strings.Split(strings.TrimSpace(string(checksumBytes)), "\n")
	if len(lines) != 3 { t.Fatalf("checksum lines = %d", len(lines)) }
	for index, name := range archives {
		contents, err := os.ReadFile(filepath.Join(root, "dist", name))
		if err != nil { t.Fatal(err) }
		sum := sha256.Sum256(contents)
		want := hex.EncodeToString(sum[:]) + "  " + name
		if lines[index] != want { t.Errorf("line %d = %q, want %q", index, lines[index], want) }
	}
}

func newReleaseRoot(t *testing.T) string {
	t.Helper()
	root := newBuildRoot(t)
	for name, contents := range map[string]string{
		"README.md": "readme\n",
		"LICENSE": "license\n",
		"npm/package.json": "{\"name\":\"@baleen37/ars\",\"version\":\"0.0.0\"}\n",
		"npm/bin/ars.js": "#!/usr/bin/env node\n",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { t.Fatal(err) }
		if err := os.WriteFile(path, []byte(contents), 0o755); err != nil { t.Fatal(err) }
	}
	return root
}

func archiveMembers(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil { t.Fatal(err) }
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil { t.Fatal(err) }
	defer gz.Close()
	reader := tar.NewReader(gz)
	var names []string
	for {
		header, err := reader.Next()
		if err == io.EOF { return names }
		if err != nil { t.Fatal(err) }
		names = append(names, header.Name)
	}
}
~~~

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

~~~sh
go test ./cmd/ars-build -run 'TestValidateReleaseVersion|TestBuildRelease' -v
~~~

Expected: FAIL because `validateReleaseVersion` and `buildRelease` are undefined.

- [ ] **Step 3: Implement the release builder**

Create `cmd/ars-build/release.go` with these exact responsibilities:

~~~go
package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

type commandExecutor func(context.Context, string, []string, []string) error

type archiveEntry struct {
	name string
	path string
	mode int64
}

var stableReleaseVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func validateReleaseVersion(version string) error {
	if !stableReleaseVersion.MatchString(version) {
		return fmt.Errorf("release version must be MAJOR.MINOR.PATCH")
	}
	return nil
}

func buildRelease(ctx context.Context, root, version string, execute commandExecutor) error {
	if err := validateReleaseVersion(version); err != nil { return err }
	dist := filepath.Join(root, "dist")
	if err := os.RemoveAll(dist); err != nil { return fmt.Errorf("remove dist: %w", err) }
	packageRoot := filepath.Join(dist, "npm")
	for _, directory := range []string{filepath.Join(packageRoot, "bin"), filepath.Join(packageRoot, "vendor")} {
		if err := os.MkdirAll(directory, 0o755); err != nil { return fmt.Errorf("create release directory: %w", err) }
	}

	copyPairs := [][2]string{
		{filepath.Join(root, "npm", "package.json"), filepath.Join(packageRoot, "package.json")},
		{filepath.Join(root, "npm", "bin", "ars.js"), filepath.Join(packageRoot, "bin", "ars.js")},
		{filepath.Join(root, "README.md"), filepath.Join(packageRoot, "README.md")},
		{filepath.Join(root, "LICENSE"), filepath.Join(packageRoot, "LICENSE")},
	}
	for _, pair := range copyPairs {
		if err := copyReleaseFile(pair[0], pair[1]); err != nil { return err }
	}

	var archives []string
	for _, target := range collectorTargets {
		goos, goarch := target[0], target[1]
		binaryName := "ars-" + goos + "-" + goarch
		binaryPath := filepath.Join(packageRoot, "vendor", binaryName)
		args := []string{"go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-buildid=", "-o", binaryPath, "./cmd/ars"}
		env := []string{"CGO_ENABLED=0", "GOOS=" + goos, "GOARCH=" + goarch}
		if err := execute(ctx, root, args, env); err != nil {
			return fmt.Errorf("build ars %s/%s: %w", goos, goarch, err)
		}
		archiveName := fmt.Sprintf("ars_%s_%s_%s.tar.gz", version, goos, goarch)
		archivePath := filepath.Join(dist, archiveName)
		entries := []archiveEntry{
			{name: "ars", path: binaryPath, mode: 0o755},
			{name: "README.md", path: filepath.Join(root, "README.md"), mode: 0o644},
			{name: "LICENSE", path: filepath.Join(root, "LICENSE"), mode: 0o644},
		}
		if err := writeReleaseArchive(archivePath, entries); err != nil { return err }
		archives = append(archives, archivePath)
	}
	return writeReleaseChecksums(filepath.Join(dist, "SHA256SUMS"), archives)
}

func copyReleaseFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil { return fmt.Errorf("open release source %s: %w", filepath.Base(source), err) }
	defer input.Close()
	info, err := input.Stat()
	if err != nil { return fmt.Errorf("stat release source: %w", err) }
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil { return fmt.Errorf("create release file: %w", err) }
	if _, err := io.Copy(output, input); err != nil { output.Close(); return fmt.Errorf("copy release file: %w", err) }
	if err := output.Close(); err != nil { return fmt.Errorf("close release file: %w", err) }
	return nil
}

func writeReleaseArchive(path string, entries []archiveEntry) error {
	file, err := os.Create(path)
	if err != nil { return fmt.Errorf("create release archive: %w", err) }
	gzipWriter := gzip.NewWriter(file)
	gzipWriter.Header.ModTime = time.Unix(0, 0)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		contents, err := os.ReadFile(entry.path)
		if err != nil { tarWriter.Close(); gzipWriter.Close(); file.Close(); return fmt.Errorf("read archive entry %s: %w", entry.name, err) }
		header := &tar.Header{Name: entry.name, Mode: entry.mode, Size: int64(len(contents)), ModTime: time.Unix(0, 0), Format: tar.FormatPAX}
		if err := tarWriter.WriteHeader(header); err != nil { tarWriter.Close(); gzipWriter.Close(); file.Close(); return fmt.Errorf("write archive header: %w", err) }
		if _, err := tarWriter.Write(contents); err != nil { tarWriter.Close(); gzipWriter.Close(); file.Close(); return fmt.Errorf("write archive entry: %w", err) }
	}
	if err := tarWriter.Close(); err != nil { gzipWriter.Close(); file.Close(); return fmt.Errorf("close tar archive: %w", err) }
	if err := gzipWriter.Close(); err != nil { file.Close(); return fmt.Errorf("close gzip archive: %w", err) }
	if err := file.Close(); err != nil { return fmt.Errorf("close release archive: %w", err) }
	return nil
}

func writeReleaseChecksums(path string, archives []string) error {
	file, err := os.Create(path)
	if err != nil { return fmt.Errorf("create checksums: %w", err) }
	for _, archive := range archives {
		contents, err := os.ReadFile(archive)
		if err != nil { file.Close(); return fmt.Errorf("read archive for checksum: %w", err) }
		sum := sha256.Sum256(contents)
		if _, err := fmt.Fprintf(file, "%x  %s\n", sum, filepath.Base(archive)); err != nil { file.Close(); return fmt.Errorf("write checksum: %w", err) }
	}
	if err := file.Close(); err != nil { return fmt.Errorf("close checksums: %w", err) }
	return nil
}
~~~

Keep error cleanup surgical: failed builds may leave the ignored `dist` for inspection; the next release call removes only that exact directory.

- [ ] **Step 4: Route `--release` through the existing command**

In `cmd/ars-build/main.go`, replace the anonymous executor signature with `commandExecutor`, parse these modes before building collectors, and update usage:

~~~go
	assetsOnly := false
	releaseVersion := ""
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--assets-only":
		assetsOnly = true
	case len(args) == 2 && args[0] == "--release":
		if err := validateReleaseVersion(args[1]); err != nil {
			fmt.Fprintln(stderr, "ars-build:", err)
			return 2
		}
		releaseVersion = args[1]
	default:
		fmt.Fprintln(stderr, "usage: ars-build [--assets-only | --release MAJOR.MINOR.PATCH]")
		return 2
	}
~~~

After the complete collector-set check and before the local build, add:

~~~go
	if releaseVersion != "" {
		if err := buildRelease(ctx, root, releaseVersion, execute); err != nil {
			fmt.Fprintln(stderr, "ars-build: build release:", err)
			return 1
		}
		return 0
	}
~~~

Change `run` and `main` to use the named `commandExecutor` type. Extend `TestRunRejectsUnknownArguments` with `--release`, `--release v1.0.0`, and extra-argument cases; assert invalid versions do not call the executor or create `dist`.

- [ ] **Step 5: Ignore only generated release outputs**

Append to `.gitignore`:

~~~gitignore
/dist/
/node_modules/
~~~

- [ ] **Step 6: Run focused and regression tests**

Run:

~~~sh
gofmt -w cmd/ars-build/main.go cmd/ars-build/main_test.go cmd/ars-build/release.go cmd/ars-build/release_test.go
go test ./cmd/ars-build -v
go run ./cmd/ars-build --release 0.0.0
(cd dist && shasum -a 256 -c SHA256SUMS)
~~~

Expected: all Go tests PASS, exactly three archives report `OK`, and `dist/npm/vendor` contains exactly three non-empty executables.

- [ ] **Step 7: Commit the release builder**

~~~sh
git add .gitignore cmd/ars-build/main.go cmd/ars-build/main_test.go cmd/ars-build/release.go cmd/ars-build/release_test.go
git commit -m "feat: build release artifacts"
~~~

### Task 3: Configure semantic-release and verify commit classification

**Files:**

- Create: `package.json`
- Create: `package-lock.json`
- Create: `release.config.cjs`
- Create: `test/release-config.test.cjs`

**Interfaces:**

- Consumes: `go run ./cmd/ars-build --release ${nextRelease.version}`.
- Produces: `npm test` and `npm run release`.
- Produces: semantic-release plugins ordered as analyze, notes, build, npm publish, GitHub publish.

- [ ] **Step 1: Write failing release configuration tests**

Create `test/release-config.test.cjs`:

~~~js
const assert = require("node:assert/strict");
const test = require("node:test");

const config = require("../release.config.cjs");

test("release config is main-only and publishes generated assets", () => {
  assert.deepEqual(config.branches, ["main"]);
  assert.equal(config.tagFormat, "v${version}");
  assert.deepEqual(config.plugins.map((plugin) => Array.isArray(plugin) ? plugin[0] : plugin), [
    "@semantic-release/commit-analyzer",
    "@semantic-release/release-notes-generator",
    "@semantic-release/exec",
    "@semantic-release/npm",
    "@semantic-release/github",
  ]);
  assert.equal(config.plugins[2][1].prepareCmd, "go run ./cmd/ars-build --release ${nextRelease.version}");
  assert.equal(config.plugins[3][1].pkgRoot, "dist/npm");
  assert.deepEqual(config.plugins[4][1].assets.map((asset) => asset.path), [
    "dist/ars_*_darwin_arm64.tar.gz",
    "dist/ars_*_linux_amd64.tar.gz",
    "dist/ars_*_linux_arm64.tar.gz",
    "dist/SHA256SUMS",
  ]);
});

test("commit analyzer maps the accepted release contract", async () => {
  const { analyzeCommits } = await import("@semantic-release/commit-analyzer");
  const analyzer = config.plugins[0][1];
  const logger = { log() {} };
  const classify = (message) => analyzeCommits(analyzer, { commits: [{ message }], logger });

  assert.equal(await classify("feat: add picker"), "minor");
  assert.equal(await classify("fix: preserve exit code"), "patch");
  assert.equal(await classify("perf: reduce allocations"), "patch");
  assert.equal(await classify("feat: replace protocol\n\nBREAKING CHANGE: schema changed"), "major");
  assert.equal(await classify("docs: explain install"), null);
  assert.equal(await classify("chore: update metadata"), null);
  assert.equal(await classify("test: cover launcher"), null);
});
~~~

- [ ] **Step 2: Run the tests and verify RED**

Run:

~~~sh
node --test test/release-config.test.cjs
~~~

Expected: FAIL because `release.config.cjs` does not exist.

- [ ] **Step 3: Add the private release toolchain**

Create root `package.json`:

~~~json
{
  "name": "agent-remote-sessions-release-toolchain",
  "private": true,
  "engines": {
    "node": "^22.14.0 || >=24.10.0"
  },
  "scripts": {
    "test": "node --test npm/bin/ars.test.cjs test/*.test.cjs",
    "release": "semantic-release"
  },
  "devDependencies": {
    "@semantic-release/commit-analyzer": "13.0.1",
    "@semantic-release/exec": "7.1.0",
    "@semantic-release/github": "12.0.9",
    "@semantic-release/npm": "13.1.5",
    "@semantic-release/release-notes-generator": "14.1.1",
    "semantic-release": "25.0.8"
  }
}
~~~

Run `npm install --package-lock-only --ignore-scripts` and commit the resulting `package-lock.json`. Do not hand-edit the lockfile.

- [ ] **Step 4: Add the exact semantic-release lifecycle**

Create `release.config.cjs`:

~~~js
module.exports = {
  branches: ["main"],
  tagFormat: "v${version}",
  plugins: [
    ["@semantic-release/commit-analyzer", {
      releaseRules: [
        { type: "docs", release: false },
        { type: "chore", release: false },
        { type: "test", release: false },
      ],
    }],
    "@semantic-release/release-notes-generator",
    ["@semantic-release/exec", {
      prepareCmd: "go run ./cmd/ars-build --release ${nextRelease.version}",
    }],
    ["@semantic-release/npm", {
      pkgRoot: "dist/npm",
    }],
    ["@semantic-release/github", {
      assets: [
        { path: "dist/ars_*_darwin_arm64.tar.gz", label: "ars darwin/arm64" },
        { path: "dist/ars_*_linux_amd64.tar.gz", label: "ars linux/amd64" },
        { path: "dist/ars_*_linux_arm64.tar.gz", label: "ars linux/arm64" },
        { path: "dist/SHA256SUMS", label: "SHA256 checksums" },
      ],
    }],
  ],
};
~~~

- [ ] **Step 5: Install and run all Node tests**

Run:

~~~sh
npm ci
npm test
npm pack ./dist/npm --dry-run
~~~

Expected: launcher and release-config tests PASS; npm's dry-run file list contains only package metadata, `README.md`, `LICENSE`, `bin/ars.js`, and the three vendor binaries.

- [ ] **Step 6: Commit the release configuration**

~~~sh
git add package.json package-lock.json release.config.cjs test/release-config.test.cjs
git commit -m "feat: configure semantic release"
~~~

### Task 4: Gate publishing behind CI and document installation and recovery

**Files:**

- Create: `test/release-workflow.test.cjs`
- Modify: `.github/workflows/ci.yml:7-32`
- Modify: `README.md:8-30`
- Modify: `README.md:170-193`

**Interfaces:**

- Consumes: `npm ci`, `npm test`, `npm run release`, and `ars-build --release`.
- Produces: a `release` job trusted as workflow filename `ci.yml` by npm.
- Produces: public install commands and the exact operator bootstrap/recovery contract.

- [ ] **Step 1: Write a failing workflow contract test**

Create `test/release-workflow.test.cjs`:

~~~js
const assert = require("node:assert/strict");
const fs = require("node:fs");
const test = require("node:test");

const workflow = fs.readFileSync(".github/workflows/ci.yml", "utf8");

test("release waits for verification and is restricted to main pushes", () => {
  assert.match(workflow, /release:\n[\s\S]*needs: verify/);
  assert.match(workflow, /github\.event_name == 'push'/);
  assert.match(workflow, /github\.ref == 'refs\/heads\/main'/);
  assert.match(workflow, /group: release-main/);
  assert.match(workflow, /cancel-in-progress: false/);
});

test("release alone can write contents and request an OIDC token", () => {
  assert.match(workflow, /release:[\s\S]*permissions:[\s\S]*contents: write[\s\S]*id-token: write/);
  assert.match(workflow, /fetch-depth: 0/);
  assert.match(workflow, /node-version: 24\.15\.0/);
  assert.match(workflow, /npm run release/);
});
~~~

- [ ] **Step 2: Run the workflow test and verify RED**

Run:

~~~sh
node --test test/release-workflow.test.cjs
~~~

Expected: FAIL because `.github/workflows/ci.yml` has no `release` job.

- [ ] **Step 3: Add Node verification to the existing matrix**

After `actions/setup-go` in the `verify` job, add:

~~~yaml
      - uses: actions/setup-node@v7
        with:
          node-version: 24.15.0
          package-manager-cache: false
      - name: Install release toolchain
        run: npm ci
      - name: Test release toolchain
        run: npm test
~~~

Do not change the existing Go test, race, vet, or local build steps.

- [ ] **Step 4: Add the main-only release job**

Append this job to `.github/workflows/ci.yml`:

~~~yaml
  release:
    if: github.event_name == 'push' && github.ref == 'refs/heads/main'
    needs: verify
    runs-on: ubuntu-latest
    concurrency:
      group: release-main
      cancel-in-progress: false
    permissions:
      contents: write
      id-token: write
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: false
      - uses: actions/setup-node@v7
        with:
          node-version: 24.15.0
          registry-url: https://registry.npmjs.org
          package-manager-cache: false
      - name: Install release toolchain
        run: npm ci
      - name: Preflight release package
        run: |
          go run ./cmd/ars-build --release 0.0.0
          (cd dist && sha256sum --check SHA256SUMS)
          npm pack ./dist/npm --dry-run
          npm pack ./dist/npm --pack-destination "$RUNNER_TEMP"
          npm install --global --prefix "$RUNNER_TEMP/ars-prefix" "$RUNNER_TEMP/baleen37-ars-0.0.0.tgz"
          mkdir -p "$RUNNER_TEMP/config/ars"
          : > "$RUNNER_TEMP/config/ars/hosts"
          XDG_CONFIG_HOME="$RUNNER_TEMP/config" "$RUNNER_TEMP/ars-prefix/bin/ars" list --json | grep -F '"schema_version":1'
      - name: Preview release
        run: npm run release -- --dry-run
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
      - name: Publish release
        run: npm run release
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
~~~

Do not set `NPM_TOKEN` or `NODE_AUTH_TOKEN`; npm CLI obtains a short-lived token from the job's OIDC identity after Trusted Publishing is configured.

- [ ] **Step 5: Document user installation**

At the start of README's Install section, add:

~~~markdown
Install the latest release from npm:

```sh
npm install -g @baleen37/ars
```

The npm package includes native ars binaries for Apple Silicon, Linux x86-64,
and Linux arm64. It does not download an executable during installation. For a
Node-free install, download the matching archive from GitHub Releases, verify
it against `SHA256SUMS`, and place `ars` on `PATH`.
~~~

Keep the existing source-build instructions below this text.

- [ ] **Step 6: Document release bootstrap and recovery**

Add a compact `## Release` section after Verification that records:

~~~markdown
Releases run after CI succeeds on `main`. Conventional `feat`, `fix`, `perf`,
and `BREAKING CHANGE` commits determine the next version; documentation,
chore, and test-only changes are no-ops.

The one-time npm setup is:

1. publish `@baleen37/ars@0.0.0` with the `bootstrap` dist-tag
2. configure npm Trusted Publishing for GitHub repository
   `baleen37/agent-remote-sessions` and workflow `ci.yml`
3. allow `npm publish`, then verify the first `main` release as `v1.0.0`

If publication is partial, inspect the Git tag, npm version, and GitHub Release
before changing state. Preserve any public npm version. Reconstruct a missing
GitHub Release from the same tag, or publish a missing npm package rebuilt from
that exact tag. Delete a failed tag only when neither registry published it.
~~~

- [ ] **Step 7: Run workflow, package, and repository verification**

Run:

~~~sh
npm test
go run ./cmd/ars-build --assets-only
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/ars-build
go run ./cmd/ars-build --release 0.0.0
(cd dist && shasum -a 256 -c SHA256SUMS)
npm pack ./dist/npm --dry-run
git diff --check
~~~

Expected: every test and checksum passes, npm lists only intended files, and `git diff --check` prints nothing.

- [ ] **Step 8: Commit CI and documentation**

~~~sh
git add .github/workflows/ci.yml README.md test/release-workflow.test.cjs
git commit -m "ci: publish ars releases"
~~~

### Task 5: Bootstrap npm Trusted Publishing and prove the real v1.0.0 flow

**Files:**

- No source files; this task changes npm and GitHub release state after the implementation branch is reviewed and approved for merge.

**Interfaces:**

- Consumes: `dist/npm`, GitHub workflow `ci.yml`, npm package `@baleen37/ars`, and the merged `main` commit.
- Produces: npm bootstrap version `0.0.0@bootstrap`, Trusted Publisher binding, Git tag and GitHub Release `v1.0.0`, and npm `1.0.0@latest`.

- [ ] **Step 1: Run the final local verification before external writes**

Run:

~~~sh
git status --short
npm ci
npm test
go run ./cmd/ars-build --assets-only
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/ars-build --release 0.0.0
(cd dist && shasum -a 256 -c SHA256SUMS)
npm pack ./dist/npm --dry-run
~~~

Expected: only known branch changes are present, all tests pass, all three checksums report `OK`, and npm's file list contains no source transcripts, generated collector paths, or unrelated files.

- [ ] **Step 2: Confirm npm identity and package state**

Authenticate interactively with `npm login`, then run:

~~~sh
npm whoami
npm view @baleen37/ars versions --json
~~~

Expected before bootstrap: `npm whoami` is the owner of the `baleen37` scope and package lookup returns `E404` or contains no `0.0.0`. Stop if another owner or version is present.

- [ ] **Step 3: Publish the one-time bootstrap version**

Run exactly:

~~~sh
npm publish ./dist/npm --access public --tag bootstrap
npm view @baleen37/ars@bootstrap name version dist-tags --json
~~~

Expected: package name is `@baleen37/ars`, version is `0.0.0`, `bootstrap` points to `0.0.0`, and no `latest` tag points to the bootstrap version.

- [ ] **Step 4: Configure the npm Trusted Publisher**

In npm package settings for `@baleen37/ars`, create a GitHub Actions trusted publisher with these exact values:

~~~text
Organization or user: baleen37
Repository: agent-remote-sessions
Workflow filename: ci.yml
Environment name: empty
Allowed action: npm publish
~~~

Keep GitHub Actions free of `NPM_TOKEN`. Confirm the npm settings page shows the active publisher before merging.

- [ ] **Step 5: Integrate the approved implementation branch**

Use the repository's normal reviewed PR flow. Confirm the merged `main` commit contains all four implementation commits and that at least one included commit is `feat`, so the absence of prior `v*` tags produces `v1.0.0`.

Read-only proof before waiting for Actions:

~~~sh
git fetch origin main --tags
git log --oneline origin/main -8
git tag --sort=-version:refname | head
~~~

Expected: implementation is on `origin/main` and no pre-existing stable tag conflicts with `v1.0.0`.

- [ ] **Step 6: Watch the actual release workflow**

Run:

~~~sh
gh run list --workflow CI --branch main --limit 5
release_run_id=$(gh run list --workflow CI --branch main --limit 1 --json databaseId --jq '.[0].databaseId')
gh run watch "$release_run_id" --exit-status
~~~

Expected: the current main run completes with both verify matrix jobs and release job successful. If it fails, use `gh run view --log-failed` and apply the documented partial-state inspection before any retry.

- [ ] **Step 7: Verify GitHub and npm release state**

Run:

~~~sh
gh release view v1.0.0 --json tagName,targetCommitish,assets,url
npm view @baleen37/ars@latest name version dist-tags dist.attestations --json
~~~

Expected: GitHub lists three platform archives plus `SHA256SUMS`; npm reports `@baleen37/ars` version `1.0.0`, `latest` points to `1.0.0`, `bootstrap` remains `0.0.0`, and provenance attestation metadata is present.

- [ ] **Step 8: Download, verify, and install the public artifacts**

Run:

~~~sh
release_check_dir=$(mktemp -d)
gh release download v1.0.0 --dir "$release_check_dir"
(cd "$release_check_dir" && shasum -a 256 -c SHA256SUMS)
npm install --global --prefix "$release_check_dir/npm-prefix" @baleen37/ars@1.0.0
mkdir -p "$release_check_dir/config/ars"
: > "$release_check_dir/config/ars/hosts"
XDG_CONFIG_HOME="$release_check_dir/config" "$release_check_dir/npm-prefix/bin/ars" list --json
~~~

Expected: all three archives report `OK`; npm installation succeeds; ars prints schema version 1 with empty hosts, sessions, and errors arrays and exits 0.

- [ ] **Step 9: Record final evidence without committing generated outputs**

Run:

~~~sh
git status --short --branch
git fetch origin main --tags
git log -1 --oneline origin/main
gh release view v1.0.0 --json url,tagName
npm view @baleen37/ars@latest version
~~~

Expected: the worktree has no tracked release output changes, `origin/main` is the released commit, GitHub reports `v1.0.0`, and npm reports `1.0.0`.
