package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
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
	if err := validateReleaseVersion(version); err != nil {
		return err
	}

	dist := filepath.Join(root, "dist")
	if err := os.RemoveAll(dist); err != nil {
		return fmt.Errorf("remove dist: %w", err)
	}
	packageRoot := filepath.Join(dist, "npm")
	for _, directory := range []string{
		filepath.Join(packageRoot, "bin"),
		filepath.Join(packageRoot, "vendor"),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("create release directory: %w", err)
		}
	}

	if err := writeReleasePackage(
		filepath.Join(root, "npm", "package.json"),
		filepath.Join(packageRoot, "package.json"),
		version,
	); err != nil {
		return err
	}
	copyPairs := [][2]string{
		{filepath.Join(root, "npm", "bin", "ars.js"), filepath.Join(packageRoot, "bin", "ars.js")},
		{filepath.Join(root, "README.md"), filepath.Join(packageRoot, "README.md")},
		{filepath.Join(root, "LICENSE"), filepath.Join(packageRoot, "LICENSE")},
	}
	for _, pair := range copyPairs {
		if err := copyReleaseFile(pair[0], pair[1]); err != nil {
			return err
		}
	}

	archives := make([]string, 0, len(collectorTargets))
	for _, target := range collectorTargets {
		goos, goarch := target[0], target[1]
		binaryName := "ars-" + goos + "-" + goarch
		binaryPath := filepath.Join(packageRoot, "vendor", binaryName)
		args := []string{
			"go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-buildid=",
			"-o", binaryPath, "./cmd/ars",
		}
		environment := []string{"CGO_ENABLED=0", "GOOS=" + goos, "GOARCH=" + goarch}
		if err := execute(ctx, root, args, environment); err != nil {
			return fmt.Errorf("build ars %s/%s: %w", goos, goarch, err)
		}

		archiveName := fmt.Sprintf("ars_%s_%s_%s.tar.gz", version, goos, goarch)
		archivePath := filepath.Join(dist, archiveName)
		entries := []archiveEntry{
			{name: "ars", path: binaryPath, mode: 0o755},
			{name: "README.md", path: filepath.Join(root, "README.md"), mode: 0o644},
			{name: "LICENSE", path: filepath.Join(root, "LICENSE"), mode: 0o644},
		}
		if err := writeReleaseArchive(archivePath, entries); err != nil {
			return err
		}
		archives = append(archives, archivePath)
	}

	return writeReleaseChecksums(filepath.Join(dist, "SHA256SUMS"), archives)
}

func writeReleasePackage(source, destination, version string) error {
	contents, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read npm package template: %w", err)
	}
	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil {
		return fmt.Errorf("decode npm package template: %w", err)
	}
	document["version"] = version
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode npm package: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(destination, encoded, 0o644); err != nil {
		return fmt.Errorf("write npm package: %w", err)
	}
	return nil
}

func copyReleaseFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open release source %s: %w", filepath.Base(source), err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return fmt.Errorf("stat release source %s: %w", filepath.Base(source), err)
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create release file %s: %w", filepath.Base(destination), err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return fmt.Errorf("copy release file %s: %w", filepath.Base(destination), err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close release file %s: %w", filepath.Base(destination), err)
	}
	return nil
}

func writeReleaseArchive(path string, entries []archiveEntry) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create release archive: %w", err)
	}
	gzipWriter := gzip.NewWriter(file)
	gzipWriter.Header.ModTime = time.Unix(0, 0)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		contents, err := os.ReadFile(entry.path)
		if err != nil {
			return closeReleaseArchive(file, gzipWriter, tarWriter, fmt.Errorf("read archive entry %s: %w", entry.name, err))
		}
		header := &tar.Header{
			Name:    entry.name,
			Mode:    entry.mode,
			Size:    int64(len(contents)),
			ModTime: time.Unix(0, 0),
			Format:  tar.FormatPAX,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return closeReleaseArchive(file, gzipWriter, tarWriter, fmt.Errorf("write archive header %s: %w", entry.name, err))
		}
		if _, err := tarWriter.Write(contents); err != nil {
			return closeReleaseArchive(file, gzipWriter, tarWriter, fmt.Errorf("write archive entry %s: %w", entry.name, err))
		}
	}
	if err := tarWriter.Close(); err != nil {
		gzipWriter.Close()
		file.Close()
		return fmt.Errorf("close tar archive: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		file.Close()
		return fmt.Errorf("close gzip archive: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close release archive: %w", err)
	}
	return nil
}

func closeReleaseArchive(file *os.File, gzipWriter *gzip.Writer, tarWriter *tar.Writer, failure error) error {
	tarWriter.Close()
	gzipWriter.Close()
	file.Close()
	return failure
}

func writeReleaseChecksums(path string, archives []string) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create checksums: %w", err)
	}
	for _, archive := range archives {
		contents, err := os.ReadFile(archive)
		if err != nil {
			file.Close()
			return fmt.Errorf("read archive for checksum: %w", err)
		}
		sum := sha256.Sum256(contents)
		if _, err := fmt.Fprintf(file, "%x  %s\n", sum, filepath.Base(archive)); err != nil {
			file.Close()
			return fmt.Errorf("write checksum: %w", err)
		}
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close checksums: %w", err)
	}
	return nil
}
