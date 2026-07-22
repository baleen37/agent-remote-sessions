# ars Auto Update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When `ars` starts interactively and a newer release exists, show a pre-TUI prompt; Enter updates the binary (npm or standalone channel) and re-execs the new version.

**Architecture:** A new `internal/update` package holds all update logic as small injected-dependency functions (matching the codebase style): release check, prompt, channel detection, npm/binary apply, and a `Maybe` orchestrator. `cmd/ars` wires real dependencies into the interactive path only. `cmd/ars-build` embeds the release version via `-X main.version`.

**Tech Stack:** Go 1.26 stdlib (`net/http`, `archive/tar`, `compress/gzip`, `crypto/sha256`, `syscall`), `golang.org/x/term` (already a dependency), `httptest` for tests.

**Spec:** `docs/superpowers/specs/2026-07-22-auto-update-design.md`

## Global Constraints

- Update check runs ONLY in the interactive path (never for `ars list --json`, `ars remote add`, `--help`).
- Dev builds (empty `version`) skip the check entirely.
- Check budget: 1.5 seconds, no cache; every failure is silent (user decision: no cache).
- Only an apply failure (after the user pressed Enter) prints one stderr line: `ars: update: <error>`; ars then continues on the current version. Startup must never be blocked.
- User-facing copy is English, consistent with the rest of the CLI.
- Charm imports must stay inside `internal/tui` (enforced by `internal/tui/import_boundary_test.go`) — `internal/update` uses stdlib only.
- Conventional commits (`feat:`, `test:`, `docs:`); release versions are `MAJOR.MINOR.PATCH`, tags are `v<version>`.
- Fresh worktrees fail to build `cmd/ars` / `internal/ssh` until `go run ./cmd/ars-build --assets-only` has generated the embedded collector assets. Run it once before the first build or test that touches those packages. Generated blobs and the root `ars` artifact must never be committed.
- Run tests with `go test ./<pkg>/ -run <Name> -v`; full suite is `go test ./...`.

---

### Task 1: Embed release version into the ars binary

**Files:**
- Modify: `cmd/ars-build/release.go:76-79`
- Modify: `cmd/ars-build/release_test.go:64`
- Modify: `cmd/ars/main.go` (add `version` variable)

**Interfaces:**
- Produces: package-level `var version string` in `package main` of `cmd/ars`, set by `-ldflags "-X main.version=<semver>"` in release builds; empty in dev builds. Task 7 reads it.

- [ ] **Step 1: Update the release test to expect the version ldflag**

In `cmd/ars-build/release_test.go`, inside `TestBuildReleaseBuildsExactTargetsAndNpmPackage`, change the `wantPrefix` line (currently line 64) to:

```go
		wantPrefix := []string{"go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-buildid= -X main.version=1.2.3", "-o"}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/ars-build/ -run TestBuildReleaseBuildsExactTargetsAndNpmPackage -v`
Expected: FAIL with `ars command args = ...` showing the old `-ldflags=-buildid=` argument.

- [ ] **Step 3: Add the ldflag in buildRelease**

In `cmd/ars-build/release.go`, replace the `args` literal inside the `collectorTargets` loop (currently lines 76-79):

```go
		args := []string{
			"go", "build", "-trimpath", "-buildvcs=false",
			"-ldflags=-buildid= -X main.version=" + version,
			"-o", binaryPath, "./cmd/ars",
		}
```

- [ ] **Step 4: Declare the version variable in cmd/ars**

In `cmd/ars/main.go`, add directly above `func main()`:

```go
// version is the release version embedded by ars-build; empty for dev builds.
var version string
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./cmd/ars-build/ ./cmd/ars/ -v`
Expected: PASS (all tests).

- [ ] **Step 6: Commit**

```bash
git add cmd/ars-build/release.go cmd/ars-build/release_test.go cmd/ars/main.go
git commit -m "feat: embed release version into ars binary"
```

---

### Task 2: Release check — FetchLatest and IsNewer

**Files:**
- Create: `internal/update/check.go`
- Create: `internal/update/check_test.go`

