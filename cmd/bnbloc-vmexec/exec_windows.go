//go:build windows

package main

import "errors"

// execHost is unavailable on Windows: hybrid execution's host confinement and
// passthrough are POSIX-only for now.
func execHost(argv, env []string) error {
	return errors.New("host exec is not supported on windows")
}
