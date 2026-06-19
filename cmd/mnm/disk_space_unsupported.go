//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package main

import (
	"fmt"
	"runtime"
)

func freeDiskBytes(path string) (uint64, error) {
	return 0, fmt.Errorf("host disk detection is not supported on %s for %s", runtime.GOOS, path)
}