**Interfaces:**
- Produces:
  - `const DefaultReleaseAPI = "https://api.github.com/repos/baleen37/agent-remote-sessions/releases/latest"`
  - `func FetchLatest(ctx context.Context, client *http.Client, apiURL string) (string, error)` — returns the latest release semver without the `v` prefix, e.g. `"1.3.0"`.
  - `func IsNewer(latest, current string) bool` — true only when both parse as `MAJOR.MINOR.PATCH` and latest > current.

- [ ] **Step 1: Write the failing tests**

Create `internal/update/check_test.go`:

```go
package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchLatestReturnsTagWithoutPrefix(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"tag_name":"v1.3.0"}`))
	}))
	defer server.Close()

	latest, err := FetchLatest(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if latest != "1.3.0" {
		t.Errorf("latest = %q, want 1.3.0", latest)
	}
}

func TestFetchLatestRejectsFailures(t *testing.T) {
	t.Parallel()

	cases := map[string]http.HandlerFunc{
		"status": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		},
		"body": func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`not json`))
		},
		"tag": func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"tag_name":"1.3.0"}`))
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(handler)
			defer server.Close()
			if _, err := FetchLatest(context.Background(), server.Client(), server.URL); err == nil {
				t.Error("FetchLatest = nil error")
			}
		})
	}
}

func TestIsNewer(t *testing.T) {
	t.Parallel()

	newer := [][2]string{
		{"1.3.0", "1.2.9"},
		{"2.0.0", "1.9.9"},
		{"1.2.10", "1.2.9"},
	}
	for _, pair := range newer {
		if !IsNewer(pair[0], pair[1]) {
			t.Errorf("IsNewer(%q, %q) = false", pair[0], pair[1])
		}
	}
	notNewer := [][2]string{
		{"1.2.3", "1.2.3"},
		{"1.2.3", "1.3.0"},
		{"", "1.2.3"},
		{"1.3.0", ""},
		{"v1.3.0", "1.2.3"},
		{"1.3", "1.2.3"},
		{"1.3.0-beta", "1.2.3"},
	}
	for _, pair := range notNewer {
		if IsNewer(pair[0], pair[1]) {
			t.Errorf("IsNewer(%q, %q) = true", pair[0], pair[1])
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/update/ -v`
Expected: FAIL to build with `undefined: FetchLatest` and `undefined: IsNewer`.

- [ ] **Step 3: Implement check.go**

Create `internal/update/check.go`:

```go
// Package update checks GitHub Releases for a newer ars version and,
// after user confirmation, applies the update for the active install
// channel before re-executing the new binary.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const DefaultReleaseAPI = "https://api.github.com/repos/baleen37/agent-remote-sessions/releases/latest"

func FetchLatest(ctx context.Context, client *http.Client, apiURL string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("build release request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("fetch latest release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch latest release: status %d", response.StatusCode)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&release); err != nil {
		return "", fmt.Errorf("decode latest release: %w", err)
	}
	latest, ok := strings.CutPrefix(release.TagName, "v")
	if !ok {
		return "", fmt.Errorf("unexpected release tag %q", release.TagName)
	}
	return latest, nil
}

func IsNewer(latest, current string) bool {
	latestParts, ok := parseVersion(latest)
	if !ok {
		return false
	}
	currentParts, ok := parseVersion(current)
	if !ok {
		return false
	}
	for index := range latestParts {
		if latestParts[index] != currentParts[index] {
			return latestParts[index] > currentParts[index]
		}
	}
	return false
}

func parseVersion(version string) ([3]int, bool) {
	pieces := strings.Split(version, ".")
	if len(pieces) != 3 {
		return [3]int{}, false
	}
	var parts [3]int
	for index, piece := range pieces {
		value, err := strconv.Atoi(piece)
		if err != nil || value < 0 || piece != strconv.Itoa(value) {
			return [3]int{}, false
		}
		parts[index] = value
	}
	return parts, true
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/update/ -v`
Expected: PASS (all three tests).

- [ ] **Step 5: Commit**

