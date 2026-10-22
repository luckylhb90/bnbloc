package roomtools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/services/bridge/hosttool"
)

// Host tools (docs/prd/2026-10-22-host-run-mcp-tools.md): an MCP tool placed
// "host" runs as a host process of the bridge, never as a child of the agent
// CLI, and this server fronts it. The agent is given
// <server>/tools/<name> as an MCP server under the tool's own name, with the
// session's bearer; the server speaks MCP Streamable HTTP to it and stdio to
// the tool, so the agent can no longer reach the tool's process, environment
// or files.
//
//   - Only the tools attached to this session answer (Session.AttachHostTool);
//     the bearer is the session's and dies with it, taking the tools along.
//   - A call that acts (anything but the handshake and the lists) needs a turn
//     bound to the session, like the room relay: between turns it is refused.
//   - The server is the tool's one client. It answers the agent's own
//     initialize from the tool's (the version negotiated with the agent, the
//     tool's capabilities and identity), and forwards every other request
//     with an id of its own towards the tool.

// ToolsPath prefixes a host tool's endpoint: <server>/tools/<name>.
const ToolsPath = "/tools"

// HostTool is a tool process the server fronts (*hosttool.Runner).
type HostTool interface {
	Info(ctx context.Context) (json.RawMessage, error)
	Call(ctx context.Context, method string, params json.RawMessage, onProgress func(json.RawMessage)) (json.RawMessage, *hosttool.RPCError, error)
	Close()
}

// HostToolURL is the MCP endpoint of the host tool called name.
func (s *Server) HostToolURL(name string) string {
	return "http://" + s.ln.Addr().String() + ToolsPath + "/" + name
}

// AttachHostTool makes t the session's tool called name. The session closes
// it when it is revoked.
func (s *Session) AttachHostTool(name string, t HostTool) {
	s.mu.Lock()
	if s.gone {
		s.mu.Unlock()
		t.Close()
		return
	}
	old := s.host[name]
	if s.host == nil {
		s.host = map[string]HostTool{}
	}
	s.host[name] = t
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

func (s *Session) hostTool(name string) HostTool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.host[name]
}

// closeHostTools stops every tool the session holds (revoke).
func (s *Session) closeHostTools() {
	s.mu.Lock()
	tools := s.host
	s.host = nil
	s.mu.Unlock()
	for _, t := range tools {
		t.Close()
	}
}

// handshake lists the methods that need no turn: a client opens and lists
// before and between turns.
var handshake = map[string]bool{
	"initialize": true, "server/discover": true, "ping": true,
	"tools/list": true, "resources/list": true, "resources/templates/list": true, "prompts/list": true,
}

func (s *Server) handleHostTool(w http.ResponseWriter, r *http.Request) {
	if !loopbackOrigin(r.Header.Get("Origin")) || !loopbackHost(r.Host) {
		writeRPCError(w, http.StatusForbidden, nil, codeInvalidRequest, "forbidden origin", nil)
		return
	}
	if r.Method != http.MethodPost {
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
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, ToolsPath), "/")
	tool := sess.hostTool(name)
	if name == "" || strings.Contains(name, "/") || tool == nil {
		http.Error(w, "no such tool in this session", http.StatusNotFound)
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
		// A notification: accepted; the host does the tool's own handshake.
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
	if modern != "" {
		if status, code, msg, data := validateModern(r, req, meta, modern); status != 0 {
			writeRPCError(w, status, req.ID, code, msg, data)
			return
		}
	} else if v := strings.TrimSpace(r.Header.Get("MCP-Protocol-Version")); v != "" && !contains(legacyVersions, v) {
		writeRPCError(w, http.StatusBadRequest, req.ID, codeUnsupportedVersion, "unsupported protocol version",
			map[string]any{"supported": append(append([]string{}, modernVersions...), legacyVersions...), "requested": v})
		return
	}
	if !handshake[req.Method] && sess.current().JobID == "" {
		// Between turns nothing acts: the tool's own rules (who asked, whether
		// the person answered) are about a turn.
		writeRPCError(w, http.StatusConflict, req.ID, codeInvalidRequest, "no turn is running in this session", nil)
		return
	}

	var sse *sseWriter
	if wantsStream(r, meta) {
		sse = newSSEWriter(w)
	}
	result, rerr := s.serveHostTool(r.Context(), tool, name, req, modern != "", sse)
	switch {
	case rerr != nil && rerr.Code == codeMethodNotFound && modern != "" && sse == nil:
		writeRPCError(w, http.StatusNotFound, req.ID, rerr.Code, rerr.Message, rerr.Data)
	case sse != nil:
		sse.final(req.ID, rawOrEmpty(result), rerr)
	case rerr != nil:
		writeRPCError(w, http.StatusOK, req.ID, rerr.Code, rerr.Message, rerr.Data)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": rawOrEmpty(result)})
	}
}

