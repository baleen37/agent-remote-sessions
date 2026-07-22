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