```bash
git add internal/update/check.go internal/update/check_test.go
git commit -m "feat: check latest ars release"
```

---

### Task 3: Pre-TUI prompt — Confirm

**Files:**
- Create: `internal/update/prompt.go`
- Create: `internal/update/prompt_test.go`

**Interfaces:**
- Produces: `func Confirm(input io.Reader, output io.Writer, current, latest string, makeRaw func() (restore func(), err error)) bool` — prints the prompt in cooked mode, switches the terminal to raw mode only for the single-key read, and restores it. Returns true only for Enter (`\r` or `\n`).
- Consumes: nothing from earlier tasks.

- [ ] **Step 1: Write the failing tests**

Create `internal/update/prompt_test.go`:

```go
package update

import (
	"errors"
	"strings"
	"testing"
)

func noopRaw() (func(), error) {
	return func() {}, nil
}

func TestConfirmAcceptsOnlyEnter(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		key  string
		want bool
	}{
		"carriage return": {key: "\r", want: true},
		"newline":         {key: "\n", want: true},
		"escape":          {key: "\x1b", want: false},
		"letter":          {key: "q", want: false},
		"space":           {key: " ", want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var output strings.Builder
			got := Confirm(strings.NewReader(tc.key), &output, "1.2.0", "1.3.0", noopRaw)
			if got != tc.want {
				t.Errorf("Confirm(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

func TestConfirmWritesPrompt(t *testing.T) {
	t.Parallel()

	var output strings.Builder
	Confirm(strings.NewReader("\r"), &output, "1.2.0", "1.3.0", noopRaw)
	prompt := output.String()
	for _, want := range []string{
		"ars v1.3.0 available (current v1.2.0)",
		"Enter: update now",
		"any other key: skip",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt %q missing %q", prompt, want)
		}
	}
}

func TestConfirmRestoresTerminalAndHandlesFailures(t *testing.T) {
	t.Parallel()

	restored := false
	makeRaw := func() (func(), error) {
		return func() { restored = true }, nil
	}
	if !Confirm(strings.NewReader("\r"), &strings.Builder{}, "1.2.0", "1.3.0", makeRaw) {
		t.Error("Confirm with enter = false")
	}
	if !restored {
		t.Error("terminal was not restored")
	}

	failRaw := func() (func(), error) {
		return nil, errors.New("no tty")
	}
	if Confirm(strings.NewReader("\r"), &strings.Builder{}, "1.2.0", "1.3.0", failRaw) {
		t.Error("Confirm with raw failure = true")
	}
	if Confirm(strings.NewReader(""), &strings.Builder{}, "1.2.0", "1.3.0", noopRaw) {
		t.Error("Confirm with empty input = true")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/update/ -v`
Expected: FAIL to build with `undefined: Confirm`.

- [ ] **Step 3: Implement prompt.go**

Create `internal/update/prompt.go`:

```go
package update

import (
	"fmt"
	"io"
)

// Confirm asks whether to install the newer release. The prompt is
// printed before makeRaw so newlines render in cooked mode; raw mode
// covers only the single-key read.
func Confirm(input io.Reader, output io.Writer, current, latest string, makeRaw func() (restore func(), err error)) bool {
	fmt.Fprintf(output, "ars v%s available (current v%s)\n", latest, current)
	fmt.Fprintln(output, "Enter: update now    any other key: skip")
	restore, err := makeRaw()
	if err != nil {
		return false
	}
	defer restore()
	var key [1]byte
	if _, err := input.Read(key[:]); err != nil {
		return false
	}
	return key[0] == '\r' || key[0] == '\n'
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/update/ -v`
Expected: PASS (all tests).

- [ ] **Step 5: Commit**

```bash
git add internal/update/prompt.go internal/update/prompt_test.go
git commit -m "feat: prompt before applying ars update"
```

---

### Task 4: Channel detection and npm apply

**Files:**
- Create: `internal/update/apply.go`
- Create: `internal/update/apply_test.go`

