package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/app"
	"github.com/baleen37/agent-remote-sessions/internal/output"
	"github.com/baleen37/agent-remote-sessions/internal/protocol"
	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/baleen37/agent-remote-sessions/internal/ssh"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	runner := ssh.SystemRunner{}
	assets := ssh.EmbeddedCollectorAssets{}
	collector := func(ctx context.Context, target string) ([]session.Candidate, []provider.Result, error) {
		return ssh.Collect(ctx, runner, assets, target, ssh.CollectOptions{
			ConnectTimeout: 5 * time.Second,
			HostTimeout:    60 * time.Second,
			ProtocolLimits: protocol.DefaultLimits(),
		})
	}
	dependencies := app.Dependencies{
		LoadHosts: app.Load,
		Collect: func(ctx context.Context, hosts []app.Host) app.Result {
			return app.CollectHosts(ctx, hosts, 4, collector)
		},
		Pick: (output.FZF{Runner: runner}).Select,
		Resume: func(ctx context.Context, item session.Session) error {
			adapter, ok := provider.Lookup(item.Provider)
			if !ok {
				return fmt.Errorf("unsupported session provider")
			}
			return ssh.Resume(ctx, runner, item.Host, item, adapter)
		},
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	exitCode := app.Run(ctx, os.Args[1:], dependencies)
	stop()
	os.Exit(exitCode)
}
