package ssh

import (
	"context"
	"io"
	"os/exec"
)

type Runner interface {
	Run(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error
}

type SystemRunner struct{}

func (SystemRunner) Run(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}