**Interfaces:**
- Produces:
  - `func IsNPMInstall(executable string) bool` — true when the executable path sits inside `node_modules/@baleen37/ars/`.
  - `type CommandRunner func(ctx context.Context, name string, args ...string) error`
  - `func ApplyNPM(ctx context.Context, run CommandRunner, version string) error` — runs `npm install -g @baleen37/ars@<version>`.

- [ ] **Step 1: Write the failing tests**

Create `internal/update/apply_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/update/ -v`
Expected: FAIL to build with `undefined: IsNPMInstall` and `undefined: ApplyNPM`.

- [ ] **Step 3: Implement channel detection and npm apply**

Create `internal/update/apply.go`:

```go
package update

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

const npmPackagePath = "node_modules/@baleen37/ars/"

// IsNPMInstall reports whether the running executable is the vendored
// binary inside the @baleen37/ars npm package.
func IsNPMInstall(executable string) bool {
	return strings.Contains(filepath.ToSlash(executable), npmPackagePath)
}

type CommandRunner func(ctx context.Context, name string, args ...string) error

func ApplyNPM(ctx context.Context, run CommandRunner, version string) error {
	if err := run(ctx, "npm", "install", "-g", "@baleen37/ars@"+version); err != nil {
		return fmt.Errorf("npm install: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/update/ -v`
Expected: PASS (all tests).

- [ ] **Step 5: Commit**

```bash
git add internal/update/apply.go internal/update/apply_test.go
git commit -m "feat: apply ars update through npm"
```

---

### Task 5: Standalone binary apply — download, verify, replace

**Files:**
- Modify: `internal/update/apply.go`
- Modify: `internal/update/apply_test.go`

**Interfaces:**
- Produces:
  - `const DefaultDownloadBase = "https://github.com/baleen37/agent-remote-sessions/releases/download"`
  - `func ApplyBinary(ctx context.Context, client *http.Client, baseURL, version, goos, goarch, executable string) error` — downloads `<baseURL>/v<version>/ars_<version>_<goos>_<goarch>.tar.gz` plus `SHA256SUMS`, verifies the checksum, extracts the `ars` tar entry, and atomically replaces `executable`.
- Consumes: nothing new; extends `apply.go` from Task 4.

- [ ] **Step 1: Write the failing tests**

Append to `internal/update/apply_test.go` (also extend the import block with `"archive/tar"`, `"bytes"`, `"compress/gzip"`, `"crypto/sha256"`, `"fmt"`, `"net/http"`, `"net/http/httptest"`, `"os"`, `"path/filepath"`):

