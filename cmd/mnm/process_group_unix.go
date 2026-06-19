//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris

package main

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func isolateCommandProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func supportsCommandProcessGroupCleanup() bool {
	return true
}

func cleanupCommandProcessGroup(command *exec.Cmd) error {
	if command.Process == nil || command.Process.Pid <= 0 {
		return nil
	}
	pgid := command.Process.Pid
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	time.Sleep(200 * time.Millisecond)
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
