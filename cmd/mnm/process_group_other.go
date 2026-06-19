//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !solaris

package main

import "os/exec"

func isolateCommandProcessGroup(_ *exec.Cmd) {}

func supportsCommandProcessGroupCleanup() bool {
	return false
}

func cleanupCommandProcessGroup(_ *exec.Cmd) error {
	return nil
}