```go
func releaseArchive(t *testing.T, binary []byte) []byte {
	t.Helper()

	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	entries := []struct {
		name     string
		contents []byte
	}{
		{name: "ars", contents: binary},
		{name: "README.md", contents: []byte("readme")},
		{name: "LICENSE", contents: []byte("license")},
	}
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0o755, Size: int64(len(entry.contents))}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(entry.contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func releaseServer(t *testing.T, archiveName string, archive []byte, sums string) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1.3.0/SHA256SUMS", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(sums))
	})
	mux.HandleFunc("/v1.3.0/"+archiveName, func(w http.ResponseWriter, _ *http.Request) {
		w.Write(archive)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestApplyBinaryReplacesExecutable(t *testing.T) {
	t.Parallel()

	newBinary := []byte("new ars binary")
	archive := releaseArchive(t, newBinary)
	archiveName := "ars_1.3.0_darwin_arm64.tar.gz"
	sums := fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), archiveName)
	server := releaseServer(t, archiveName, archive, sums)

	executable := filepath.Join(t.TempDir(), "ars")
	if err := os.WriteFile(executable, []byte("old ars binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := ApplyBinary(context.Background(), server.Client(), server.URL, "1.3.0", "darwin", "arm64", executable)
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(replaced, newBinary) {
		t.Errorf("executable contents = %q, want %q", replaced, newBinary)
	}
	info, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("executable mode = %o, want 755", info.Mode().Perm())
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(executable), ".ars-update-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Errorf("leftover temp files: %v", leftovers)
	}
}

func TestApplyBinaryRejectsChecksumMismatch(t *testing.T) {
	t.Parallel()

	archive := releaseArchive(t, []byte("new ars binary"))
	archiveName := "ars_1.3.0_darwin_arm64.tar.gz"
	sums := fmt.Sprintf("%064d  %s\n", 0, archiveName)
	server := releaseServer(t, archiveName, archive, sums)

	executable := filepath.Join(t.TempDir(), "ars")
	original := []byte("old ars binary")
	if err := os.WriteFile(executable, original, 0o755); err != nil {
		t.Fatal(err)
	}

	err := ApplyBinary(context.Background(), server.Client(), server.URL, "1.3.0", "darwin", "arm64", executable)
	if err == nil {
		t.Fatal("ApplyBinary = nil error")
	}
	contents, readErr := os.ReadFile(executable)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(contents, original) {
		t.Errorf("executable was modified: %q", contents)
	}
}

func TestApplyBinaryRejectsMissingChecksumEntry(t *testing.T) {
	t.Parallel()

	archive := releaseArchive(t, []byte("new ars binary"))
	archiveName := "ars_1.3.0_darwin_arm64.tar.gz"
	sums := fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), "ars_1.3.0_linux_amd64.tar.gz")
	server := releaseServer(t, archiveName, archive, sums)

	executable := filepath.Join(t.TempDir(), "ars")
	if err := os.WriteFile(executable, []byte("old ars binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := ApplyBinary(context.Background(), server.Client(), server.URL, "1.3.0", "darwin", "arm64", executable)
	if err == nil {
		t.Fatal("ApplyBinary = nil error")
	}
}

func TestApplyBinaryRejectsArchiveWithoutArs(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	header := &tar.Header{Name: "README.md", Mode: 0o644, Size: int64(len("readme"))}
	if err := tarWriter.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte("readme")); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	archive := buffer.Bytes()
	archiveName := "ars_1.3.0_darwin_arm64.tar.gz"
	sums := fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), archiveName)
	server := releaseServer(t, archiveName, archive, sums)

	executable := filepath.Join(t.TempDir(), "ars")
	if err := os.WriteFile(executable, []byte("old ars binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := ApplyBinary(context.Background(), server.Client(), server.URL, "1.3.0", "darwin", "arm64", executable)
	if err == nil {
		t.Fatal("ApplyBinary = nil error")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/update/ -v`
Expected: FAIL to build with `undefined: ApplyBinary`.

- [ ] **Step 3: Implement ApplyBinary**

Extend `internal/update/apply.go`. Add to the import block: `"archive/tar"`, `"bytes"`, `"compress/gzip"`, `"crypto/sha256"`, `"encoding/hex"`, `"io"`, `"net/http"`, `"os"`. Append:

```go
const DefaultDownloadBase = "https://github.com/baleen37/agent-remote-sessions/releases/download"

const maxArchiveBytes = 256 << 20

// ApplyBinary downloads the release archive for this platform, verifies
// it against SHA256SUMS, and atomically replaces the running executable.
func ApplyBinary(ctx context.Context, client *http.Client, baseURL, version, goos, goarch, executable string) error {
	archiveName := fmt.Sprintf("ars_%s_%s_%s.tar.gz", version, goos, goarch)
	prefix := fmt.Sprintf("%s/v%s/", baseURL, version)
	sums, err := download(ctx, client, prefix+"SHA256SUMS", 1<<20)
	if err != nil {
		return err
	}
	wantSum, err := checksumFor(string(sums), archiveName)
	if err != nil {
		return err
	}
	archive, err := download(ctx, client, prefix+archiveName, maxArchiveBytes)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(archive)
	if hex.EncodeToString(sum[:]) != wantSum {
		return fmt.Errorf("checksum mismatch for %s", archiveName)
	}
	binary, err := extractBinary(archive)
	if err != nil {
		return err
	}
	return replaceExecutable(executable, binary)
}

func download(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build download request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", filepath.Base(url), err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: status %d", filepath.Base(url), response.StatusCode)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, limit))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", filepath.Base(url), err)
	}
	return contents, nil
}

func checksumFor(sums, name string) (string, error) {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksum for %s not found", name)
}

func extractBinary(archive []byte) ([]byte, error) {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open release archive: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read release archive: %w", err)
		}
		if header.Name != "ars" {
			continue
		}
		contents, err := io.ReadAll(io.LimitReader(tarReader, maxArchiveBytes))
		if err != nil {
			return nil, fmt.Errorf("read ars binary: %w", err)
		}
		return contents, nil
	}
	return nil, fmt.Errorf("ars binary not found in archive")
}

func replaceExecutable(executable string, binary []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(executable), ".ars-update-*")
	if err != nil {
		return fmt.Errorf("create replacement file: %w", err)
	}
	name := temp.Name()
	cleanup := func(failure error) error {
		temp.Close()
		os.Remove(name)
		return failure
	}
	if _, err := temp.Write(binary); err != nil {
		return cleanup(fmt.Errorf("write replacement file: %w", err))
	}
	if err := temp.Chmod(0o755); err != nil {
		return cleanup(fmt.Errorf("mark replacement executable: %w", err))
	}
	if err := temp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("close replacement file: %w", err)
	}
	if err := os.Rename(name, executable); err != nil {
		os.Remove(name)
		return fmt.Errorf("replace executable: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/update/ -v`
