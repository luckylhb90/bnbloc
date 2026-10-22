// Package hosttool runs one stdio MCP server as a host process and lets its
// callers speak to it concurrently (docs/prd/2026-10-22-host-run-mcp-tools.md).
//
// The bridge starts a tool placed "host" itself, outside the agent's process
// tree, and fronts it on the room tools server: the agent reaches it only as
// an MCP server. This package is the process half: it starts the tool when
// the first request arrives, initializes it once, gives every request an id
// of its own towards the tool (the callers' ids are theirs), routes each
// answer back by it, forwards progress to the request that asked for it, and
// answers a request the tool makes of its client with an error (the agent's
// session is not the tool's). A tool that dies fails every request in flight
// at once and is started again by the next one.
package hosttool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// Spec says how to start the tool.
type Spec struct {
	Name    string
	Command string
	Args    []string
	// Env is the tool's whole environment (KEY=VALUE).
	Env []string
	// Dir is its working directory ("" = the bridge's).
	Dir string
}

// RPCError is a JSON-RPC error answered by the tool.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("tool error %d: %s", e.Code, e.Message) }

// ErrStopped is a call on a runner that was closed.
var ErrStopped = errors.New("the tool was stopped")

// maxLine bounds one line the tool writes (a tool's answer is one line).
const maxLine = 32 << 20

// stopGrace is how long a stopped tool has to exit after its stdin closed and
// it was asked to terminate, before it is killed.
const stopGrace = 2 * time.Second

// protocolVersion is what the runner speaks to the tool: the oldest era the
// room tools server answers, which every stdio MCP server in use accepts.
const protocolVersion = "2025-06-18"

// Runner is one tool process, started on first use.
type Runner struct {
	spec Spec

	mu     sync.Mutex
	proc   *process
	closed bool
	// starting serializes starts: a second request waits for the first's
	// initialize instead of starting a second process.
	starting sync.Mutex
}

// New returns a runner; nothing starts until the first call.
func New(spec Spec) *Runner { return &Runner{spec: spec} }

// Name is the tool's name.
func (r *Runner) Name() string { return r.spec.Name }

// Info is the tool's initialize result (its capabilities and identity),
// starting it if needed.
func (r *Runner) Info(ctx context.Context) (json.RawMessage, error) {
	p, err := r.live(ctx)
	if err != nil {
		return nil, err
	}
	return p.info, nil
}

// Call sends one request to the tool and returns its answer: the result, or
// the error the tool answered. err is set when no answer came (the tool could
// not start, died, or ctx ended). onProgress, when set, gets each progress
// notification whose token is the request's own params._meta.progressToken.
func (r *Runner) Call(ctx context.Context, method string, params json.RawMessage, onProgress func(json.RawMessage)) (json.RawMessage, *RPCError, error) {
	p, err := r.live(ctx)
	if err != nil {
		return nil, nil, err
	}
	return p.call(ctx, method, params, onProgress)
}

// Close stops the tool: its stdin closes, it is asked to terminate and, if it
// has not exited after a short grace, killed. Calls in flight fail.
func (r *Runner) Close() {
	r.mu.Lock()
	r.closed = true
	p := r.proc
	r.proc = nil
	r.mu.Unlock()
	if p != nil {
		p.stop()
	}
}

// live returns the running process, starting one when there is none (the
// first call, or after the last one died).
func (r *Runner) live(ctx context.Context) (*process, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, ErrStopped
	}
	if p := r.proc; p != nil && !p.ended() {
		r.mu.Unlock()
		return p, nil
	}
	r.mu.Unlock()

	r.starting.Lock()
	defer r.starting.Unlock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, ErrStopped
	}
	if p := r.proc; p != nil && !p.ended() {
		r.mu.Unlock()
		return p, nil
	}
	r.mu.Unlock()

	p, err := start(ctx, r.spec)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		p.stop()
		return nil, ErrStopped
	}
	r.proc = p
	r.mu.Unlock()
	return p, nil
}

type response struct {
	result json.RawMessage
	rerr   *RPCError
}

type process struct {
	spec  Spec
	cmd   *exec.Cmd
	stdin io.WriteCloser
	wmu   sync.Mutex // one writer at a time

	mu       sync.Mutex
	next     int64
	pending  map[int64]chan response
	progress map[string]func(json.RawMessage)
	info     json.RawMessage

	done chan struct{}
	err  error // why it ended; read after done closes
	tail *tailBuffer
}

func (p *process) ended() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// start spawns the tool and performs the MCP initialize handshake.
func start(ctx context.Context, spec Spec) (*process, error) {
	if spec.Command == "" {
		return nil, fmt.Errorf("the %s tool has no command", spec.Name)
	}
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Env = spec.Env
	cmd.Dir = spec.Dir
	own(cmd)
	tail := &tailBuffer{max: 2048}
	cmd.Stderr = tail
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("the %s tool did not start: %w", spec.Name, err)
	}
	p := &process{
		spec: spec, cmd: cmd, stdin: stdin, tail: tail,
		pending: map[int64]chan response{}, progress: map[string]func(json.RawMessage){}, done: make(chan struct{}),
	}
	go p.read(stdout)

	init, rerr, err := p.call(ctx, "initialize", json.RawMessage(
		`{"protocolVersion":"`+protocolVersion+`","capabilities":{},"clientInfo":{"name":"bnbloc-bridge","version":"1"}}`), nil)
	switch {
	case err != nil:
		p.stop()
		return nil, err
	case rerr != nil:
		p.stop()
		return nil, fmt.Errorf("the %s tool refused to initialize: %w", spec.Name, rerr)
	}
	p.info = init
	if err := p.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		p.stop()
		return nil, err
	}
	return p, nil
}

