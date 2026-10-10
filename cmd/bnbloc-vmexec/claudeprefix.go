package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	sandbox "github.com/luckylhb90/bnbloc/sandbox/protocol"
)

// Claude Code runs EVERY spawn through CLAUDE_CODE_SHELL_PREFIX as
// `<prefix> '<command string>'`: Bash-tool commands, hooks, stdio MCP servers,
// the status line. Only Bash-tool commands go to the sandbox; everything else
// is host tooling and keeps running on the host. A Bash-tool command string
// has the shape
//
//	[source <shell-snapshot> 2>/dev/null || true && [export …\n]…]<setup> && eval <cmd word> [< /dev/null] && pwd -P >| <cwd file>
//
// where the cwd file is how Claude tracks the working directory between
// commands. The router rewrites it for the guest: the host shell snapshot
// (host shell state) is dropped, and the final `pwd -P` goes to a file in the
// space user's guest home — the space's own writable state dir — which vmexec
// reads back after the command, maps guest→host and writes to Claude's cwd
// file on the host. Nothing is written into the workspace and no extra host
// directory has to be shared or writable.
//
// Classification fails CLOSED: the cwd tail alone marks a Bash-tool command,
// and such a command always runs in the sandbox. The eval'ed word is parsed
// only to decide passthrough; a word the parser does not understand means no
// passthrough, never a host run (2026-10-09: Claude quotes an embedded ' as
// '"'"', which the old eval pattern did not accept, so every command with a
// single quote ran on the host — docs/bugs/2026-10-09-sandbox-quoted-claude-commands-ran-on-host.md).
var (
	claudeCwdTail  = regexp.MustCompile(`^(?s)(.*) && pwd -P >\| ('[^']*'|"[^"]*"|[^ '"]+)$`)
	claudeSnapshot = regexp.MustCompile(`^source ('[^']*'|"[^"]*"|[^ '"]+) 2>/dev/null \|\| true && `)
)

// claudeBashCommand recognizes a Bash-tool command string. It returns the
// command body without the cwd tail (and without the host shell snapshot),
// the host cwd-file path, and the eval'ed user command for the passthrough
// decision ("" when it cannot be parsed exactly).
func claudeBashCommand(s string) (body, cwdFile, userCmd string, ok bool) {
	m := claudeCwdTail.FindStringSubmatch(s)
	if m == nil {
		return "", "", "", false
	}
	body, cwdFile = m[1], unquoteShell(m[2])
	if sm := claudeSnapshot.FindStringSubmatch(body); sm != nil && strings.Contains(sm[1], "shell-snapshots") {
		body = body[len(sm[0]):]
	}
	return body, cwdFile, claudeEvalWord(body), true
}

// claudeEvalWord returns the decoded argument of the trailing
// `eval <word> [< /dev/null]` of a Bash-tool body, or "" when the body does
// not end that way or the word is not one plain shell word.
func claudeEvalWord(body string) string {
	body = strings.TrimSuffix(body, " < /dev/null")
	for i := strings.LastIndex(body, "eval "); i >= 0; i = strings.LastIndex(body[:i], "eval ") {
		if i != 0 && !strings.HasSuffix(body[:i], "&& ") {
			continue
		}
		if w, ok := shellWord(body[i+len("eval "):]); ok {
			return w
		}
	}
	return ""
}

// shellWord decodes s as exactly one POSIX shell word made of unquoted,
// 'single-quoted', "double-quoted" and backslash-escaped parts (Claude
// closes the single-quoted part around an embedded quote and writes it as
// "'" or as an escaped \'). ok is false when s holds
// anything else: whitespace or a shell operator outside quotes, an
// unterminated quote, an expansion.
func shellWord(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch c := s[i]; c {
		case '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return "", false
			}
			b.WriteString(s[i+1 : i+1+j])
			i += j + 2
		case '"':
			i++
			for {
				if i >= len(s) {
					return "", false
				}
				d := s[i]
				if d == '"' {
					i++
					break
				}
				if d == '$' || d == '`' {
					return "", false
				}
				if d == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`", s[i+1]) >= 0 {
					b.WriteByte(s[i+1])
					i += 2
					continue
				}
				b.WriteByte(d)
				i++
			}
		case '\\':
			if i+1 >= len(s) {
				return "", false
			}
			b.WriteByte(s[i+1])
			i += 2
		default:
			if strings.IndexByte(" \t\n;&|<>()$`*?[#~", c) >= 0 {
				return "", false
			}
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), b.Len() > 0
}