// serveHostTool answers one request: the handshake from the tool's own
// initialize result, everything else by forwarding it.
func (s *Server) serveHostTool(ctx context.Context, tool HostTool, name string, req rpcRequest, modern bool, sse *sseWriter) (json.RawMessage, *rpcError) {
	unavailable := func(err error) (json.RawMessage, *rpcError) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, &rpcError{Code: -32603, Message: "the request ended before the " + name + " tool answered"}
		}
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	switch req.Method {
	case "initialize":
		if modern {
			return nil, &rpcError{Code: codeMethodNotFound, Message: "initialize is a legacy method; this request carries modern _meta"}
		}
		info, err := tool.Info(ctx)
		if err != nil {
			return unavailable(err)
		}
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := LatestLegacyVersion
		if contains(legacyVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return withFields(info, map[string]any{"protocolVersion": version}), nil
	case "server/discover":
		if !modern {
			return nil, &rpcError{Code: codeMethodNotFound, Message: "server/discover needs modern _meta (or use initialize)"}
		}
		info, err := tool.Info(ctx)
		if err != nil {
			return unavailable(err)
		}
		var i struct {
			Capabilities json.RawMessage `json:"capabilities"`
			ServerInfo   json.RawMessage `json:"serverInfo"`
		}
		_ = json.Unmarshal(info, &i)
		out := map[string]any{"supportedVersions": append(append([]string{}, modernVersions...), legacyVersions...), "resultType": "complete"}
		if len(i.Capabilities) > 0 {
			out["capabilities"] = i.Capabilities
		}
		if len(i.ServerInfo) > 0 {
			out["_meta"] = map[string]any{metaServerInfo: i.ServerInfo}
		}
		raw, _ := json.Marshal(out)
		return raw, nil
	case "ping":
		return json.RawMessage(`{}`), nil
	}
	var onProgress func(json.RawMessage)
	if sse != nil {
		onProgress = func(raw json.RawMessage) { sse.event(raw) }
	}
	result, trerr, err := tool.Call(ctx, req.Method, req.Params, onProgress)
	switch {
	case err != nil:
		return unavailable(err)
	case trerr != nil:
		var data any
		if len(trerr.Data) > 0 {
			data = trerr.Data
		}
		return nil, &rpcError{Code: trerr.Code, Message: trerr.Message, Data: data}
	}
	if modern {
		fields := map[string]any{"resultType": "complete"}
		if req.Method == "tools/list" {
			fields["ttlMs"], fields["cacheScope"] = toolsListTTL.Milliseconds(), "private"
		}
		result = withFields(result, fields)
	}
	return result, nil
}

func rawOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

// withFields returns the JSON object raw with fields set on it (a fresh
// object holding only them when raw is not one).
func withFields(raw json.RawMessage, fields map[string]any) json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		obj = map[string]json.RawMessage{}
	}
	for k, v := range fields {
		b, _ := json.Marshal(v)
		obj[k] = b
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return out
}
