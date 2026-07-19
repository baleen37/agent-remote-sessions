package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

var collectorTargets = [][2]string{
	{"darwin", "arm64"},
	{"linux", "amd64"},
	{"linux", "arm64"},
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ars-build: determine repository root:", err)
		os.Exit(1)
	}
	var execute commandExecutor = func(ctx context.Context, directory string, args, environment []string) error {
		command := exec.CommandContext(ctx, args[0], args[1:]...)
		command.Dir = directory
		command.Env = append(os.Environ(), environment...)
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		return command.Run()
	}
	os.Exit(run(context.Background(), os.Args[1:], root, execute, os.Stderr))
}

func run(
	ctx context.Context,
	args []string,
	root string,
	execute commandExecutor,
	stderr io.Writer,
) int {
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

	generated := filepath.Join(root, "internal", "ssh", "generated")
	if err := os.MkdirAll(generated, 0o755); err != nil {
		fmt.Fprintln(stderr, "ars-build: create generated directory:", err)
		return 1
	}
	for _, target := range collectorTargets {
		goos, goarch := target[0], target[1]
		output := filepath.Join(generated, "ars-collector-"+goos+"-"+goarch)
		if err := os.Remove(output); err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(stderr, "ars-build: remove old collector asset:", err)
			return 1
		}
		commandArgs := []string{"go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-buildid=", "-o", output, "./cmd/ars-collector"}
		environment := []string{"CGO_ENABLED=0", "GOOS=" + goos, "GOARCH=" + goarch}
		if err := execute(ctx, root, commandArgs, environment); err != nil {
			fmt.Fprintf(stderr, "ars-build: build collector %s/%s: %v\n", goos, goarch, err)
			return 1
		}
	}
	for _, target := range collectorTargets {
		goos, goarch := target[0], target[1]
		output := filepath.Join(generated, "ars-collector-"+goos+"-"+goarch)
		info, err := os.Stat(output)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			fmt.Fprintf(stderr, "ars-build: missing collector asset %s/%s\n", goos, goarch)
			return 1
		}
	}
	if assetsOnly {
		return 0
	}
	if releaseVersion != "" {
		if err := buildRelease(ctx, root, releaseVersion, execute); err != nil {
			fmt.Fprintln(stderr, "ars-build: build release:", err)
			return 1
		}
		return 0
	}

	localOutput := filepath.Join(root, "ars")
	if err := execute(ctx, root, []string{"go", "build", "-trimpath", "-o", localOutput, "./cmd/ars"}, nil); err != nil {
		fmt.Fprintln(stderr, "ars-build: build local ars:", err)
		return 1
	}
	return 0
}