// unquoteShell undoes one level of POSIX single or double quoting of a word.
func unquoteShell(w string) string {
	switch {
	case len(w) >= 2 && w[0] == '\'' && w[len(w)-1] == '\'':
		return strings.ReplaceAll(w[1:len(w)-1], `'\''`, `'`)
	case len(w) >= 2 && w[0] == '"' && w[len(w)-1] == '"':
		return w[1 : len(w)-1]
	}
	return w
}

// claudePrefix is the CLAUDE_CODE_SHELL_PREFIX target. The whole command
// string is args[0].
func claudePrefix(args []string, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "bnbloc-vmexec claude-prefix: expected exactly one command string")
		return 2
	}
	command := args[0]
	body, cwdFile, userCmd, isBash := claudeBashCommand(command)
	hostShell := firstNonEmptyStr(os.Getenv("SHELL"), "/bin/sh")
	if !isBash {
		// Hooks, MCP servers, the status line: host tooling, run on the host.
		if err := execHost([]string{hostShell, "-c", command}, os.Environ()); err != nil {
			fmt.Fprintf(stderr, "bnbloc-vmexec: host spawn: %v\n", err)
			return exitFailure
		}
		return 0
	}
	r, err := newRunner(os.Stdin, os.Stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "bnbloc-vmexec: %v\n", err)
		return exitFailure
	}
	if isPassthrough([]string{userCmd}, r.cfg.Passthrough) {
		if err := r.hostExec([]string{hostShell, "-c", command}, r.environ); err != nil {
			fmt.Fprintf(stderr, "bnbloc-vmexec: passthrough: %v\n", err)
			return exitFailure
		}
		return 0
	}
	return r.runClaudeBash(context.Background(), body, cwdFile)
}

// runClaudeBash runs a Bash-tool command body in the space and relays the
// final working directory to Claude's host cwd file.
func (r *runner) runClaudeBash(ctx context.Context, body, cwdFile string) int {
	cwd, err := r.getwd()
	if err != nil {
		fmt.Fprintf(r.stderr, "bnbloc-vmexec: current directory: %v\n", err)
		return exitFailure
	}
	guestCwd, ok := mapHostPath(r.cfg.Mounts, cwd)
	if !ok {
		fmt.Fprintf(r.stderr, "bnbloc-vmexec: the current directory %s is outside the sandbox workspace (%s); refusing to run the command\n",
			cwd, strings.Join(mountHosts(r.cfg.Mounts), ", "))
		return exitRefused
	}
	stateFile := `"$HOME"/.bnbloc-cwd-` + randomID()
	script := body + " && pwd -P >| " + stateFile
	code, err := r.exec(ctx, shellCommand(script), guestCwd, false)
	if err != nil {
		return code
	}
	r.relayCwd(ctx, stateFile, cwdFile)
	return code
}

// relayCwd reads the guest cwd left by the command (absent when the command
// failed before `pwd`, exactly like Claude's own chain), removes it, maps it
// to the host and writes Claude's cwd file. Failures are silent: Claude then
// keeps its previous cwd.
func (r *runner) relayCwd(ctx context.Context, stateFile, cwdFile string) {
	res, err := r.client.SpaceRun(ctx, r.endpoint(), r.cfg.Machine, r.cfg.Space, sandbox.ExecRequest{
		Command: []string{"sh", "-c", "f=" + stateFile + `; cat "$f" 2>/dev/null; rm -f "$f"`},
	})
	if err != nil || res.ExitCode != 0 {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(res.StdoutBase64)
	if err != nil {
		return
	}
	guest := strings.TrimSpace(string(raw))
	if guest == "" {
		return
	}
	host, ok := mapGuestPath(r.cfg.Mounts, guest)
	if !ok {
		return
	}
	_ = os.WriteFile(cwdFile, []byte(host+"\n"), 0o600)
}

func randomID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
