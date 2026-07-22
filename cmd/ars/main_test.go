package main

import (
	"context"
	"os"
	"testing"

	"github.com/baleen37/agent-remote-sessions/internal/tui"
)

func TestRunTUIRejectsNonTTYStdinBeforeStartingBubbleTea(t *testing.T) {
	stdin, stdout := terminalFiles(t)
	err := runTUI(context.Background(), tui.Dependencies{}, stdin, stdout, func(fd int) bool {
		return fd != int(stdin.Fd())
	})
	if err == nil || err.Error() != "interactive mode requires a TTY; use ars list --json" {
		t.Fatalf("runTUI() error = %v", err)
	}
}

func TestRunTUIRejectsNonTTYStdoutBeforeStartingBubbleTea(t *testing.T) {
	stdin, stdout := terminalFiles(t)
	err := runTUI(context.Background(), tui.Dependencies{}, stdin, stdout, func(fd int) bool {
		return fd != int(stdout.Fd())
	})
	if err == nil || err.Error() != "interactive mode requires a TTY; use ars list --json" {
		t.Fatalf("runTUI() error = %v", err)
	}
}

func terminalFiles(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	stdin, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	return stdin, stdout
}
