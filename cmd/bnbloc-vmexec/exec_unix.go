//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// execHost replaces this process with argv (resolved through PATH), so the
// host program receives signals and its exit status is the caller's.
func execHost(argv, env []string) error {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, argv, env)
}
