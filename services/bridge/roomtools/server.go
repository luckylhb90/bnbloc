// Package roomtools is the bridge's room tools MCP server
// (docs/prd/2026-10-12-experts-and-teams.md §7, docs/architecture/room-tools.md).
//
// An agent the bridge drives sees the room's agents and dispatches work to
// them (ask_agent) through a loopback MCP server inside the bridge. The
// server never hands the agent the bridge's session: the agent holds only a
// per-session bearer that is meaningless outside this process, and every
// gateway call is made by the bridge's own authenticated client. A bearer is
// bound to the job its session is driving, so it can only dispatch on behalf
// of that job, and realtime admits the dispatch only while the job runs on a
// bridge of this owner (the dispatch posts as the job's human requester).
//
// Transport: MCP Streamable HTTP on 127.0.0.1 at a random port. The server is
// dual-era: a legacy client (protocol 2025-03-26 .. 2025-11-25) opens with
// initialize; a modern client (2026-07-28) carries its version in every
// request's _meta and mirrors it into headers. No protocol sessions are
// minted. Agents without http MCP reach it through the stdio shim
// (RunStdio, `bnbloc-bridge roomtools-stdio`).
package roomtools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
)

// Protocol versions. Modern requests carry their version per request;
// legacy ones negotiate it in initialize.
const (
	ModernVersion = "2026-07-28"
	// LatestLegacyVersion is what initialize answers a client asking for a
	// legacy version this server does not know.
	LatestLegacyVersion = "2025-11-25"
)

var (
	modernVersions = []string{ModernVersion}
	legacyVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}
)

// Endpoint is the MCP endpoint path.
const Endpoint = "/mcp"

// ServerName is the server's MCP identity and the name it is attached under.
const ServerName = pkgmeta.ReservedMCPServerName

// TokenEnv carries the bearer to the stdio shim (an env var, never argv:
// argv is visible to every local user in the process list).
const TokenEnv = "BNBLOC_ROOMTOOLS_TOKEN"

// maxRequestBytes bounds one JSON-RPC request body.
const maxRequestBytes = 1 << 20

// JSON-RPC and MCP error codes.
const (
	codeParseError         = -32700
	codeInvalidRequest     = -32600
	codeMethodNotFound     = -32601
	codeInvalidParams      = -32602
	codeHeaderMismatch     = -32020
	codeUnsupportedVersion = -32022
)

// Server is the room tools MCP server of one bridge. One server serves every
// driven session; each session has its own bearer.
type Server struct {
	cfg   Config
	ln    net.Listener
	srv   *http.Server
	tools *Registry

	mu       sync.Mutex
	sessions map[[sha256.Size]byte]*Session
}

// Start listens on 127.0.0.1 (cfg.Addr when it is free, else a random
// port) and serves until Close.
func Start(cfg Config) (*Server, error) {
	cfg = cfg.withDefaults()
	if cfg.Gateway == nil {
		return nil, errors.New("roomtools: a gateway client is required")
	}
	ln, err := listenLoopback(cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("roomtools: listen: %w", err)
	}
	s := &Server{cfg: cfg, ln: ln, sessions: map[[sha256.Size]byte]*Session{}}
	s.tools = DefaultRegistry()
	mux := http.NewServeMux()
	mux.HandleFunc(Endpoint, s.handle)
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("bnbloc bridge room tools server stopped", "event", "bnbloc.bridge.roomtools_stopped", "error", err)
		}
	}()
	return s, nil
}

