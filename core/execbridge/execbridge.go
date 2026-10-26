// Package execbridge is the bridge that sends a codex's commands into a sandbox space
// (docs/prd/2026-10-22-acp-sandbox.md). Codex on the host dials CODEX_EXEC_SERVER_URL for its
// remote executor and speaks JSON-RPC over WebSocket text frames; `codex exec-server --listen
// stdio` in the space speaks the same JSON-RPC as JSONL. A core that runs codex confined on the
// host starts one Bridge per agent process, sets CODEX_EXEC_SERVER_URL to its URL, and the
// commands codex runs go through `vmexec run --stdio` into the room's space. It is one owner
// for every core that fronts codex (the bnbloc ACP core and the company core).
package execbridge

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os/exec"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// EnvURL is the variable that points codex at its remote executor: set to Bridge.URL() in the
// environment of the confined codex.
const EnvURL = "CODEX_EXEC_SERVER_URL"

// Bridge forwards codex's remote-executor connection into the
// sandbox space. codex (on the host) dials CODEX_EXEC_SERVER_URL and speaks
// JSON-RPC over WebSocket text frames; `codex exec-server --listen stdio`
// speaks the same JSON-RPC as JSONL on stdio. The bridge listens on a random
// loopback port under a random path (the path is the capability: any other
// local process that cannot read codex's environment gets a 404) and, per
// accepted connection, spawns the exec-server in the space through
// `vmexec run --stdio` and copies messages both ways until either side ends.
type Bridge struct {
	ln   net.Listener
	srv  *http.Server
	path string

	spawn func(ctx context.Context, args ...string) *exec.Cmd

	mu    sync.Mutex
	conns map[*exec.Cmd]struct{}
}

// ExecServerArgv is the vmexec invocation that runs codex's exec-server in
// the space: `run --stdio` keeps stdin/stdout piped and binary-faithful for
// the life of the connection. A stdio exec-server exits at stdin EOF, which
// the bridge sends when codex closes the WebSocket. (--exit-on-stdin-close is
// a remote-registration flag and is rejected in plain --listen mode.)
var ExecServerArgv = []string{"run", "--stdio", "--", "codex", "exec-server", "--listen", "stdio"}

// Start listens on 127.0.0.1:0. spawn builds the vmexec
// command (sandbox env + workdir).
func Start(spawn func(ctx context.Context, args ...string) *exec.Cmd) (*Bridge, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("codex exec-server bridge: listen: %w", err)
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	br := &Bridge{ln: ln, path: "/" + hex.EncodeToString(b[:]), spawn: spawn, conns: map[*exec.Cmd]struct{}{}}
	br.srv = &http.Server{Handler: http.HandlerFunc(br.serve), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = br.srv.Serve(ln) }()
	return br, nil
}

// URL is the value for CODEX_EXEC_SERVER_URL.
func (b *Bridge) URL() string { return "ws://" + b.ln.Addr().String() + b.path }

// Close stops the listener and kills every exec-server it started.
func (b *Bridge) Close() {
	_ = b.srv.Close()
	b.mu.Lock()
	for cmd := range b.conns {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	b.mu.Unlock()
}

func (b *Bridge) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != b.path {
		http.NotFound(w, r)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(-1) // fs/writeFile payloads carry whole files
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	cmd := b.spawn(ctx, ExecServerArgv...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = ws.Close(websocket.StatusInternalError, "stdin pipe")
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = ws.Close(websocket.StatusInternalError, "stdout pipe")
		return
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, n: 8 << 10}
	if err := cmd.Start(); err != nil {
		_ = ws.Close(websocket.StatusInternalError, "start exec-server")
		return
	}
	b.mu.Lock()
	b.conns[cmd] = struct{}{}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.conns, cmd)
		b.mu.Unlock()
	}()

	done := make(chan struct{})
	// guest -> codex: one JSONL line per text frame.
	go func() {
		defer close(done)
		rd := bufio.NewReaderSize(stdout, 64<<10)
		for {
			line, err := rd.ReadBytes('\n')
			if line = bytes.TrimRight(line, "\r\n"); len(line) > 0 {
				if werr := ws.Write(ctx, websocket.MessageText, line); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	// codex -> guest: each frame becomes one line. JSON-RPC messages are
	// compacted so an embedded raw newline can never split a message.
	go func() {
		defer stdin.Close()
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var buf bytes.Buffer
			if json.Compact(&buf, data) != nil {
				buf.Reset()
				buf.Write(bytes.ReplaceAll(data, []byte("\n"), nil))
			}
			buf.WriteByte('\n')
			if _, err := stdin.Write(buf.Bytes()); err != nil {
				return
			}
		}
	}()

	<-done // exec-server stdout closed: it exited or the connection ended
	err = cmd.Wait()
	status, reason := websocket.StatusNormalClosure, ""
	var ee *exec.ExitError
	if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
		status = websocket.StatusInternalError
		reason = "exec-server exited"
		if errors.As(err, &ee) {
			reason = fmt.Sprintf("exec-server exited %d", ee.ExitCode())
		}
		log.Printf("execbridge: codex exec-server ended: %v: %s", err, tail(stderr.String(), 400))
	}
	_ = ws.Close(status, reason)
}

// limitedWriter keeps the first n bytes (diagnostics only).
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := len(p)
		if k > l.n {
			k = l.n
		}
		_, _ = l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}

// tail is the last n bytes of s (diagnostics only).
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
