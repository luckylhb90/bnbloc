//go:build windows

package bridge

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// execTool runs the tool on this process's stdio and answers its exit
// code (Windows has no exec).
func execTool(path string, args []string) int {
	cmd := exec.Command(path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "bnbloc-bridge mcp-tool: run %s: %v\n", path, err)
		return 1
	}
	return 0
}