Expected: PASS (all tests).

- [ ] **Step 5: Commit**

```bash
git add internal/update/apply.go internal/update/apply_test.go
git commit -m "feat: replace standalone ars binary in place"
```

---

### Task 6: Orchestrator — Maybe

**Files:**
- Create: `internal/update/update.go`
- Create: `internal/update/update_test.go`

**Interfaces:**
- Consumes: `FetchLatest`, `IsNewer` (Task 2), `Confirm` (Task 3), `IsNPMInstall`, `CommandRunner`, `ApplyNPM`, `ApplyBinary` (Tasks 4-5).
- Produces:

```go
type Dependencies struct {
	CurrentVersion string
	Client         *http.Client
	ReleaseAPI     string
	DownloadBase   string
	GOOS           string
	GOARCH         string
	Executable     func() (string, error)
	RunCommand     CommandRunner
	Exec           func(argv0 string, argv, env []string) error
	MakeRaw        func() (restore func(), err error)
	Input          io.Reader
	Output         io.Writer
	Args           []string
	Environ        []string
	CheckTimeout   time.Duration
}

func Maybe(ctx context.Context, deps Dependencies) error
```

`Maybe` returns nil for every skip path (dev build, check failure, up to date, user declined). It returns an error only when the user accepted and applying or re-exec failed. On success `deps.Exec` replaces the process and `Maybe` never returns.

- [ ] **Step 1: Write the failing tests**

Create `internal/update/update_test.go`:

```go
package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

type maybeHarness struct {
	deps     Dependencies
	commands [][]string
	execs    [][]string
}

func newMaybeHarness(t *testing.T, tag, current, key, executable string) *maybeHarness {
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
		MakeRaw:      noopRaw,
		Input:        strings.NewReader(key),
		Output:       &strings.Builder{},
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

	harness := newMaybeHarness(t, "v1.3.0", "1.2.0", "\r", npmExecutable)
	harness.deps.ReleaseAPI = "http://127.0.0.1:0/unreachable"
	if err := Maybe(context.Background(), harness.deps); err != nil {
		t.Fatal(err)
	}
	if len(harness.commands) != 0 || len(harness.execs) != 0 {
		t.Errorf("update ran after check failure: %v %v", harness.commands, harness.execs)
	}
}

func TestMaybeSkipsWhenUpToDate(t *testing.T) {
	t.Parallel()

	harness := newMaybeHarness(t, "v1.2.0", "1.2.0", "\r", npmExecutable)
	if err := Maybe(context.Background(), harness.deps); err != nil {
		t.Fatal(err)
	}
	prompt := harness.deps.Output.(*strings.Builder).String()
	if prompt != "" {
		t.Errorf("prompt shown while up to date: %q", prompt)
	}
}

func TestMaybeSkipsWhenDeclined(t *testing.T) {
	t.Parallel()

	harness := newMaybeHarness(t, "v1.3.0", "1.2.0", "q", npmExecutable)
	if err := Maybe(context.Background(), harness.deps); err != nil {
		t.Fatal(err)
	}
	if len(harness.commands) != 0 || len(harness.execs) != 0 {
		t.Errorf("update ran after decline: %v %v", harness.commands, harness.execs)
	}
}

func TestMaybeAppliesNPMUpdateAndReExecs(t *testing.T) {
	t.Parallel()

	harness := newMaybeHarness(t, "v1.3.0", "1.2.0", "\r", npmExecutable)
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

	harness := newMaybeHarness(t, "v1.3.0", "1.2.0", "\r", npmExecutable)
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
```

