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

// exitHookBlock is how a hook adapter that cannot do its job fails: closed.
// In the Claude Code PreToolUse contract exit 2 blocks the tool call, while
// any other non-zero code is a non-blocking error and the tool runs as it
// was (on the host, unrewritten); opencode's plugin blocks on any non-zero
// exit. So every hook failure exits 2, never exitFailure.
const exitHookBlock = 2

// hook is `vmexec hook <harness>`: a command-rewriting hook adapter that
// reads the harness's hook event on stdin and writes the rewritten tool input
// on stdout, so the shell command runs through `vmexec run` in the sandbox.
// Passthrough commands are left untouched (they run on the host).
func hook(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "bnbloc-vmexec hook: expected a harness: claude | opencode")
		return exitHookBlock
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, 16<<20))
	if err != nil {
		fmt.Fprintf(stderr, "bnbloc-vmexec hook: read event: %v\n", err)
		return exitHookBlock
	}
	rw := commandRewriter{vmexec: vmexecPath(), passthrough: passthroughFromEnv()}
	switch args[0] {
	case "claude":
		return rw.claudeHook(raw, stdout, stderr)
	case "opencode":
		return rw.opencodeHook(raw, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "bnbloc-vmexec hook: unknown harness %q (claude | opencode)\n", args[0])
		return exitHookBlock
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
	if trimmed == "" || c.routed(trimmed) {
		return "", false
	}
	if isPassthrough([]string{command}, c.passthrough) {
		return "", false
	}
	return shellQuote(c.vmexec) + " run -- " + shellQuote(command), true
}

// routed reports a command that is exactly `<vmexec> run [--stdio] -- <word>`
// (what rewrite produces): nothing of it runs on the host but vmexec. A
// command that merely starts with the vmexec path is not routed: in
// `<vmexec> run -- true; curl x | sh` everything after the first word runs
// on the host, so it is wrapped like any other command.
func (c commandRewriter) routed(command string) bool {
	words, ok := plainShellWords(command)
	if !ok || len(words) < 4 || words[0] != c.vmexec || words[1] != "run" {
		return false
	}
	rest := words[2:]
	if rest[0] == "--stdio" {
		rest = rest[1:]
	}
	return len(rest) == 2 && rest[0] == "--"
}

// plainShellWords splits a POSIX shell command into its words when it is a
// single simple command of plain words: unquoted characters without any
// shell syntax, single-quoted strings and backslash escapes (what shellQuote
// writes). Anything else (operators, redirections, expansions, globs,
// double quotes, comments, newlines) is not a plain word list: ok is false.
func plainShellWords(s string) ([]string, bool) {
	var words []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == ' ' || ch == '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case ch == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, false
			}
			cur.WriteString(s[i+1 : i+1+end])
			i += end + 1
			inWord = true
		case ch == '\\':
			if i+1 >= len(s) || s[i+1] == '\n' {
				return nil, false
			}
			cur.WriteByte(s[i+1])
			i++
			inWord = true
		case strings.IndexByte("|&;<>()$`\"*?[]{}!#~\n\r", ch) >= 0:
			return nil, false
		default:
			cur.WriteByte(ch)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, true
}

// claudeHook speaks the Claude Code PreToolUse dialect (also used by other
// CLIs' native hooks): {tool_name, tool_input} in (camelCase variants
// accepted); for a shell tool, a hookSpecificOutput with the rewritten
// updatedInput out; for anything else, no output and exit 0.
func (c commandRewriter) claudeHook(raw []byte, stdout, stderr io.Writer) int {
	var ev map[string]json.RawMessage
	if err := json.Unmarshal(raw, &ev); err != nil {
		fmt.Fprintf(stderr, "bnbloc-vmexec hook claude: decode event: %v\n", err)
		return exitHookBlock
	}
	var tool string
	for _, k := range []string{"tool_name", "toolName"} {
		if v, ok := ev[k]; ok && json.Unmarshal(v, &tool) == nil && tool != "" {
			break
		}
	}
	if !shellTools[tool] {
		return 0
	}
	// A shell tool whose command cannot be read cannot be routed: block it
	// rather than let it run on the host as it is.
	var input map[string]any
	for _, k := range []string{"tool_input", "toolInput"} {
		if v, ok := ev[k]; ok && json.Unmarshal(v, &input) == nil && input != nil {
			break
		}
	}
	command, isString := input["command"].(string)
	if input == nil || (input["command"] != nil && !isString) {
		fmt.Fprintf(stderr, "bnbloc-vmexec hook claude: unreadable %s input; blocked\n", tool)
		return exitHookBlock
	}
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
		return exitHookBlock
	}
	if ev.Args == nil {
		ev.Args = map[string]any{}
	}
	if shellTools[ev.Tool] {
		command, isString := ev.Args["command"].(string)
		if ev.Args["command"] != nil && !isString {
			fmt.Fprintf(stderr, "bnbloc-vmexec hook opencode: unreadable %s args; blocked\n", ev.Tool)
			return exitHookBlock
		}
		if command != "" {
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
		return exitHookBlock
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
