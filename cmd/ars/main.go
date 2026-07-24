package main

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

// version is the release version embedded by ars-build; empty for dev builds.
var version string

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	sshRunner := ssh.SystemRunner{}
	runtimeRunner := runtime.SystemRunner{}
	assets := ssh.EmbeddedCollectorAssets{}
	collectOptions := ssh.CollectOptions{
		ConnectTimeout: 5 * time.Second,
		HostTimeout:    60 * time.Second,
		ProtocolLimits: protocol.DefaultLimits(),
	}
	collectHost := func(ctx context.Context, host app.Host) ([]session.Discovered, []provider.Result, runtime.Report, error) {
		if host.Local {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, nil, runtime.Report{}, err
			}
			candidates, results, err := provider.DiscoverAll(ctx, home, provider.Builtin())
			if err != nil {
				return nil, nil, runtime.Report{}, err
			}
			states, report := runtime.Inspect(ctx, runtimeRunner, candidates)
			return combineRuntime(candidates, states), results, report, nil
		}
		return ssh.Collect(ctx, sshRunner, assets, host.Target, collectOptions)
	}
	collectHosts := func(ctx context.Context, hosts []app.Host) app.Result {
		return app.CollectHosts(ctx, hosts, 4, collectHost)
	}
	hostCache := app.HostCache{}
	if cacheDir, err := app.CachePath(); err == nil {
		hostCache = app.HostCache{
			Load: func(target string) ([]session.Session, bool) {
				return app.LoadHostCache(cacheDir, target)
			},
			Save: func(target string, sessions []session.Session) {
				_ = app.SaveHostCache(cacheDir, target, sessions)
			},
		}
	}
	dependencies := app.Dependencies{
		LoadTopology: app.LoadTopology,
		AddHost:      app.Add,
		Collect:      collectHosts,
		RunInteractive: func(ctx context.Context, hosts []app.Host) error {
			hostsByTarget := make(map[string]app.Host, len(hosts))
			for _, host := range hosts {
				hostsByTarget[host.Target] = host
			}
			return runTUI(ctx, tui.Dependencies{
				Collect: func(ctx context.Context) <-chan tui.Update {
					updates := make(chan tui.Update)
					go func() {
						defer close(updates)
						app.CollectHostsStream(ctx, hosts, 4, collectHost, hostCache, func(snapshot app.Snapshot) {
							update := tui.Update{
								Result: tui.Result{
									Hosts:    snapshot.Result.Hosts,
									Sessions: snapshot.Result.Sessions,
									Errors:   snapshot.Result.Errors,
									Warnings: snapshot.Result.Warnings,
								},
								Stale: snapshot.Stale,
								Done:  snapshot.Done,
							}
							select {
							case updates <- update:
							case <-ctx.Done():
								return
							}
						})
					}()
					return updates
				},
				Attach: func(ctx context.Context, item session.Session) (tui.ExecCommand, error) {
					host, ok := hostsByTarget[item.Host]
					if !ok {
						return nil, fmt.Errorf("session host is not selected")
					}
					adapter, ok := provider.Lookup(item.Provider)
					if !ok {
						return nil, fmt.Errorf("unsupported session provider")
					}
					spec, err := adapter.Resume(item.NativeID)
					if err != nil {
						return nil, fmt.Errorf("build provider resume command: %w", err)
					}
					if host.Local {
						return runtime.NewAttachCommand(ctx, runtimeRunner, item, spec)
					}
					return ssh.NewAttachCommand(ctx, host.Target, item, spec)
				},
				Preview: func(ctx context.Context, item session.Session) ([]byte, error) {
					host, ok := hostsByTarget[item.Host]
					if !ok {
						return nil, fmt.Errorf("session host is not selected")
					}
					if host.Local {
						return runtime.CapturePane(ctx, runtimeRunner, string(item.Provider), item.NativeID)
					}
					return ssh.CapturePane(ctx, sshRunner, host.Target, string(item.Provider), item.NativeID)
				},
				Kill: func(ctx context.Context, item session.Session) error {
					host, ok := hostsByTarget[item.Host]
					if !ok {
						return fmt.Errorf("session host is not selected")
					}
					if host.Local {
						return runtime.KillSession(ctx, runtimeRunner, string(item.Provider), item.NativeID)
					}
					return ssh.KillSession(ctx, sshRunner, host.Target, string(item.Provider), item.NativeID)
				},
				LocalTarget: app.LocalhostTarget,
			}, os.Stdin, os.Stdout, term.IsTerminal)
		},
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	exitCode := app.Run(ctx, os.Args[1:], dependencies)
	stop()
	os.Exit(exitCode)
}

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

func combineRuntime(candidates []session.Candidate, states map[string]session.Runtime) []session.Discovered {
	discovered := make([]session.Discovered, len(candidates))
	for index, candidate := range candidates {
		state, ok := states[runtime.Key(string(candidate.Provider), candidate.NativeID)]
		if !ok {
			state = session.Runtime{State: session.RuntimeSaved}
		}
		discovered[index] = session.Discovered{Candidate: candidate, Runtime: state}
	}
	return discovered
}
