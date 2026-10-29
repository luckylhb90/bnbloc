package bridge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"maps"
	"time"

	"github.com/luckylhb90/bnbloc/component"
	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/services/bridge/hosttool"
	"github.com/luckylhb90/bnbloc/services/bridge/roomtools"
)

// Room tools (docs/architecture/room-tools.md): every session the bridge
// creates on an agentProfile core gets the loopback room tools MCP server,
// with a bearer of its own bound to the job the session is driving.

// EnableRoomTools starts the room tools server and registers it as a
// session contributor. shimCommand is this executable (the stdio shim is
// its `roomtools-stdio` subcommand); "" offers only the http transport.
// The returned stop closes the server and revokes every bearer. Call it
// before Run.
func (b *Bridge) EnableRoomTools(shimCommand string) (stop func(), err error) {
	st := loadRoomToolsState(b.cfg.StateDir)
	proxyAddr := ""
	if port := b.cfg.Sandbox.hostProxyPort(); port != "" {
		proxyAddr = net.JoinHostPort("127.0.0.1", port)
	}
	srv, err := roomtools.Start(roomtools.Config{Gateway: roomToolsGateway{b}, GatewayBaseURL: b.cfg.GatewayBaseURL, Addr: st.Addr, Names: b.displayNames, EgressDir: b.cfg.StateDir, ProxyAddr: proxyAddr, GuestHost: b.cfg.Sandbox.HostProxy, PrivateHosts: b.cfg.Sandbox.PrivateHosts})
	if err != nil {
		return nil, err
	}
	st.Addr = srv.Addr()
	st.save(b.cfg.StateDir)
	c := &roomToolsContributor{b: b, srv: srv, shimCommand: shimCommand, key: st.key}
	b.AddSessionContributor(c)
	if b.sandbox != nil && srv.ProxyListening() {
		b.sandbox.spaceEnv = c.sandboxEgressEnv
	}
	b.roomCalls = srv.TakeCalls
	return func() { _ = srv.Close() }, nil
}

// The room tools server keeps, in the bridge's state directory, what lets a
// session restored after a restart keep the server its agent was given
// (services/bridge/session_restore.go): the address it listens on
// (roomtools.json; a busy address means a new one, and those sessions are
// started anew) and the key its session bearers derive from
// (roomtools.key, owner-only), so a session's bearer can be derived again
// from the nonce its binding kept. Without a state directory both are
// per run.
const (
	roomToolsStateFile = "roomtools.json"
	roomToolsKeyFile   = "roomtools.key"
)

type roomToolsState struct {
	Addr string `json:"addr"`
	key  []byte
}

func loadRoomToolsState(dir string) roomToolsState {
	var st roomToolsState
	if dir != "" {
		if raw, err := os.ReadFile(filepath.Join(dir, roomToolsStateFile)); err == nil {
			_ = json.Unmarshal(raw, &st)
		}
		if raw, err := os.ReadFile(filepath.Join(dir, roomToolsKeyFile)); err == nil && len(raw) >= 32 {
			st.key = raw
		}
	}
	if st.key == nil {
		st.key = make([]byte, 32)
		_, _ = rand.Read(st.key)
		if dir != "" {
			if err := os.MkdirAll(dir, 0o700); err == nil {
				if err := os.WriteFile(filepath.Join(dir, roomToolsKeyFile), st.key, 0o600); err != nil {
					slog.Warn("bnbloc bridge room tools key not saved", "event", "bnbloc.bridge.roomtools_key_save_failed", "error", err)
				}
			}
		}
	}
	return st
}

func (st roomToolsState) save(dir string) {
	if dir == "" {
		return
	}
	if err := writeFileAtomic(filepath.Join(dir, roomToolsStateFile), st); err != nil {
		slog.Warn("bnbloc bridge room tools state not saved", "event", "bnbloc.bridge.roomtools_state_save_failed", "error", err)
	}
}

// roomToolsGateway is the tools' gateway client: the bridge's own
// authenticated session, never handed to the agent.
type roomToolsGateway struct{ b *Bridge }

