// Command bnbloc-vmexec is the host-side helper of bnbloc's hybrid execution:
// an agent CLI stays on the member's machine (its install, plugins, MCP
// servers, login) confined by the host OS sandbox to its workspace, while the
// shell commands it runs execute inside the room's space of a Sandbox
// Protocol v1 machine. It is generic: it speaks only the Sandbox Protocol and
// knows each CLI only by its documented extension points.
//
//	bnbloc-vmexec run [--stdio] -- <command string>   run a shell command in the space
//	bnbloc-vmexec run [--stdio] -- <argv…>            run an exact argv in the space
//	bnbloc-vmexec claude-prefix <command string>      CLAUDE_CODE_SHELL_PREFIX router
//	bnbloc-vmexec confine [flags] -- <cli…>           exec the CLI under the host sandbox profile
//	bnbloc-vmexec hook claude|opencode                command-rewriting hook adapters
//
// The sandbox session comes from the environment written by the core with
// core/protocol.VMExecEnv (BNBLOC_VMEXEC_*, BNBLOC_CONFINE_*); the token is a
// short-lived space-scoped exec token, never the sandbox admin bearer.
//
// Exit codes: the command's own exit code; 125 when vmexec itself fails (bad
// environment, sandbox unreachable, protocol error); 126 when it refuses
// (cwd outside the space mounts, no host sandbox available). The hook
// adapters exit 2 when they fail: the PreToolUse code that blocks the tool.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	coreprotocol "github.com/luckylhb90/bnbloc/core/protocol"
)

const (
	exitFailure = 125
	exitRefused = 126
)

// claudePrefixArgv0 lets a symlink (or a copy) named like this act as the
// prefix router directly: CLAUDE_CODE_SHELL_PREFIX does not word-split, so the
// prefix must be a single absolute path (or "<path> --claude-prefix").
const claudePrefixArgv0 = "bnbloc-vmexec-claude-prefix"

func main() {
	os.Exit(dispatch(os.Args, os.Stdin, os.Stdout, os.Stderr))
}

func dispatch(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(argv) > 0 && trimExe(filepath.Base(argv[0])) == claudePrefixArgv0 {
		return claudePrefix(argv[1:], stderr)
	}
	if len(argv) < 2 {
		usage(stderr)
		return 2
	}
	sub, rest := argv[1], argv[2:]
	switch sub {
	case "run":
		return runCommand(rest, stdin, stdout, stderr)
	case "claude-prefix", "--claude-prefix":
		return claudePrefix(rest, stderr)
	case "confine":
		return confine(rest, stdout, stderr)
	case "hook":
		return hook(rest, stdin, stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "bnbloc-vmexec: unknown command %q\n", sub)
		usage(stderr)
		return 2
	}
}

func trimExe(name string) string {
	if ext := filepath.Ext(name); ext == ".exe" {
		return name[:len(name)-len(ext)]
	}
	return name
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage:
  bnbloc-vmexec run [--stdio] -- <command string>    shell command in the sandbox space (bash, else sh)
  bnbloc-vmexec run [--stdio] -- <argv...>           exact argv in the sandbox space
  bnbloc-vmexec claude-prefix <command string>       CLAUDE_CODE_SHELL_PREFIX router (also --claude-prefix,
                                                     or invoke as `+claudePrefixArgv0+`)
  bnbloc-vmexec confine [flags] -- <cli...>          exec a CLI under the host sandbox profile
        --harness `+harnessNames(false)+`  --allow-read P  --allow-write P  --deny-read P
        --deny-write P  --allow-write-prefix P (temp-then-rename files beside read-only config)
        --deny-keychain  --print-profile
  bnbloc-vmexec hook `+harnessNames(true)+`                 command-rewriting hook adapter (stdin -> stdout)

`)
	fmt.Fprintf(w, "environment: %s\n             %s\n",
		strings.Join([]string{coreprotocol.EnvVMExecURL, coreprotocol.EnvVMExecToken, coreprotocol.EnvVMExecMachine, coreprotocol.EnvVMExecSpace,
			coreprotocol.EnvVMExecMounts, coreprotocol.EnvVMExecPassthrough, coreprotocol.EnvVMExecEnv, coreprotocol.EnvVMExecPath, coreprotocol.EnvVMExecWorkdir}, ", "),
		strings.Join([]string{coreprotocol.EnvConfineWorkspace, coreprotocol.EnvConfineAllowRead, coreprotocol.EnvConfineAllowWrite, coreprotocol.EnvConfineDenyWrite, coreprotocol.EnvConfineDenyRead}, ", "))
}
