package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/baleen37/agent-remote-sessions/internal/output"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

const (
	exitSuccess = 0
	exitFailure = 1
	exitUsage   = 2
)

const topLevelHelp = `Usage:
  ars [host]
  ars list --json
  ars remote add <host>

Run "ars remote --help" for remote command help.
`

const remoteHelp = `Usage:
  ars remote add <host>

Add one SSH target to the ARS host inventory.
`

type Dependencies struct {
	LoadHosts func(string) ([]Host, error)
	AddHost   func(string, string) error
	Collect   func(context.Context, []Host) Result
	Pick      func(context.Context, []session.Session) (session.Session, bool, error)
	Resume    func(context.Context, session.Session) error
	Stdout    io.Writer
	Stderr    io.Writer
}

func Run(ctx context.Context, args []string, dependencies Dependencies) int {
	stdout := dependencies.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	stderr := dependencies.Stderr
	if stderr == nil {
		stderr = io.Discard
	}

	if len(args) == 1 && args[0] == "--help" {
		fmt.Fprint(stdout, topLevelHelp)
		return exitSuccess
	}
	if len(args) == 2 && args[0] == "remote" && args[1] == "--help" {
		fmt.Fprint(stdout, remoteHelp)
		return exitSuccess
	}
	if len(args) == 3 && args[0] == "remote" && args[1] == "add" {
		if dependencies.AddHost == nil {
			fmt.Fprintln(stderr, "ars: invalid application dependencies")
			return exitFailure
		}
		configPath, err := ConfigPath()
		if err != nil {
			fmt.Fprintln(stderr, "ars:", err)
			return exitFailure
		}
		if err := dependencies.AddHost(configPath, args[2]); err != nil {
			fmt.Fprintln(stderr, "ars:", err)
			return exitFailure
		}
		return exitSuccess
	}

	target, jsonMode, valid := parseArguments(args)
	if !valid {
		fmt.Fprintln(stderr, "usage: ars [host] | ars list --json | ars remote add <host>")
		return exitUsage
	}
	if dependencies.LoadHosts == nil || dependencies.Collect == nil ||
		(!jsonMode && (dependencies.Pick == nil || dependencies.Resume == nil)) {
		fmt.Fprintln(stderr, "ars: invalid application dependencies")
		return exitFailure
	}

	configPath, err := ConfigPath()
	if err != nil {
		fmt.Fprintln(stderr, "ars:", err)
		return exitFailure
	}
	hosts, err := dependencies.LoadHosts(configPath)
	if err != nil {
		fmt.Fprintln(stderr, "ars:", err)
		return exitFailure
	}
	hosts, err = Select(hosts, target)
	if err != nil {
		fmt.Fprintln(stderr, "ars:", err)
		return exitUsage
	}

	result := dependencies.Collect(ctx, hosts)
	allFailed := everyHostFailed(result.Hosts)
	if jsonMode {
		if err := output.WriteJSON(stdout, result.Hosts, result.Sessions, result.Errors); err != nil {
			fmt.Fprintln(stderr, "ars:", err)
			return exitFailure
		}
		if allFailed {
			fmt.Fprintln(stderr, "ars: all selected hosts failed")
			return exitFailure
		}
		return exitSuccess
	}

	reportHostErrors(stderr, result.Errors)
	if allFailed {
		fmt.Fprintln(stderr, "ars: all selected hosts failed")
		return exitFailure
	}
	if len(result.Sessions) == 0 {
		fmt.Fprintln(stdout, "No sessions found.")
		return exitSuccess
	}

	selected, ok, err := dependencies.Pick(ctx, result.Sessions)
	if err != nil {
		fmt.Fprintln(stderr, "ars: select session:", err)
		return exitFailure
	}
	if !ok {
		return exitSuccess
	}
	if err := dependencies.Resume(ctx, selected); err != nil {
		fmt.Fprintln(stderr, "ars: resume session:", err)
		var exitError interface{ ExitCode() int }
		if errors.As(err, &exitError) {
			return exitError.ExitCode()
		}
		return exitFailure
	}
	return exitSuccess
}

func parseArguments(args []string) (target string, jsonMode, valid bool) {
	switch {
	case len(args) == 0:
		return "", false, true
	case len(args) == 1 && args[0] != "list" && !strings.HasPrefix(args[0], "-"):
		return args[0], false, true
	case len(args) == 2 && args[0] == "list" && args[1] == "--json":
		return "", true, true
	default:
		return "", false, false
	}
}

func everyHostFailed(hosts []output.HostResult) bool {
	if len(hosts) == 0 {
		return false
	}
	for _, host := range hosts {
		if host.Status != output.HostStatusError {
			return false
		}
	}
	return true
}

func reportHostErrors(stderr io.Writer, hostErrors []output.HostError) {
	for _, hostError := range hostErrors {
		fmt.Fprintf(stderr, "ars: %s: %s (%s)\n", hostError.Host, hostError.Message, hostError.Code)
	}
}
