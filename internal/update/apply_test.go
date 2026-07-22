package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
