package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	coreprotocol "github.com/luckylhb90/bnbloc/core/protocol"
	sandbox "github.com/luckylhb90/bnbloc/sandbox/protocol"
)

// fakeSandbox is a minimal Sandbox Protocol space-exec endpoint: it records
// every exec request and answers with a scripted result.
type fakeSandbox struct {
	t        *testing.T
	mu       sync.Mutex
	execs    []sandbox.ExecRequest
	stdin    bytes.Buffer
	stdinEOF bool
	signals  []int
	// respond scripts one exec: it writes stdout/stderr chunks and returns
	// the exit code. stdin (when piped) is available once EOF arrived.
	respond func(req sandbox.ExecRequest, stdin func() string) (stdout, stderr string, code int)
	runOut  string // stdout of /run (the cwd relay)
	eof     chan struct{}
}

func newFakeSandbox(t *testing.T) (*fakeSandbox, *httptest.Server) {
	f := &fakeSandbox{t: t, eof: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeSandbox) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer exec-tok" {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(sandbox.ErrorEnvelope{Error: sandbox.ProtocolError{Code: sandbox.ErrorUnauthorized}})
		return
	}
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/spaces/room-space/exec"):
		var req sandbox.ExecRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.execs = append(f.execs, req)
		f.mu.Unlock()
		w.Header().Set("Content-Type", sandbox.ContentTypeNDJSON)
		enc := json.NewEncoder(w)
		_ = enc.Encode(sandbox.ExecEvent{Type: sandbox.ExecStarted, ExecID: "x1", Pid: 9})
		w.(http.Flusher).Flush()
		stdin := func() string {
			<-f.eof
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.stdin.String()
		}
		out, errOut, code := f.respond(req, stdin)
		if out != "" {
			_ = enc.Encode(sandbox.ExecEvent{Type: sandbox.ExecStdout, DataBase64: base64.StdEncoding.EncodeToString([]byte(out))})
		}
		if errOut != "" {
			_ = enc.Encode(sandbox.ExecEvent{Type: sandbox.ExecStderr, DataBase64: base64.StdEncoding.EncodeToString([]byte(errOut))})
		}
		_ = enc.Encode(sandbox.ExecEvent{Type: sandbox.ExecExit, ExitCode: &code})
	case strings.HasSuffix(p, "/spaces/room-space/run"):
		var req sandbox.ExecRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.execs = append(f.execs, req)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(sandbox.RunResult{StdoutBase64: base64.StdEncoding.EncodeToString([]byte(f.runOut))})
	case strings.HasSuffix(p, "/execs/x1/stdin"):
		data, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.stdin.Write(data)
		eof := r.URL.Query().Get("eof") == "1" && !f.stdinEOF
		if eof {
			f.stdinEOF = true
		}
		f.mu.Unlock()
		if eof {
			close(f.eof)
		}
		w.WriteHeader(http.StatusNoContent)
	case strings.HasSuffix(p, "/execs/x1/signal"):
		var s sandbox.SignalRequest
		_ = json.NewDecoder(r.Body).Decode(&s)
		f.mu.Lock()
		f.signals = append(f.signals, s.Signal)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(sandbox.ErrorEnvelope{Error: sandbox.ProtocolError{Code: sandbox.ErrorForbidden}})
	}
}

func testRunner(t *testing.T, srv *httptest.Server, ws string, stdin io.Reader, stdout, stderr io.Writer) *runner {
	t.Helper()
	return &runner{
		cfg: coreprotocol.SandboxExecution{
			URL: srv.URL, ExecToken: "exec-tok", Machine: "bnbloc", Space: "room-space",
			Mounts:      []coreprotocol.PathMount{{Host: ws, Guest: "/guest/ws"}},
			Passthrough: []string{"gh"},
		},
		client:   sandbox.NewClient(srv.Client()),
		environ:  []string{"TERM=xterm", "LC_ALL=C", "SECRET_TOKEN=nope", "GIT_AUTHOR_NAME=me"},
		getwd:    func() (string, error) { return filepath.Join(ws, "sub"), nil },
		hostExec: func(argv, env []string) error { t.Fatalf("unexpected host exec %v", argv); return nil },
		stdin:    stdin,
		stdout:   stdout,
		stderr:   stderr,
	}
}