Note: `TestMaybeAppliesNPMUpdateAndReExecs` exercises the npm channel end to end through `Maybe`; the standalone channel's download/replace behavior is already covered by the `ApplyBinary` tests in Task 5, and routing between channels is covered by the `IsNPMInstall` tests.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/update/ -v`
Expected: FAIL to build with `undefined: Dependencies` and `undefined: Maybe`.

- [ ] **Step 3: Implement update.go**

Create `internal/update/update.go`:

```go
package update

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Dependencies struct {
	CurrentVersion string
	Client         *http.Client
	ReleaseAPI     string
	DownloadBase   string
	GOOS           string
	GOARCH         string
	Executable     func() (string, error)
	RunCommand     CommandRunner
	Exec           func(argv0 string, argv, env []string) error
	MakeRaw        func() (restore func(), err error)
	Input          io.Reader
	Output         io.Writer
	Args           []string
	Environ        []string
	CheckTimeout   time.Duration
}

// Maybe offers a newer release before the TUI starts. Every skip path
// (dev build, check failure, up to date, declined prompt) returns nil so
// startup is never blocked; only a failed apply after the user accepted
// returns an error. On success Exec replaces the process.
func Maybe(ctx context.Context, deps Dependencies) error {
	if deps.CurrentVersion == "" {
		return nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, deps.CheckTimeout)
	latest, err := FetchLatest(checkCtx, deps.Client, deps.ReleaseAPI)
	cancel()
	if err != nil || !IsNewer(latest, deps.CurrentVersion) {
		return nil
	}
	if !Confirm(deps.Input, deps.Output, deps.CurrentVersion, latest, deps.MakeRaw) {
		return nil
	}
	executable, err := deps.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	if IsNPMInstall(executable) {
		err = ApplyNPM(ctx, deps.RunCommand, latest)
	} else {
		err = ApplyBinary(ctx, deps.Client, deps.DownloadBase, latest, deps.GOOS, deps.GOARCH, executable)
	}
	if err != nil {
		return err
	}
	if err := deps.Exec(executable, deps.Args, deps.Environ); err != nil {
		return fmt.Errorf("start updated ars: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/update/ -v`
Expected: PASS (all tests).

- [ ] **Step 5: Commit**

```bash
git add internal/update/update.go internal/update/update_test.go
git commit -m "feat: orchestrate ars update flow"
```

---

### Task 7: Wire the update check into the interactive path

**Files:**
- Modify: `cmd/ars/main.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `update.Maybe`, `update.Dependencies`, `update.DefaultReleaseAPI`, `update.DefaultDownloadBase` (Task 6); `version` variable (Task 1).

- [ ] **Step 1: Hook the update check into runTUI**

In `cmd/ars/main.go`:

1. Extend the import block. The existing `internal/runtime` import stays as is; the stdlib `runtime` package needs an alias to avoid the collision:

```go
import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	goruntime "runtime"
	"syscall"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/app"
	"github.com/baleen37/agent-remote-sessions/internal/protocol"
	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/runtime"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/baleen37/agent-remote-sessions/internal/ssh"
	"github.com/baleen37/agent-remote-sessions/internal/tui"
	"github.com/baleen37/agent-remote-sessions/internal/update"
	"golang.org/x/term"
)
```

2. Replace the `runTUI` function:

```go
func runTUI(ctx context.Context, deps tui.Dependencies, stdin, stdout *os.File, isTerminal func(int) bool) error {
	if !isTerminal(int(stdin.Fd())) || !isTerminal(int(stdout.Fd())) {
		return errors.New("interactive mode requires a TTY; use ars list --json")
	}
	if err := maybeUpdate(ctx, stdin, stdout); err != nil {
		fmt.Fprintln(os.Stderr, "ars: update:", err)
	}
	return tui.Run(ctx, deps, stdin, stdout)
}