func (g roomToolsGateway) Do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var raw []byte
	var err error
	if rb, ok := body.(roomtools.RawBody); ok {
		header := rb.Header.Clone()
		var data io.Reader
		if rb.ContentType != "" {
			if header == nil {
				header = http.Header{}
			}
			header.Set("Content-Type", rb.ContentType)
			data = bytes.NewReader(rb.Data)
		}
		raw, _, err = g.b.send(ctx, g.client(rb), method, g.b.cfg.GatewayBaseURL+path, g.b.gatewayAuth(ctx), header, data)
	} else {
		raw, err = g.b.do(ctx, method, g.b.cfg.GatewayBaseURL+path, g.b.gatewayAuth(ctx), body)
	}
	var ge *gatewayError
	if errors.As(err, &ge) {
		return nil, &roomtools.StatusError{Status: ge.Status, Body: ge.Body}
	}
	return raw, err
}

// client is the HTTP client a call goes on: a room relay's call on the
// bridge's client without a total timeout, so it lasts as long as the tool
// call that made it (ctx; docs/prd/2026-10-19-video-generation.md §2.4:
// an image or a video's publish may take longer than any fixed limit),
// every other room tool's on the bridge's short-call client.
func (g roomToolsGateway) client(rb roomtools.RawBody) *http.Client {
	if rb.Relay {
		return g.b.stream
	}
	return g.b.http
}

// hostToolEnv is the environment of a tool the bridge runs on the host: this
// process's, without the bridge's own settings and credentials (BNBLOC_*), then
// the tool's own (its settings), then the room relay when the room is its
// credential. Never the agent's environment.
func hostToolEnv(tool HostTool, relayURL, token string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "BNBLOC_") {
			env = append(env, kv)
		}
	}
	for _, nv := range tool.Server.Env {
		env = append(env, nv.Name+"="+nv.Value)
	}
	if tool.Relayed {
		env = append(env, pkgmeta.RoomRelayURLEnv+"="+relayURL, pkgmeta.RoomRelayTokenEnv+"="+token)
	}
	return env
}

// agentURL is the address an agent is given for path on the room tools server: the
// server's own loopback address, or, for an agent that runs inside the sandbox
// (its loopback is its own), the guest-facing address of the proxy port. An agent
// that runs on this computer with only its commands in the sandbox keeps the
// loopback one. An error when the agent is in the guest and that port is not up: a
// loopback address would only fail inside the agent, and an agent that stops on a
// tool it cannot reach (dsh does) takes the whole session with it.
func (c *roomToolsContributor) agentURL(info SessionInfo, path, loopback string) (string, error) {
	if !info.InGuest {
		return loopback, nil
	}
	if u := c.srv.GuestURL(path); u != "" {
		return u, nil
	}
	return "", fmt.Errorf("the room tools are not reachable from the sandbox (the sandbox's host proxy port is not set up)")
}

// attachHostTools starts (on first use) and fronts the session's host-placed
// tools on the server: one process per tool and session, in the session's
// working directory.
func (c *roomToolsContributor) attachHostTools(sess *roomtools.Session, info SessionInfo, token string) {
	for _, tool := range info.HostTools {
		sess.AttachHostTool(tool.Server.Name, hosttool.New(hosttool.Spec{
			Name: tool.Server.Name, Command: tool.Server.Command, Args: tool.Server.Args,
			Env: hostToolEnv(tool, c.srv.RelayURL(), token), Dir: info.Workdir,
		}))
	}
}

// hostToolServer is the MCP server the agent is given for a host tool: the
// tool's endpoint on the room tools server under the tool's own name, over http
// when the agent accepts it, else through the stdio shim.
func (c *roomToolsContributor) hostToolServer(info SessionInfo, name, token string) (corepkg.MCPServer, error) {
	url, err := c.agentURL(info, roomtools.ToolsPath+"/"+name, c.srv.HostToolURL(name))
	if err != nil {
		return corepkg.MCPServer{}, err
	}
	switch {
	case (corepkg.Target{MCPTransports: info.MCPTransports}).TransportSupported(corepkg.MCPHTTP):
		return corepkg.MCPServer{Name: name, Type: corepkg.MCPHTTP, URL: url,
			Headers: []corepkg.NameValue{{Name: "Authorization", Value: "Bearer " + token}}}, nil
	case c.shimCommand != "":
		return corepkg.MCPServer{Name: name, Type: corepkg.MCPStdio, Command: c.shimCommand,
			Args: []string{"roomtools-stdio", "--url", url}, Env: []corepkg.NameValue{{Name: roomtools.TokenEnv, Value: token}}}, nil
	}
	return corepkg.MCPServer{}, fmt.Errorf("the %s agent accepts no http MCP server and no stdio shim is available for the %s tool", info.Provider, name)
}

// roomToolsContributor attaches the server to one session.
type roomToolsContributor struct {
	b           *Bridge
	srv         *roomtools.Server
	shimCommand string
	// key derives each session's bearer from the nonce its State keeps.
	key []byte
}