func TestRun_streamsInTheSpace_withMappedCwdAndExactExitCode(t *testing.T) {
	f, srv := newFakeSandbox(t)
	f.respond = func(req sandbox.ExecRequest, _ func() string) (string, string, int) {
		return "out\x00bin\n", "warn\n", 7
	}
	var stdout, stderr bytes.Buffer
	r := testRunner(t, srv, "/host/ws", nil, &stdout, &stderr)

	code := r.run(context.Background(), []string{"make test | tee log"}, false)

	if code != 7 || stdout.String() != "out\x00bin\n" || stderr.String() != "warn\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	req := f.execs[0]
	if req.Workdir != "/guest/ws/sub" || req.StdinPipe {
		t.Fatalf("exec request = %+v", req)
	}
	if want := shellCommand("make test | tee log"); strings.Join(req.Command, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("command = %q", req.Command)
	}
	if req.Env["TERM"] != "xterm" || req.Env["LC_ALL"] != "C" || req.Env["GIT_AUTHOR_NAME"] != "me" || req.Env["SECRET_TOKEN"] != "" {
		t.Fatalf("forwarded env = %v (credentials must never cross)", req.Env)
	}
}

func TestRun_argvIsExact_andStdinIsPiped(t *testing.T) {
	f, srv := newFakeSandbox(t)
	f.respond = func(req sandbox.ExecRequest, stdin func() string) (string, string, int) {
		return strings.ToUpper(stdin()), "", 0
	}
	var stdout bytes.Buffer
	r := testRunner(t, srv, "/host/ws", strings.NewReader("line-1\nline-2\n"), &stdout, io.Discard)

	code := r.run(context.Background(), []string{"codex", "exec-server", "--listen", "stdio"}, true)

	if code != 0 || stdout.String() != "LINE-1\nLINE-2\n" {
		t.Fatalf("code=%d stdout=%q", code, stdout.String())
	}
	if got := f.execs[0]; strings.Join(got.Command, " ") != "codex exec-server --listen stdio" || !got.StdinPipe {
		t.Fatalf("exec request = %+v", got)
	}
}

