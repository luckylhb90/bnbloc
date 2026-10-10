package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	coreprotocol "github.com/luckylhb90/bnbloc/core/protocol"
)

// shellTools are the tool names whose `command` input is a shell command in
// the hook dialects vmexec adapts (Claude Code's Bash tool and the shell
// tools of CLIs that speak the same PreToolUse dialect, opencode's bash).
var shellTools = map[string]bool{
	"Bash": true, "bash": true, "shell": true,
	"run_terminal_command": true, "RunTerminalCommand": true, "run_command": true,
}

// hook is `vmexec hook <harness>`: a command-rewriting hook adapter that
// reads the harness's hook event on stdin and writes the rewritten tool input
// on stdout, so the shell command runs through `vmexec run` in the sandbox.
// Passthrough commands are left untouched (they run on the host).
func hook(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "bnbloc-vmexec hook: expected a harness: claude | opencode")
		return 2
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, 16<<20))
	if err != nil {
		fmt.Fprintf(stderr, "bnbloc-vmexec hook: read event: %v\n", err)
		return exitFailure
	}
	rw := commandRewriter{vmexec: vmexecPath(), passthrough: passthroughFromEnv()}
	switch args[0] {
	case "claude":
		return rw.claudeHook(raw, stdout, stderr)
	case "opencode":
		return rw.opencodeHook(raw, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "bnbloc-vmexec hook: unknown harness %q (claude | opencode)\n", args[0])
		return 2
	}
}

type commandRewriter struct {
	vmexec      string
	passthrough []string
}

// rewrite returns the command routed through `vmexec run`, or ok=false when
// it must stay as it is (already routed, empty, or an allowed passthrough).
func (c commandRewriter) rewrite(command string) (string, bool) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" || strings.HasPrefix(trimmed, shellQuote(c.vmexec)+" ") || strings.HasPrefix(trimmed, c.vmexec+" ") {
		return "", false
	}
	if isPassthrough([]string{command}, c.passthrough) {
		return "", false
	}
	return shellQuote(c.vmexec) + " run -- " + shellQuote(command), true
}

// claudeHook speaks the Claude Code PreToolUse dialect (also used by other
// CLIs' native hooks): {tool_name, tool_input} in (camelCase variants
// accepted); for a shell tool, a hookSpecificOutput with the rewritten
// updatedInput out; for anything else, no output and exit 0.
func (c commandRewriter) claudeHook(raw []byte, stdout, stderr io.Writer) int {
	var ev map[string]json.RawMessage
	if err := json.Unmarshal(raw, &ev); err != nil {
		fmt.Fprintf(stderr, "bnbloc-vmexec hook claude: decode event: %v\n", err)
		return exitFailure
	}
	var tool string
	for _, k := range []string{"tool_name", "toolName"} {
		if v, ok := ev[k]; ok && json.Unmarshal(v, &tool) == nil && tool != "" {
			break
		}
	}
	var input map[string]any
	for _, k := range []string{"tool_input", "toolInput"} {
		if v, ok := ev[k]; ok && json.Unmarshal(v, &input) == nil && input != nil {
			break
		}
	}
	if !shellTools[tool] || input == nil {
		return 0
	}
	command, _ := input["command"].(string)
	rewritten, ok := c.rewrite(command)
	if !ok {
		return 0
	}
	input["command"] = rewritten
	out := map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":      "PreToolUse",
		"permissionDecision": "allow",
		"updatedInput":       input,
	}}
	return writeJSON(stdout, stderr, out)
}

// opencodeHook serves an opencode plugin's tool.execute.before: in
// {"tool":"bash","args":{…}}, out {"args":{…}} (rewritten for bash, unchanged
// otherwise). A non-zero exit blocks the tool.
func (c commandRewriter) opencodeHook(raw []byte, stdout, stderr io.Writer) int {
	var ev struct {
		Tool string         `json:"tool"`
		Args map[string]any `json:"args"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		fmt.Fprintf(stderr, "bnbloc-vmexec hook opencode: decode event: %v\n", err)
		return exitFailure
	}
	if ev.Args == nil {
		ev.Args = map[string]any{}
	}
	if shellTools[ev.Tool] {
		if command, _ := ev.Args["command"].(string); command != "" {
			if rewritten, ok := c.rewrite(command); ok {
				ev.Args["command"] = rewritten
			}
		}
	}
	return writeJSON(stdout, stderr, map[string]any{"args": ev.Args})
}

func writeJSON(stdout, stderr io.Writer, v any) int {
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(stderr, "bnbloc-vmexec hook: encode: %v\n", err)
		return exitFailure
	}
	return 0
}

// vmexecPath is the helper the rewritten commands call: the path the core
// declared (BNBLOC_VMEXEC_PATH), else this executable.
func vmexecPath() string {
	if p := strings.TrimSpace(os.Getenv(coreprotocol.EnvVMExecPath)); p != "" {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			return resolved
		}
		return exe
	}
	return "bnbloc-vmexec"
}

func passthroughFromEnv() []string {
	var out []string
	for _, name := range strings.Split(os.Getenv(coreprotocol.EnvVMExecPassthrough), ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// shellQuote single-quotes a word for POSIX shells.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`;&|<>()*?[]{}!#~=%") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
