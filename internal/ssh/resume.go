package ssh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func Resume(ctx context.Context, runner Runner, target string, item session.Session, adapter provider.Adapter) error {
	if runner == nil {
		return fmt.Errorf("SSH runner is nil")
	}
	if adapter == nil {
		return fmt.Errorf("provider adapter is nil")
	}
	if item.Host != target {
		return fmt.Errorf("session host does not match SSH target")
	}
	if item.Provider != session.Claude && item.Provider != session.Codex {
		return fmt.Errorf("unsupported session provider")
	}
	if adapter.Name() != item.Provider {
		return fmt.Errorf("provider adapter does not match session")
	}
	if _, err := session.Bind(target, item.Candidate); err != nil {
		return fmt.Errorf("invalid resume session: %w", err)
	}

	spec, err := adapter.Resume(item.NativeID)
	if err != nil {
		return fmt.Errorf("build provider resume command: %w", err)
	}
	if !validResumeSpec(item.Provider, item.NativeID, spec) {
		return fmt.Errorf("provider returned an invalid resume command")
	}
	remoteCommand := "cd " + quotePOSIX(item.CWD) + " && exec " + spec.Executable +
		" " + strings.Join(spec.Args[:len(spec.Args)-1], " ") +
		" " + quotePOSIX(spec.Args[len(spec.Args)-1])

	err = runner.Run(ctx, "ssh", []string{"-tt", target, remoteCommand}, os.Stdin, os.Stdout, os.Stderr)
	if err == nil {
		return nil
	}
	var exitError interface{ ExitCode() int }
	if errors.As(err, &exitError) {
		return resumeError{err: err, code: exitError.ExitCode()}
	}
	return fmt.Errorf("resume SSH session: %w", err)
}

func validResumeSpec(name session.Provider, id string, spec provider.ResumeSpec) bool {
	switch name {
	case session.Claude:
		return spec.Executable == "claude" && slices.Equal(spec.Args, []string{"--resume", id})
	case session.Codex:
		return spec.Executable == "codex" && slices.Equal(spec.Args, []string{"resume", id})
	default:
		return false
	}
}

func quotePOSIX(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

type resumeError struct {
	err  error
	code int
}

func (err resumeError) Error() string { return "resume SSH session: " + err.err.Error() }
func (err resumeError) Unwrap() error { return err.err }
func (err resumeError) ExitCode() int { return err.code }
