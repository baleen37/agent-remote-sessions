package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
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
