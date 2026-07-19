package output

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

type CommandRunner interface {
	Run(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error
}

type FZF struct {
	Runner CommandRunner
}

func (picker FZF) Select(ctx context.Context, sessions []session.Session) (session.Session, bool, error) {
	if picker.Runner == nil {
		return session.Session{}, false, fmt.Errorf("fzf runner is nil")
	}

	var input strings.Builder
	for index, item := range sessions {
		fmt.Fprintf(&input, "%d\t%s\n", index, displaySession(item))
	}
	var selected bytes.Buffer
	var stderr bytes.Buffer
	err := picker.Runner.Run(
		ctx,
		"fzf",
		[]string{"--no-multi", "--delimiter=\t", "--with-nth=2..", "--accept-nth=1"},
		strings.NewReader(input.String()),
		&selected,
		&stderr,
	)
	if err != nil {
		var exitError interface{ ExitCode() int }
		if errors.As(err, &exitError) && (exitError.ExitCode() == 1 || exitError.ExitCode() == 130) {
			return session.Session{}, false, nil
		}
		return session.Session{}, false, fmt.Errorf("run fzf: %w", err)
	}

	value := strings.TrimSuffix(selected.String(), "\n")
	if value == "" || strings.ContainsAny(value, "\r\n\t ") {
		return session.Session{}, false, fmt.Errorf("fzf returned an invalid selection")
	}
	index, err := strconv.Atoi(value)
	if err != nil || index < 0 || index >= len(sessions) {
		return session.Session{}, false, fmt.Errorf("fzf returned an invalid selection")
	}
	return sessions[index], true, nil
}

func displaySession(item session.Session) string {
	fields := []string{
		string(item.Provider),
		item.Host,
		session.Project(item.CWD),
		item.Title,
		item.CWD,
	}
	for index := range fields {
		fields[index] = sanitizeDisplay(fields[index])
	}
	return strings.Join(fields, "  ")
}

func sanitizeDisplay(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
}