// roomToolsGrant is a session's State: how it reaches the server (never
// its bearer, which derives from the nonce with the key).
type roomToolsGrant struct {
	URL   string `json:"url"`
	Nonce string `json:"nonce"`
	// Egress: the session's commands go through the server's egress proxy
	// (the hosts are asked again at every turn).
	Egress bool `json:"egress,omitempty"`
}

// bearer is the session bearer a nonce derives.
func (c *roomToolsContributor) bearer(nonce string) string {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte("bnbloc room tools bearer\x00" + nonce))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// hooks are a session's turn and release callbacks. With egress, each turn
// asks the services again which hosts they handle for its job.
func (c *roomToolsContributor) hooks(sess *roomtools.Session, egress bool, sandboxWorkdir string) (turn, turnEnd func(TurnInfo), release func()) {
	return func(t TurnInfo) {
			job := roomtools.Job{JobID: t.JobID, RequesterUserID: t.RequesterUserID, ChannelID: t.ChannelID, MessageSeq: t.MessageSeq, ByRequester: t.ByRequester, Drive: t.Drive, OwnRun: t.OwnRun, Workspace: t.Workspace, Team: t.Team}
			sess.Bind(job)
			if egress && sandboxWorkdir != "" {
				c.writeSandboxCA(sandboxWorkdir)
			}
			if egress {
				ctx, cancel := context.WithTimeout(context.Background(), egressAskTimeout)
				cl := c.b.egressFor(ctx, sess.Scope().RoomID, t.JobID, sess.EgressHosts(), ownTurn(sess.Scope(), job, sandboxWorkdir != ""))
				cancel()
				sess.SetLocalProviders(cl.local)
				sess.SetEgressHosts(cl.hosts, cl.streams)
				sess.SetEgressPaths(cl.paths)
				if sandboxWorkdir != "" {
					c.writeProviderFiles(sandboxWorkdir, cl, nil)
				}
			}
		}, func(t TurnInfo) {
			sess.Unbind(t.JobID)
		}, func() {
			c.srv.Revoke(sess)
		}
}

// RestoreSession reopens a session's bearer after a bridge restart, when the
// server listens where its agent was told it does.
func (c *roomToolsContributor) RestoreSession(_ context.Context, info SessionInfo, state json.RawMessage) (SessionContribution, bool) {
	var g roomToolsGrant
	if json.Unmarshal(state, &g) != nil || g.Nonce == "" || g.URL != c.srv.URL() {
		return SessionContribution{}, false
	}
	sess, err := c.srv.OpenWithToken(roomToolsScope(info), c.bearer(g.Nonce))
	if err != nil {
		return SessionContribution{}, false
	}
	if g.Egress && info.Sandboxed && info.Workdir != "" {
		_ = c.srv.AliasEgress(c.egressAlias(info.Workdir), sess)
	}
	turn, turnEnd, release := c.hooks(sess, g.Egress, sandboxWorkdir(info))
	// The session keeps its host tools' endpoints in its profile; the tools
	// themselves ended with the bridge, so they are fronted again (and start
	// on their next call).
	c.attachHostTools(sess, info, c.bearer(g.Nonce))
	return SessionContribution{RoomTools: true, Turn: turn, TurnEnd: turnEnd, Release: release, State: state}, true
}

func roomToolsScope(info SessionInfo) roomtools.Scope {
	return roomtools.Scope{RoomID: info.RoomID, TenantID: info.TenantID, Owner: info.Owner, Core: info.Core, Provider: info.Provider}
}