// listenLoopback listens on addr when it is a free loopback address, else
// on a random loopback port.
func listenLoopback(addr string) (net.Listener, error) {
	if host, _, err := net.SplitHostPort(addr); err == nil && net.ParseIP(host).IsLoopback() {
		if ln, err := net.Listen("tcp", addr); err == nil {
			return ln, nil
		}
		slog.Info("bnbloc bridge room tools address busy; listening on a new port",
			"event", "bnbloc.bridge.roomtools_addr_busy", "addr", addr)
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

// Addr is the address the server listens on.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// URL is the MCP endpoint agents are given.
func (s *Server) URL() string { return "http://" + s.ln.Addr().String() + Endpoint }

// Close stops the server and revokes every session.
func (s *Server) Close() error {
	s.mu.Lock()
	for k, sess := range s.sessions {
		sess.revoke()
		delete(s.sessions, k)
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

// Open mints a bearer for one driven session. The session dispatches
// nothing until Bind names the job it is driving.
func (s *Server) Open(scope Scope) (token string, sess *Session, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("roomtools: mint token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	sess, err = s.OpenWithToken(scope, token)
	return token, sess, err
}

// minTokenBytes bounds a caller-chosen bearer (OpenWithToken).
const minTokenBytes = 32

// OpenWithToken opens a session whose bearer the caller chose (one it can
// derive again, so a session restored after a bridge restart keeps the
// bearer its agent was given). It must be at least minTokenBytes long and
// not already in use.
func (s *Server) OpenWithToken(scope Scope, token string) (*Session, error) {
	if len(token) < minTokenBytes {
		return nil, errors.New("roomtools: bearer too short")
	}
	sess := &Session{scope: scope, token: token}
	k := sha256.Sum256([]byte(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[k] != nil {
		return nil, errors.New("roomtools: bearer already in use")
	}
	s.sessions[k] = sess
	return sess, nil
}

// Revoke ends a session: its bearer is refused from now on.
func (s *Server) Revoke(sess *Session) {
	if sess == nil {
		return
	}
	s.mu.Lock()
	k := sha256.Sum256([]byte(sess.token))
	if s.sessions[k] == sess {
		delete(s.sessions, k)
	}
	s.mu.Unlock()
	sess.revoke()
}

// authenticate resolves the request's bearer to a live session. The map is
// keyed by the token's hash and the token itself is then compared in
// constant time, so neither step leaks how much of a guess matched.
func (s *Server) authenticate(r *http.Request) *Session {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	token, ok := strings.CutPrefix(h, "Bearer ")
	token = strings.TrimSpace(token)
	if !ok || token == "" {
		return nil
	}
	s.mu.Lock()
	sess := s.sessions[sha256.Sum256([]byte(token))]
	s.mu.Unlock()
	if sess == nil || subtle.ConstantTimeCompare([]byte(sess.token), []byte(token)) != 1 || sess.revoked() {
		return nil
	}
	return sess
}

// rpcRequest is one JSON-RPC request or notification.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// requestMeta is the part of params every handler may read: the modern
// per-request metadata and the progress token.
type requestMeta struct {
	Meta map[string]json.RawMessage `json:"_meta"`
	Name string                     `json:"name"`
}

const (
	metaProtocolVersion = "io.modelcontextprotocol/protocolVersion"
	metaServerInfo      = "io.modelcontextprotocol/serverInfo"
	metaProgressToken   = "progressToken"
)

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	// DNS rebinding: a browser page must never reach the loopback server.
	// An Origin (browsers set one) must be loopback, and the Host must be
	// the address the server listens on.
	if !loopbackOrigin(r.Header.Get("Origin")) || !loopbackHost(r.Host) {
		writeRPCError(w, http.StatusForbidden, nil, codeInvalidRequest, "forbidden origin", nil)
		return
	}
	if r.Method != http.MethodPost {
		// No GET stream and no protocol sessions to DELETE.
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sess := s.authenticate(r)
	if sess == nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="bnbloc-room"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil || len(body) > maxRequestBytes {
		writeRPCError(w, http.StatusBadRequest, nil, codeInvalidRequest, "request too large or unreadable", nil)
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPCError(w, http.StatusBadRequest, nil, codeParseError, "parse error (one JSON-RPC message per POST)", nil)
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		writeRPCError(w, http.StatusBadRequest, req.ID, codeInvalidRequest, "invalid JSON-RPC request", nil)
		return
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		// A notification (notifications/initialized, cancelled, …): accepted,
		// nothing to answer.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var meta requestMeta
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &meta); err != nil {
			writeRPCError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "params must be an object", nil)
			return
		}
	}
	var modern string
	if raw, ok := meta.Meta[metaProtocolVersion]; ok {
		_ = json.Unmarshal(raw, &modern)
	}
	call := &call{srv: s, sess: sess, req: req, modern: modern != "", meta: meta}
	if call.modern {
		if status, code, msg, data := validateModern(r, req, meta, modern); status != 0 {
			writeRPCError(w, status, req.ID, code, msg, data)
			return
		}
	} else if v := strings.TrimSpace(r.Header.Get("MCP-Protocol-Version")); v != "" && !contains(legacyVersions, v) {
		writeRPCError(w, http.StatusBadRequest, req.ID, codeUnsupportedVersion, "unsupported protocol version",
			map[string]any{"supported": append(append([]string{}, modernVersions...), legacyVersions...), "requested": v})
		return
	}
	stream := wantsStream(r, meta)
	if stream {
		call.progress = newSSEWriter(w)
	}
	result, rerr := call.dispatch(r.Context())
	switch {
	case rerr != nil && rerr.Code == codeMethodNotFound && call.modern:
		// A modern server answers an unknown method with 404 (the body
		// tells it apart from a server that has no MCP endpoint here).
		writeRPCError(w, http.StatusNotFound, req.ID, rerr.Code, rerr.Message, rerr.Data)
	case stream:
		call.progress.final(req.ID, result, rerr)
	case rerr != nil:
		writeRPCError(w, http.StatusOK, req.ID, rerr.Code, rerr.Message, rerr.Data)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
}

// validateModern applies the modern header rules: MCP-Protocol-Version,
// Mcp-Method and (for tools/call) Mcp-Name must be present and match the
// body; the version must be one this server speaks.
func validateModern(r *http.Request, req rpcRequest, meta requestMeta, version string) (status, code int, msg string, data any) {
	if hv := r.Header.Get("MCP-Protocol-Version"); hv != version {
		return http.StatusBadRequest, codeHeaderMismatch, fmt.Sprintf("Header mismatch: MCP-Protocol-Version %q does not match body %q", hv, version), nil
	}
	if !contains(modernVersions, version) {
		return http.StatusBadRequest, codeUnsupportedVersion, "Unsupported protocol version",
			map[string]any{"supported": append(append([]string{}, modernVersions...), legacyVersions...), "requested": version}
	}
	if hm := r.Header.Get("Mcp-Method"); hm != req.Method {
		return http.StatusBadRequest, codeHeaderMismatch, fmt.Sprintf("Header mismatch: Mcp-Method %q does not match body %q", hm, req.Method), nil
	}
	if req.Method == "tools/call" {
		hn, ok := decodeHeaderValue(r.Header.Get("Mcp-Name"))
		if !ok || hn != meta.Name {
			return http.StatusBadRequest, codeHeaderMismatch, "Header mismatch: Mcp-Name does not match params.name", nil
		}
	}
	return 0, 0, "", nil
}

// decodeHeaderValue decodes the =?base64?…?= sentinel form of a mirrored
// header value.
func decodeHeaderValue(v string) (string, bool) {
	if strings.HasPrefix(v, "=?base64?") && strings.HasSuffix(v, "?=") && len(v) >= len("=?base64??=") {
		raw, err := base64.StdEncoding.DecodeString(v[len("=?base64?") : len(v)-2])
		if err != nil {
			return "", false
		}
		return string(raw), true
	}
	return v, true
}

// encodeHeaderValue is decodeHeaderValue's inverse: plain when the value is
// a safe header value, else the base64 sentinel.
func encodeHeaderValue(v string) string {
	safe := v != "" && strings.TrimSpace(v) == v && !(strings.HasPrefix(v, "=?base64?") && strings.HasSuffix(v, "?="))
	for i := 0; safe && i < len(v); i++ {
		if c := v[i]; c < 0x20 || c > 0x7e {
			safe = false
		}
	}
	if safe {
		return v
	}
	return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(v)) + "?="
}

// wantsStream reports whether a request is answered as an SSE stream: only
// when the caller asked for progress and accepts event streams.
func wantsStream(r *http.Request, meta requestMeta) bool {
	if _, ok := meta.Meta[metaProgressToken]; !ok {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/event-stream")
}

// call is one JSON-RPC request being served.
type call struct {
	srv      *Server
	sess     *Session
	req      rpcRequest
	modern   bool
	meta     requestMeta
	progress *sseWriter // nil = plain JSON answer
}

func (c *call) dispatch(ctx context.Context) (any, *rpcError) {
	switch c.req.Method {
	case "initialize":
		if c.modern {
			return nil, &rpcError{Code: codeMethodNotFound, Message: "initialize is a legacy method; this request carries modern _meta"}
		}
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(c.req.Params, &p)
		version := LatestLegacyVersion
		if contains(legacyVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      serverInfo(),
			"instructions":    serverInstructions,
		}, nil
	case "server/discover":
		if !c.modern {
			return nil, &rpcError{Code: codeMethodNotFound, Message: "server/discover needs modern _meta (or use initialize)"}
		}
		return c.complete(map[string]any{
			"supportedVersions": append(append([]string{}, modernVersions...), legacyVersions...),
			"capabilities":      map[string]any{"tools": map[string]any{}},
			"instructions":      serverInstructions,
		}), nil
	case "ping":
		return c.complete(map[string]any{}), nil
	case "tools/list":
		result := map[string]any{"tools": c.srv.tools.Descriptors()}
		if c.modern {
			// A 2026-07-28 list result is cacheable (SEP-2549) and a client
			// refuses one without its hints: the tools are this session's
			// (its bearer), so never cached for anyone else.
			result["ttlMs"], result["cacheScope"] = toolsListTTL.Milliseconds(), "private"
		}
		return c.complete(result), nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(c.req.Params, &p); err != nil || p.Name == "" {
			return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call needs a tool name"}
		}
		tool, ok := c.srv.tools.Lookup(p.Name)
		if !ok {
			return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool: " + p.Name}
		}
		env := &Env{
			Session:  c.sess,
			Gateway:  c.srv.cfg.Gateway,
			Config:   c.srv.cfg,
			Progress: c.progressFunc(),
		}
		res := tool.Call(ctx, env, p.Arguments)
		return c.complete(res.wire()), nil
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + c.req.Method}
	}
}

// toolsListTTL is how long a modern client may keep the tool list: the
// tools never change during a session (listChanged false).
const toolsListTTL = 5 * time.Minute

// complete stamps a modern result's resultType.
func (c *call) complete(result map[string]any) map[string]any {
	if c.modern {
		result["resultType"] = "complete"
		if _, ok := result["_meta"]; !ok && c.req.Method == "server/discover" {
			result["_meta"] = map[string]any{metaServerInfo: serverInfo()}
		}
	}
	return result
}

// progressFunc reports progress on the request's SSE stream (a no-op for a
// plain JSON answer).
func (c *call) progressFunc() func(progress float64, message string) {
	if c.progress == nil {
		return func(float64, string) {}
	}
	token := c.meta.Meta[metaProgressToken]
	return func(progress float64, message string) {
		c.progress.event(map[string]any{
			"jsonrpc": "2.0",
			"method":  "notifications/progress",
			"params":  map[string]any{"progressToken": token, "progress": progress, "message": message},
		})
	}
}

func serverInfo() map[string]any {
	return map[string]any{"name": ServerName, "title": "bnbloc room tools", "version": "1"}
}

// sseWriter answers one request as an SSE stream: progress notifications,
// then the final response.
type sseWriter struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	flusher http.Flusher
	started bool
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	f, _ := w.(http.Flusher)
	return &sseWriter{w: w, flusher: f}
}

func (s *sseWriter) event(msg any) {
	buf, err := json.Marshal(msg)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		s.w.Header().Set("Content-Type", "text/event-stream")
		s.w.Header().Set("Cache-Control", "no-store")
		s.w.Header().Set("X-Accel-Buffering", "no")
		s.w.WriteHeader(http.StatusOK)
		s.started = true
	}
	_, _ = fmt.Fprintf(s.w, "event: message\ndata: %s\n\n", buf)
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

func (s *sseWriter) final(id json.RawMessage, result any, rerr *rpcError) {
	msg := map[string]any{"jsonrpc": "2.0", "id": id}
	if rerr != nil {
		msg["error"] = rerr
	} else {
		msg["result"] = result
	}
	s.event(msg)
}

func writeRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, message string, data any) {
	msg := map[string]any{"jsonrpc": "2.0", "error": rpcError{Code: code, Message: message, Data: data}}
	if len(id) > 0 {
		msg["id"] = id
	}
	writeJSON(w, status, msg)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// loopbackOrigin admits a missing Origin (non-browser clients) or a
// loopback one.
func loopbackOrigin(origin string) bool {
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return loopbackHostname(u.Hostname())
}

func loopbackHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	return loopbackHostname(h)
}

func loopbackHostname(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// newClientMsgID is a random dispatch idempotency id.
func newClientMsgID() string {
	raw := make([]byte, 12)
	_, _ = rand.Read(raw)
	return "rt-" + hex.EncodeToString(raw)
}
