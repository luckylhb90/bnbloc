//go:build !windows

package bridge

import (
	"fmt"
	"os"
	"syscall"
)

// execTool replaces this process with the tool (its stdio, environment
// and signals are the agent's own channel to it).
func execTool(path string, args []string) int {
	err := syscall.Exec(path, append([]string{path}, args...), os.Environ())
	fmt.Fprintf(os.Stderr, "bnbloc-bridge mcp-tool: run %s: %v\n", path, err)
	return 1
}