// ContributeSession mints the session's bearer and attaches the server over
// http when the session's agent accepts http MCP servers, else through the
// stdio shim (every agent accepts stdio). The bearer is bound to each
// turn's job as the turn starts and revoked when the session binding is
// dropped.
func (c *roomToolsContributor) ContributeSession(ctx context.Context, info SessionInfo) (SessionContribution, error) {
	server := corepkg.MCPServer{Name: roomtools.ServerName}
	url, err := c.agentURL(info, roomtools.Endpoint, c.srv.URL())
	if err != nil {
		return SessionContribution{}, err
	}
	switch {
	case (corepkg.Target{MCPTransports: info.MCPTransports}).TransportSupported(corepkg.MCPHTTP):
		server.Type = corepkg.MCPHTTP
		server.URL = url
	case c.shimCommand != "":
		server.Type = corepkg.MCPStdio
		server.Command = c.shimCommand
		server.Args = []string{"roomtools-stdio", "--url", url}
	default:
		return SessionContribution{}, fmt.Errorf("room tools: the %s agent accepts no http MCP server and no stdio shim is available", info.Provider)
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return SessionContribution{}, fmt.Errorf("room tools: nonce: %w", err)
	}
	nonce := hex.EncodeToString(raw)
	token := c.bearer(nonce)
	sess, err := c.srv.OpenWithToken(roomToolsScope(info), token)
	if err != nil {
		return SessionContribution{}, err
	}
	egressEnv, egress := c.egressEnv(ctx, info, sess, token)
	state, _ := json.Marshal(roomToolsGrant{URL: c.srv.URL(), Nonce: nonce, Egress: egress})
	var hostServers []corepkg.MCPServer
	for _, tool := range info.HostTools {
		hs, err := c.hostToolServer(info, tool.Server.Name, token)
		if err != nil {
			c.srv.Revoke(sess)
			return SessionContribution{}, err
		}
		hostServers = append(hostServers, hs)
	}
	c.attachHostTools(sess, info, token)
	if server.Type == corepkg.MCPHTTP {
		server.Headers = []corepkg.NameValue{{Name: "Authorization", Value: "Bearer " + token}}
	} else {
		server.Env = []corepkg.NameValue{{Name: roomtools.TokenEnv, Value: token}}
	}
	turn, turnEnd, release := c.hooks(sess, egress, sandboxWorkdir(info))
	part := SessionContribution{MCPServers: append([]corepkg.MCPServer{server}, hostServers...), RoomTools: true, Turn: turn, TurnEnd: turnEnd, Release: release, State: state, Env: egressEnv}
	if len(info.RoomRelayServers) > 0 {
		// The session's room-credential tools reach the gateway through
		// this server's relay with the same bearer: bound to each turn's
		// job, revoked with the session, never the gateway session.
		part.RelayEnv = []corepkg.NameValue{
			{Name: pkgmeta.RoomRelayURLEnv, Value: c.srv.RelayURL()},
			{Name: pkgmeta.RoomRelayTokenEnv, Value: token},
		}
	}
	return part, nil
}

// egressAskTimeout bounds asking the services for a turn's hosts.
const egressAskTimeout = 10 * time.Second

// egressEnv is the session's egress environment when a service of the
// organization declares egress and the server runs its proxy: the proxy
// and CA variables, and the placeholders the services give the session's
// commands (never over the proxy's own). The hosts are claimed for the
// session's first job here and asked again at every turn. A sandboxed
// session's commands get theirs from their space (sandboxEgressEnv): its
// alias bearer is bound to the session here, and the agent's own process
// gets nothing. on: the session has egress.
func (c *roomToolsContributor) egressEnv(ctx context.Context, info SessionInfo, sess *roomtools.Session, token string) (env map[string]string, on bool) {
	if c.b == nil {
		return nil, false
	}
	proxy := c.srv.EgressEnv(token, "")
	if proxy == nil || !c.b.hasEgress(ctx, info.Sandboxed) {
		return nil, false
	}
	if info.Sandboxed && (!c.srv.ProxyListening() || info.Workdir == "" || c.srv.AliasEgress(c.egressAlias(info.Workdir), sess) != nil) {
		return nil, false
	}
	actx, cancel := context.WithTimeout(ctx, egressAskTimeout)
	cl := c.b.egressFor(actx, info.RoomID, info.JobID, nil, ownTurn(roomToolsScope(info), roomtools.Job{JobID: info.JobID, RequesterUserID: info.RequesterUserID, ChannelID: info.ChannelID, OwnRun: info.OwnRun}, info.Sandboxed))
	cancel()
	hosts, env := cl.hosts, cl.env
	sess.SetLocalProviders(cl.local)
	sess.SetEgressHosts(hosts, cl.streams)
	sess.SetEgressPaths(cl.paths)
	if info.Sandboxed {
		c.writeSandboxCA(info.Workdir)
		c.writeProviderFiles(info.Workdir, cl, nil)
		return nil, true
	}
	maps.Copy(env, proxy)
	return env, true
}

// sandboxWorkdir is a sandboxed session's workspace ("" on the host).
func sandboxWorkdir(info SessionInfo) string {
	if info.Sandboxed {
		return info.Workdir
	}
	return ""
}

// egressAlias is the egress bearer of the sandboxed session working in
// workdir (derived, so the space's environment can carry it before the
// session exists).
func (c *roomToolsContributor) egressAlias(workdir string) string {
	return c.bearer("egress:" + workdir)
}

