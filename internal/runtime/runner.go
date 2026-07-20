package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
)

const maxInspectOutputBytes = 2 << 20

var errInspectOutputLimit = errors.New("tmux output exceeds limit")

type Command struct {
	Name string
	Args []string
	Env  []string
	Dir  string
}

type Runner interface {
	Output(context.Context, Command) ([]byte, error)
	Run(context.Context, Command, io.Reader, io.Writer, io.Writer) error
}

type SystemRunner struct{}

func (SystemRunner) Output(ctx context.Context, value Command) ([]byte, error) {
	command := systemCommand(ctx, value)
	var output boundedOutput
	command.Stdout = &output
	err := command.Run()
	if output.exceeded || output.Len() > maxInspectOutputBytes {
		return nil, errInspectOutputLimit
	}
	if err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (SystemRunner) Run(
	ctx context.Context,
	value Command,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	command := systemCommand(ctx, value)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func systemCommand(ctx context.Context, value Command) *exec.Cmd {
	command := exec.CommandContext(ctx, value.Name, value.Args...)
	command.Dir = value.Dir
	command.Env = append(os.Environ(), value.Env...)
	return command
}

type boundedOutput struct {
	bytes.Buffer
	exceeded bool
}

func (output *boundedOutput) Write(value []byte) (int, error) {
	remaining := maxInspectOutputBytes + 1 - output.Len()
	if len(value) <= remaining {
		return output.Buffer.Write(value)
	}
	if remaining > 0 {
		_, _ = output.Buffer.Write(value[:remaining])
	}
	output.exceeded = true
	return remaining, errInspectOutputLimit
}