// call sends one request and waits for its answer.
func (p *process) call(ctx context.Context, method string, params json.RawMessage, onProgress func(json.RawMessage)) (json.RawMessage, *RPCError, error) {
	ch := make(chan response, 1)
	token := progressToken(params)
	p.mu.Lock()
	p.next++
	id := p.next
	p.pending[id] = ch
	if onProgress != nil && token != "" {
		p.progress[token] = onProgress
	}
	p.mu.Unlock()
	forget := func() {
		p.mu.Lock()
		delete(p.pending, id)
		if token != "" {
			delete(p.progress, token)
		}
		p.mu.Unlock()
	}
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if len(params) > 0 {
		msg["params"] = params
	}
	if err := p.write(msg); err != nil {
		forget()
		return nil, nil, p.exitErr(err)
	}
	select {
	case resp := <-ch:
		forget()
		return resp.result, resp.rerr, nil
	case <-p.done:
		forget()
		// An answer that arrived just before the tool ended still counts.
		select {
		case resp := <-ch:
			return resp.result, resp.rerr, nil
		default:
		}
		return nil, nil, p.exitErr(nil)
	case <-ctx.Done():
		forget()
		// Tell the tool its caller is gone (best effort).
		_ = p.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": id, "reason": "the caller went away"}})
		return nil, nil, ctx.Err()
	}
}

// progressToken is the request's params._meta.progressToken as the text of its
// JSON ("" when it has none).
func progressToken(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var p struct {
		Meta struct {
			Token json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return ""
	}
	return string(bytes.TrimSpace(p.Meta.Token))
}

func (p *process) write(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, err = p.stdin.Write(append(raw, '\n'))
	return err
}

// exitErr is the error of a call that got no answer because the tool is gone.
func (p *process) exitErr(cause error) error {
	select {
	case <-p.done:
		if p.err != nil {
			return fmt.Errorf("the %s tool stopped: %w", p.spec.Name, p.err)
		}
		return fmt.Errorf("the %s tool stopped", p.spec.Name)
	default:
	}
	if cause != nil {
		return fmt.Errorf("the %s tool did not take the request: %w", p.spec.Name, cause)
	}
	return fmt.Errorf("the %s tool stopped", p.spec.Name)
}

// read serves the tool's output until it ends, then reaps it.
func (p *process) read(stdout io.Reader) {
	br := bufio.NewReaderSize(stdout, 64<<10)
	var readErr error
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > maxLine {
			readErr = errors.New("a line longer than the limit")
			break
		}
		if t := bytes.TrimSpace(line); len(t) > 0 {
			p.dispatch(t)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = err
			}
			break
		}
	}
	waitErr := p.cmd.Wait()
	switch {
	case readErr != nil:
		p.err = readErr
	case waitErr != nil:
		p.err = waitErr
	}
	if tail := p.tail.String(); tail != "" {
		slog.Debug("bnbloc bridge host tool ended", "event", "bnbloc.bridge.hosttool_ended", "tool", p.spec.Name, "stderr_tail", tail)
	}
	close(p.done)
}

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

func (p *process) dispatch(line []byte) {
	var m message
	if json.Unmarshal(line, &m) != nil {
		return
	}
	hasID := len(m.ID) > 0 && string(m.ID) != "null"
	switch {
	case m.Method == "" && hasID: // a response
		id, err := strconv.ParseInt(string(m.ID), 10, 64)
		if err != nil {
			return
		}
		p.mu.Lock()
		ch := p.pending[id]
		p.mu.Unlock()
		if ch != nil {
			select {
			case ch <- response{result: m.Result, rerr: m.Error}:
			default:
			}
		}
	case m.Method != "" && hasID:
		// A request of the tool's client (sampling, roots): the agent's
		// session is not the tool's to ask.
		_ = p.write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{
			"code": -32601, "message": "this client does not serve requests from the tool: " + m.Method}})
	case m.Method == "notifications/progress":
		var pr struct {
			Token json.RawMessage `json:"progressToken"`
		}
		if json.Unmarshal(m.Params, &pr) != nil {
			return
		}
		p.mu.Lock()
		sink := p.progress[string(bytes.TrimSpace(pr.Token))]
		p.mu.Unlock()
		if sink != nil {
			sink(append(json.RawMessage(nil), line...))
		}
	}
}

// stop closes the tool's stdin, asks it to terminate and kills it when it
// has not gone after a short grace.
func (p *process) stop() {
	_ = p.stdin.Close()
	terminate(p.cmd)
	go func() {
		select {
		case <-p.done:
		case <-time.After(stopGrace):
			kill(p.cmd)
		}
	}()
}

// tailBuffer keeps the last max bytes written to it (the tool's stderr), for a
// debug log line; it never reaches an agent.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(b), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(bytes.TrimSpace(t.buf))
}
