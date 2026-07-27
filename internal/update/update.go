package update

import (
	"context"
	"fmt"
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
	Choose         func(current, latest string) bool
	Args           []string
	Environ        []string
	CheckTimeout   time.Duration
}

type Result struct {
	CurrentVersion string
	LatestVersion  string
	Updated        bool
}

func Explicit(ctx context.Context, deps Dependencies) (Result, error) {
	current := deps.CurrentVersion
	if current == "" {
		return Result{}, fmt.Errorf("updates are unavailable for development builds")
	}
	if _, ok := parseVersion(current); !ok {
		return Result{}, fmt.Errorf("invalid current version %q", current)
	}

	checkCtx, cancel := context.WithTimeout(ctx, deps.CheckTimeout)
	defer cancel()
	latest, err := FetchLatest(checkCtx, deps.Client, deps.ReleaseAPI)
	if err != nil {
		return Result{}, err
	}
	if _, ok := parseVersion(latest); !ok {
		return Result{}, fmt.Errorf("invalid latest version %q", latest)
	}

	result := Result{CurrentVersion: current, LatestVersion: latest}
	if !IsNewer(latest, current) {
		return result, nil
	}
	if _, err := apply(ctx, deps, latest); err != nil {
		return Result{}, err
	}
	result.Updated = true
	return result, nil
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
	if !deps.Choose(deps.CurrentVersion, latest) {
		return nil
	}
	executable, err := apply(ctx, deps, latest)
	if err != nil {
		return err
	}
	if err := deps.Exec(executable, deps.Args, deps.Environ); err != nil {
		return fmt.Errorf("start updated ars: %w", err)
	}
	return nil
}

func apply(ctx context.Context, deps Dependencies, latest string) (string, error) {
	executable, err := deps.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	if IsNPMInstall(executable) {
		err = ApplyNPM(ctx, deps.RunCommand, latest)
	} else {
		err = ApplyBinary(ctx, deps.Client, deps.DownloadBase, latest, deps.GOOS, deps.GOARCH, executable)
	}
	if err != nil {
		return "", err
	}
	return executable, nil
}