func maybeUpdate(ctx context.Context, stdin, stdout *os.File) error {
	return update.Maybe(ctx, update.Dependencies{
		CurrentVersion: version,
		Client:         http.DefaultClient,
		ReleaseAPI:     update.DefaultReleaseAPI,
		DownloadBase:   update.DefaultDownloadBase,
		GOOS:           goruntime.GOOS,
		GOARCH:         goruntime.GOARCH,
		Executable:     os.Executable,
		RunCommand: func(ctx context.Context, name string, args ...string) error {
			command := exec.CommandContext(ctx, name, args...)
			command.Stdin = stdin
			command.Stdout = stdout
			command.Stderr = os.Stderr
			return command.Run()
		},
		Exec: syscall.Exec,
		MakeRaw: func() (func(), error) {
			state, err := term.MakeRaw(int(stdin.Fd()))
			if err != nil {
				return nil, err
			}
			return func() { term.Restore(int(stdin.Fd()), state) }, nil
		},
		Input:        stdin,
		Output:       stdout,
		Args:         os.Args,
		Environ:      os.Environ(),
		CheckTimeout: 1500 * time.Millisecond,
	})
}
```

- [ ] **Step 2: Run the full suite to verify nothing broke**

Run: `go run ./cmd/ars-build --assets-only` first if `internal/ssh/generated/` is empty (fresh worktrees fail to build with `pattern generated/ars-collector-darwin-arm64: no matching files found` until the embedded collector assets exist).

Run: `go build ./... && go vet ./... && go test ./...`
Expected: build and vet clean, all packages PASS. (`internal/tui` runs `TestCharmImportsStayInsideTUI`, which now also walks `internal/update` — it must stay charm-free.)

Run: `gofmt -l cmd internal`
Expected: no output.

- [ ] **Step 3: Document the behavior in the README**

In `README.md`, add this section between the `## Install` section and `## Localhost and remote inventory`:

```markdown
## Automatic update check

Interactive runs check GitHub Releases for a newer version with a 1.5
second budget; any failure is ignored and ars starts normally. When a
newer release exists, ars offers it before the TUI starts: press Enter to
update and continue on the new version, or any other key to skip. npm
installs update through `npm install -g @baleen37/ars`; standalone
binaries are verified against `SHA256SUMS` and replaced in place. Source
builds skip the check.
```

- [ ] **Step 4: Manual smoke test of the prompt path**

Build a binary with a fake old version and point it at the real API:

```bash
go run ./cmd/ars-build --assets-only
go build -ldflags "-X main.version=0.0.1" -o /tmp/ars-update-smoke ./cmd/ars
/tmp/ars-update-smoke
```

Expected: if a real GitHub release newer than 0.0.1 exists, the prompt `ars vX.Y.Z available (current v0.0.1)` appears; pressing `q` skips into the TUI (quit with `q`/`Ctrl+C`). If no release exists yet or the network is blocked, ars goes straight to the TUI. Both outcomes verify the non-blocking wiring. Do NOT press Enter — that would overwrite `/tmp/ars-update-smoke`, which is harmless, but the point of the smoke test is the skip path.

Also verify the dev-build skip: `go build -o /tmp/ars-dev-smoke ./cmd/ars && /tmp/ars-dev-smoke` must enter the TUI with no prompt and no network call delay.

Clean up: `rm -f /tmp/ars-update-smoke /tmp/ars-dev-smoke ./ars`

- [ ] **Step 5: Commit**

```bash
git add cmd/ars/main.go README.md
git commit -m "feat: offer ars update before the tui starts"
```