func TestRun_refusesCwdOutsideTheMounts(t *testing.T) {
	_, srv := newFakeSandbox(t)
	var stderr bytes.Buffer
	r := testRunner(t, srv, "/host/ws", nil, io.Discard, &stderr)
	r.getwd = func() (string, error) { return "/somewhere/else", nil }

	if code := r.run(context.Background(), []string{"ls"}, false); code != exitRefused {
		t.Fatalf("code = %d, want %d", code, exitRefused)
	}
	if !strings.Contains(stderr.String(), "outside the sandbox workspace") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRun_passthroughRunsOnTheHost(t *testing.T) {
	_, srv := newFakeSandbox(t)
	r := testRunner(t, srv, "/host/ws", nil, io.Discard, io.Discard)
	var got []string
	r.hostExec = func(argv, env []string) error { got = argv; return nil }

	if code := r.run(context.Background(), []string{"gh pr list --json title"}, false); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if strings.Join(got, "|") != "/bin/sh|-c|gh pr list --json title" {
		t.Fatalf("host exec = %q", got)
	}
}

func TestRun_tokenRejectedIsAClearFailure(t *testing.T) {
	_, srv := newFakeSandbox(t)
	var stderr bytes.Buffer
	r := testRunner(t, srv, "/host/ws", nil, io.Discard, &stderr)
	r.cfg.ExecToken = "expired"
	if code := r.run(context.Background(), []string{"true"}, false); code != exitFailure || !strings.Contains(stderr.String(), "token was rejected") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestIsPassthrough(t *testing.T) {
	allowed := []string{"gh", "op"}
	cases := map[string]bool{
		"gh pr list":            true,
		"gh pr list; rm -rf /":  false,
		"gh $(whoami)":          false,
		"FOO=1 gh pr list":      false,
		"./gh pr list":          false,
		"/usr/local/bin/gh pr":  false,
		"git status":            false,
		"gh 'quoted; still no'": false,
		"gh pr list && echo ok": false,
	}
	for cmd, want := range cases {
		if got := isPassthrough([]string{cmd}, allowed); got != want {
			t.Errorf("isPassthrough(%q) = %v, want %v", cmd, got, want)
		}
	}
	if !isPassthrough([]string{"op", "read", "x; y"}, allowed) {
		t.Error("argv form with an allowed program must pass through (no shell involved)")
	}
	if isPassthrough([]string{"gh pr list"}, nil) {
		t.Error("an empty allowlist passes nothing through")
	}
}

func TestParseRunArgs(t *testing.T) {
	stdio, cmd, err := parseRunArgs([]string{"--stdio", "--", "codex", "exec-server"})
	if err != nil || !stdio || strings.Join(cmd, " ") != "codex exec-server" {
		t.Fatalf("%v %v %v", stdio, cmd, err)
	}
	if _, cmd, _ := parseRunArgs([]string{"--", "--weird-command"}); len(cmd) != 1 || cmd[0] != "--weird-command" {
		t.Fatalf("args after -- are the command: %v", cmd)
	}
	for _, bad := range [][]string{nil, {"--"}, {"--bogus", "--", "x"}, {"--", " "}} {
		if _, _, err := parseRunArgs(bad); err == nil {
			t.Fatalf("parseRunArgs(%q) accepted", bad)
		}
	}
}

func TestExitStatus(t *testing.T) {
	code := func(c int) *int { return &c }
	for _, tc := range []struct {
		ev   sandbox.ExecEvent
		want int
	}{
		{sandbox.ExecEvent{ExitCode: code(0)}, 0},
		{sandbox.ExecEvent{ExitCode: code(3)}, 3},
		{sandbox.ExecEvent{ExitCode: code(-9)}, 137},
		{sandbox.ExecEvent{ExitCode: code(300)}, 255},
		{sandbox.ExecEvent{TimedOut: true, ExitCode: code(-1)}, 124},
		{sandbox.ExecEvent{}, exitFailure},
	} {
		if got := exitStatus(tc.ev); got != tc.want {
			t.Errorf("exitStatus(%+v) = %d, want %d", tc.ev, got, tc.want)
		}
	}
}

// A real Bash-tool command string as Claude Code passes it to the prefix.
const claudeBash = `source /Users/m/.claude/shell-snapshots/snapshot-zsh-1791517151810-931kix.sh 2>/dev/null || true && { shopt -u extglob || setopt NO_EXTENDED_GLOB NO_BARE_GLOB_QUAL; } >/dev/null 2>&1 || true && { \builtin unalias -- 'unsetenv'; \builtin unset -f -- 'unsetenv'; } >/dev/null 2>&1 || true && eval 'echo hello-from-bash; echo '\''quoted'\''; pwd' < /dev/null && pwd -P >| /tmp/claude-6609-cwd`

func TestClaudeBashCommand_parsesTheBashToolShape(t *testing.T) {
	body, cwdFile, userCmd, ok := claudeBashCommand(claudeBash)
	if !ok {
		t.Fatal("bash-tool command not recognized")
	}
	if cwdFile != "/tmp/claude-6609-cwd" {
		t.Fatalf("cwd file = %q", cwdFile)
	}
	if strings.Contains(body, "shell-snapshots") || strings.Contains(body, "pwd -P >|") || !strings.HasPrefix(body, "{ shopt") {
		t.Fatalf("body = %q", body)
	}
	if userCmd != "echo hello-from-bash; echo 'quoted'; pwd" {
		t.Fatalf("user command = %q", userCmd)
	}
	for _, host := range []string{
		`"${CMUX_CLAUDE_HOOK_CMUX_BIN:-cmux}" hooks claude session-start`,
		`node "${CLAUDE_PLUGIN_ROOT}/scripts/session-lifecycle-hook.mjs" SessionStart`,
		`/usr/local/bin/gitnexus mcp`,
		`echo pwd -P >| /tmp/x`,
	} {
		if _, _, _, ok := claudeBashCommand(host); ok {
			t.Fatalf("host spawn %q classified as a Bash-tool command", host)
		}
	}
	// Without a usable snapshot Claude omits the source step; still a Bash command.
	if _, _, _, ok := claudeBashCommand(`eval 'ls' < /dev/null && pwd -P >| '/var/folders/x/T/claude-1a-cwd'`); !ok {
		t.Fatal("snapshot-less bash command not recognized")
	}
}

// The Bash-tool string Claude Code (2.x) really passes, captured 2026-10-09:
// plugin session exports between the snapshot and the setup, and an embedded
// single quote written as '"'"'. Before the fix this did not parse and ran
// on the HOST.
const claudeBashQuoted = "source /Users/m/.claude/shell-snapshots/snapshot-zsh-1791550058047-aj6ya5.sh 2>/dev/null || true && export CODEX_COMPANION_SESSION_ID='724ba234'\n" +
	"export CLAUDE_PLUGIN_DATA='/Users/m/.claude/plugins/data/codex-openai-codex'\n" +
	": && { shopt -u extglob || setopt NO_EXTENDED_GLOB NO_BARE_GLOB_QUAL; } >/dev/null 2>&1 || true && { \\builtin unalias -- 'unsetenv'; \\builtin unset -f -- 'unsetenv'; } >/dev/null 2>&1 || true && " +
	`eval 'ls -A /tmp/x | head -2; curl -s -o /dev/null -w '"'"'public:%{http_code}\n'"'"' -m 8 https://example.com || echo blocked' < /dev/null && pwd -P >| /tmp/claude-6568-cwd`

func TestClaudeBashCommand_quotedCommandsAreBashCommands(t *testing.T) {
	body, cwdFile, userCmd, ok := claudeBashCommand(claudeBashQuoted)
	if !ok {
		t.Fatal("a Bash-tool command with an embedded quote must be recognized (it would run on the host)")
	}
	if cwdFile != "/tmp/claude-6568-cwd" || strings.Contains(body, "shell-snapshots") || !strings.HasPrefix(body, "export CODEX_COMPANION_SESSION_ID=") {
		t.Fatalf("cwd file %q, body %q", cwdFile, body)
	}
	if want := `ls -A /tmp/x | head -2; curl -s -o /dev/null -w 'public:%{http_code}\n' -m 8 https://example.com || echo blocked`; userCmd != want {
		t.Fatalf("user command = %q, want %q", userCmd, want)
	}
}

// Fail closed: anything ending in Claude's cwd tail is a Bash-tool command
// and goes to the sandbox, even when its eval word cannot be parsed (then
// there is no user command, hence no passthrough).
func TestClaudeBashCommand_unparsableEvalStillGoesToTheSandbox(t *testing.T) {
	for _, s := range []string{
		`eval 'unterminated < /dev/null && pwd -P >| /tmp/c`,
		`eval "$(echo hi)" < /dev/null && pwd -P >| /tmp/c`,
		`setup && eval ls; rm -rf x < /dev/null && pwd -P >| /tmp/c`,
		`some-future-wrapper 'git status' && pwd -P >| /tmp/c`,
	} {
		_, _, userCmd, ok := claudeBashCommand(s)
		if !ok {
			t.Fatalf("%q not classified as a Bash-tool command", s)
		}
		if userCmd != "" {
			t.Fatalf("%q: user command %q parsed from an unparsable word (would allow passthrough)", s, userCmd)
		}
	}
}

func TestShellWord(t *testing.T) {
	for in, want := range map[string]string{
		`'a b'`:        `a b`,
		`'it'"'"'s'`:   `it's`,
		`'it'\''s'`:    `it's`,
		`"say \"hi\""`: `say "hi"`,
		`plain`:        `plain`,
		`'x'"y"\z`:     `xyz`,
	} {
		if got, ok := shellWord(in); !ok || got != want {
			t.Errorf("shellWord(%s) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{`a b`, `'a' 'b'`, `"$HOME"`, "`id`", `'open`, `a;b`, ``} {
		if got, ok := shellWord(in); ok {
			t.Errorf("shellWord(%s) = %q accepted", in, got)
		}
	}
}

func TestRunClaudeBash_runsInTheGuest_andRelaysTheCwdToTheHostFile(t *testing.T) {
	f, srv := newFakeSandbox(t)
	f.respond = func(req sandbox.ExecRequest, _ func() string) (string, string, int) { return "hello\n", "", 0 }
	f.runOut = "/guest/ws/sub/deeper\n"
	cwdFile := filepath.Join(t.TempDir(), "claude-1a-cwd")
	var stdout bytes.Buffer
	r := testRunner(t, srv, "/host/ws", nil, &stdout, io.Discard)
	body, _, _, _ := claudeBashCommand(claudeBash)

	code := r.runClaudeBash(context.Background(), body, cwdFile)

	if code != 0 || stdout.String() != "hello\n" {
		t.Fatalf("code=%d stdout=%q", code, stdout.String())
	}
	script := f.execs[0].Command[len(f.execs[0].Command)-1]
	if !strings.Contains(script, `&& pwd -P >| "$HOME"/.bnbloc-cwd-`) || strings.Contains(script, "/tmp/claude-6609-cwd") {
		t.Fatalf("guest script = %q", script)
	}
	if f.execs[0].Workdir != "/guest/ws/sub" {
		t.Fatalf("workdir = %q", f.execs[0].Workdir)
	}
	raw, err := os.ReadFile(cwdFile)
	if err != nil || string(raw) != "/host/ws/sub/deeper\n" {
		t.Fatalf("host cwd file = %q %v", raw, err)
	}
}

func TestHookClaude_rewritesShellTools(t *testing.T) {
	rw := commandRewriter{vmexec: "/opt/bnbloc tools/bnbloc-vmexec", passthrough: []string{"gh"}}
	var out bytes.Buffer
	ev := `{"tool_name":"Bash","tool_input":{"command":"go test ./... && echo 'ok'","description":"run tests"}}`
	if code := rw.claudeHook([]byte(ev), &out, io.Discard); code != 0 {
		t.Fatalf("code %d", code)
	}
	var got struct {
		HookSpecificOutput struct {
			HookEventName      string         `json:"hookEventName"`
			PermissionDecision string         `json:"permissionDecision"`
			UpdatedInput       map[string]any `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output %q: %v", out.String(), err)
	}
	h := got.HookSpecificOutput
	want := `'/opt/bnbloc tools/bnbloc-vmexec' run -- 'go test ./... && echo '\''ok'\'''`
	if h.HookEventName != "PreToolUse" || h.PermissionDecision != "allow" || h.UpdatedInput["command"] != want || h.UpdatedInput["description"] != "run tests" {
		t.Fatalf("hook output = %+v", h)
	}
	for _, ev := range []string{
		`{"tool_name":"Read","tool_input":{"file_path":"x"}}`,
		`{"toolName":"run_command","toolInput":{"command":"gh pr list"}}`,
		`{"tool_name":"Bash","tool_input":{"command":"'/opt/bnbloc tools/bnbloc-vmexec' run -- ls"}}`,
	} {
		out.Reset()
		if code := rw.claudeHook([]byte(ev), &out, io.Discard); code != 0 || out.Len() != 0 {
			t.Fatalf("event %s: code %d output %q, want silent", ev, code, out.String())
		}
	}
	out.Reset()
	rw.claudeHook([]byte(`{"toolName":"run_terminal_command","toolInput":{"command":"ls"}}`), &out, io.Discard)
	if !strings.Contains(out.String(), `run -- ls`) {
		t.Fatalf("camelCase dialect not rewritten: %q", out.String())
	}
}

func TestHookOpencode_rewritesBashArgs(t *testing.T) {
	rw := commandRewriter{vmexec: "/v/bnbloc-vmexec"}
	var out bytes.Buffer
	if code := rw.opencodeHook([]byte(`{"tool":"bash","args":{"command":"npm test","timeout":5}}`), &out, io.Discard); code != 0 {
		t.Fatalf("code %d", code)
	}
	var got struct {
		Args map[string]any `json:"args"`
	}
	_ = json.Unmarshal(out.Bytes(), &got)
	if got.Args["command"] != "/v/bnbloc-vmexec run -- 'npm test'" || got.Args["timeout"] != float64(5) {
		t.Fatalf("args = %+v", got.Args)
	}
	out.Reset()
	rw.opencodeHook([]byte(`{"tool":"read","args":{"filePath":"a"}}`), &out, io.Discard)
	if strings.TrimSpace(out.String()) != `{"args":{"filePath":"a"}}` {
		t.Fatalf("non-shell tool changed: %q", out.String())
	}
	if code := rw.opencodeHook([]byte(`not json`), io.Discard, io.Discard); code == 0 {
		t.Fatal("malformed event must block (non-zero exit)")
	}
}

func TestDispatch_claudePrefixForms(t *testing.T) {
	// A non-Bash spawn without a sandbox environment still runs on the host;
	// exercise only argument validation here (no exec).
	for _, argv := range [][]string{
		{"/x/bnbloc-vmexec-claude-prefix"},
		{"/x/bnbloc-vmexec", "claude-prefix"},
		{"/x/bnbloc-vmexec", "--claude-prefix", "a", "b"},
	} {
		var stderr bytes.Buffer
		if code := dispatch(argv, nil, io.Discard, &stderr); code != 2 || !strings.Contains(stderr.String(), "exactly one command string") {
			t.Fatalf("dispatch(%q) = %d %q", argv, code, stderr.String())
		}
	}
	if code := dispatch([]string{"x", "nope"}, nil, io.Discard, io.Discard); code != 2 {
		t.Fatalf("unknown subcommand code %d", code)
	}
}