// sandboxEgressDir is where a sandboxed workspace keeps the proxy's CA
// for its commands (the workspace is mounted at its own path in the
// guest).
const sandboxEgressDir = ".room/egress"

// sandboxEgressEnv is the default environment of an own drive's space when
// a service declares egress: the services' placeholders for the turn's job
// (what a host session's commands get, docs/bugs/2026-10-27-sandboxed-commands-miss-connector-env.md),
// the proxy at the address the guests reach it (SandboxConfig.HostProxy)
// with the workspace's alias bearer, and the CA files, copied into the
// workspace. nil: none.
func (c *roomToolsContributor) sandboxEgressEnv(ctx context.Context, workdir, room, job string, own *component.ProviderTurn) map[string]string {
	if !c.b.hasEgress(ctx, true) {
		return nil
	}
	caFile, bundleFile, ok := c.writeSandboxCA(workdir)
	if !ok {
		return nil
	}
	env := map[string]string{}
	if job != "" {
		actx, cancel := context.WithTimeout(ctx, egressAskTimeout)
		claims := c.b.egressFor(actx, room, job, nil, own)
		cancel()
		for k, v := range claims.env {
			env[k] = v
		}
		c.writeProviderFiles(workdir, claims, env)
	}
	maps.Copy(env, c.srv.EgressEnv(c.egressAlias(workdir), c.b.cfg.Sandbox.HostProxy))
	env["NODE_EXTRA_CA_CERTS"] = caFile
	if bundleFile != "" {
		for _, k := range []string{"SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE", "GIT_SSL_CAINFO"} {
			env[k] = bundleFile
		}
	}
	return env
}

// writeProviderFiles writes the local providers' files into the sandboxed
// workspace (.room/egress/<provider>/, replaced whole at each turn: a provider
// that hands none leaves none) and names the folder in env's `{files:<provider>}`
// tokens. The folder is the agent's to read: a provider puts no credential
// in it.
func (c *roomToolsContributor) writeProviderFiles(workdir string, claims egressClaims, env map[string]string) {
	base := filepath.Join(workdir, filepath.FromSlash(sandboxEgressDir))
	for provider, files := range claims.files {
		dir := filepath.Join(base, "providers", provider)
		_ = os.RemoveAll(dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			slog.Warn("bnbloc bridge provider files not written", "event", "bnbloc.bridge.provider_files_failed", "provider", provider, "error", err)
			continue
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				slog.Warn("bnbloc bridge provider file not written", "event", "bnbloc.bridge.provider_files_failed", "provider", provider, "file", name, "error", err)
			}
		}
		for k, v := range env {
			env[k] = strings.ReplaceAll(v, providerFilesToken(roomtools.LocalPrefix+provider), dir)
		}
	}
}

// writeSandboxCA copies the proxy's CA (and the bundle with the system's
// roots, "" when there is none) into a sandboxed workspace. It is written
// when the space is prepared and again when a session starts and at each
// turn: a fresh session resets the workspace's bnbloc-owned paths, .room/
// included.
func (c *roomToolsContributor) writeSandboxCA(workdir string) (caFile, bundleFile string, ok bool) {
	caPEM, bundle := c.srv.EgressCA()
	dir := filepath.Join(workdir, filepath.FromSlash(sandboxEgressDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("bnbloc bridge sandbox egress off: no CA folder", "event", "bnbloc.bridge.sandbox_egress_failed", "error", err)
		return "", "", false
	}
	caFile = filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o644); err != nil {
		slog.Warn("bnbloc bridge sandbox egress off: CA not written", "event", "bnbloc.bridge.sandbox_egress_failed", "error", err)
		return "", "", false
	}
	if len(bundle) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "bundle.pem"), bundle, 0o644); err == nil {
			bundleFile = filepath.Join(dir, "bundle.pem")
		}
	}
	return caFile, bundleFile, true
}

// sendFromHere runs a send the platform queued for this computer's owner
// (roomtools.SendFromHere); a failure is the component's to tell, here
// only logged.
func (b *Bridge) sendFromHere(ctx context.Context, send bridgeSend) {
	if err := roomtools.SendFromHere(ctx, roomToolsGateway{b}, send.SendID, send.Component, send.Method, send.URL); err != nil {
		slog.Warn("bnbloc bridge send failed", "event", "bnbloc.bridge.send_failed", "send", send.SendID, "error", err)
	}
}
