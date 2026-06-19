package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type CommandExecutor interface {
	Run(ctx context.Context, name string, args ...string) error
}

type ShellExecutor struct {
	Stdout io.Writer
	Stderr io.Writer
}

func (executor ShellExecutor) Run(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = executor.Stdout
	command.Stderr = executor.Stderr
	command.Env = os.Environ()
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}
