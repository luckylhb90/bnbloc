package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The tests start the test binary itself as a stdio MCP tool: with
// BRIDGE_FAKE_MCP set it answers initialize, tools/list and tools/call (which
// reports what the process was started with).
func TestMain(m *testing.M) {
	if os.Getenv("BRIDGE_FAKE_MCP") != "" {
		fakeMCPTool()
		return
	}
	os.Exit(m.Run())
}

func fakeMCPTool() {
	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			return
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(line, &m) != nil || m.Method == "" || len(m.ID) == 0 {
			continue
		}
		var result any
		switch m.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "mailer", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "whoami", "inputSchema": map[string]any{"type": "object"}}}}
		default:
			cwd, _ := os.Getwd()
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}},
				"pid": os.Getpid(), "cwd": cwd,
				"tool":        os.Getenv("MAILER_SETTING"),
				"relayURL":    os.Getenv(pkgmeta.RoomRelayURLEnv),
				"relayToken":  os.Getenv(pkgmeta.RoomRelayTokenEnv) != "",
				"bridgeLeak":  os.Getenv("BNBLOC_BRIDGE_SECRET") != "",
				"bridgeEnvOK": os.Getenv("PATH") != ""}
		}
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
		os.Stdout.Write(append(raw, '\n'))
	}
}

// mailerServer is the tool as the agent's own child would have been started:
// this test binary, as a fake MCP tool, with a setting for its environment.
func mailerServer() corepkg.MCPServer {
	return corepkg.MCPServer{
		Name: "mailer", Type: corepkg.MCPStdio, Command: os.Args[0],
		Env: []corepkg.NameValue{{Name: "BRIDGE_FAKE_MCP", Value: "1"}, {Name: "MAILER_SETTING", Value: "from-settings"}},
	}
}

func hostToolPost(t *testing.T, url, token, body string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("answer %d %q: %v", resp.StatusCode, raw, err)
	}
	return out
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// An own drive's tool placed "host" is recorded with what it keeps private; a
// drive for someone else keeps every tool with the agent, whatever the package
// says, so a borrower never gets an unconfined process of the owner's.
func TestHostPlacementIsRecordedOnlyOnTheMembersOwnDrive(t *testing.T) {
	f := newMCPFixture(t)
	f.setMember(t, "codeword", map[string]string{"salt": "s"})
	tool := codewordTool(f.sha)
	tool.Placement, tool.PrivatePaths = pkgmeta.MCPPlacementHost, []string{"acme/codeword"}
	refs := []wire.ContentRef{{ComponentID: "cc_reviewer", Owner: "maya", Name: "reviewer"}}
	b := &Bridge{cfg: Config{MCPToolsDir: f.tools, ComponentSettingsDir: f.settings, StateDir: t.TempDir()}}

	mcp, missing, _ := b.resolveMCP([]wire.ContentMCPTool{tool}, refs, "maya", "home", true)
	if len(missing) != 0 || len(mcp.servers) != 1 || len(mcp.host["codeword"]) != 1 || mcp.host["codeword"][0] != "acme/codeword" {
		t.Fatalf("own drive: servers=%+v host=%v missing=%v", mcp.servers, mcp.host, missing)
	}
	open := tool
	open.Settings = nil // nothing member-scoped: it would attach for anyone
	mcp, missing, _ = b.resolveMCP([]wire.ContentMCPTool{open}, refs, "maya", "home", false)
	if len(missing) != 0 || len(mcp.servers) != 1 || len(mcp.host) != 0 {
		t.Fatalf("a drive for someone else: servers=%+v host=%v missing=%v", mcp.servers, mcp.host, missing)
	}
}

func TestContentHostToolsAndPrivateDirs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	c := &jobContent{mcp: jobMCP{
		servers: []corepkg.MCPServer{{Name: "mailer"}, {Name: "browser"}, {Name: "plain", Type: corepkg.MCPHTTP, URL: "https://x.example"}},
		relayed: []string{"mailer", "browser"},
		host:    map[string][]string{"mailer": {"acme/mail", "acme/mail-cache"}, "plain": {"x"}},
	}}
	tools, rest := c.hostTools(c.relayedMCP())
	if len(tools) != 1 || tools[0].Server.Name != "mailer" || !tools[0].Relayed {
		t.Fatalf("tools = %+v (an http server is never started on the host)", tools)
	}
	if len(rest) != 1 || rest[0] != "browser" {
		t.Fatalf("relayed without the host tool = %v", rest)
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	dirs, err := c.hostPrivateDirs()
	want := []string{filepath.Join(cfg, "acme", "mail"), filepath.Join(cfg, "acme", "mail-cache")}
	if err != nil || strings.Join(dirs, "|") != strings.Join(want, "|") {
		t.Fatalf("dirs = %v, %v; want %v", dirs, err, want)
	}
	if dirs, err := (&jobContent{}).hostPrivateDirs(); dirs != nil || err != nil {
		t.Fatalf("no host tool = %v %v", dirs, err)
	}
}

// A sandboxed drive that has a host tool read-denies what the tool keeps
// private to the confined agent: in the execution the core receives.
func TestSandboxedDriveDeniesTheHostToolsStateToTheAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	gw := newSandboxGateway(t, "room-1", false)
	sbx := newFakeSandboxd(t)
	prov := &sandboxProv{supports: true}
	b, _ := sandboxBridge(t, gw, sbx, SandboxAll, prov)
	d := &drive{job: job("job-1", "alice"), owner: "alice", core: "core-a", caps: driveCaps{workdir: prov},
		content: &jobContent{mcp: jobMCP{servers: []corepkg.MCPServer{mailerServer()}, host: map[string][]string{"mailer": {"acme/mail"}}}}}
	if refuse := b.planSandbox(context.Background(), d); refuse != "" || d.prepareExec == nil {
		t.Fatalf("plan = %q, prepareExec set: %v", refuse, d.prepareExec != nil)
	}
	exec, err := d.prepareExec(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.UserConfigDir()
	if got := exec.Sandbox.HostConfine.DenyRead; len(got) != 1 || got[0] != filepath.Join(cfg, "acme", "mail") {
		t.Fatalf("DenyRead = %v", got)
	}
	// The session the contributors are told about runs the tool on the host.
	info := b.sessionInfo(d, corepkg.SessionOptions{Execution: exec})
	if !info.Sandboxed || info.Workdir != exec.Sandbox.Workdir || len(info.HostTools) != 1 || info.HostTools[0].Server.Name != "mailer" {
		t.Fatalf("info = %+v", info)
	}
	// And an unsandboxed session keeps it with the agent, as ever.
	if plain := b.sessionInfo(d, corepkg.SessionOptions{}); plain.Sandboxed || len(plain.HostTools) != 0 || plain.Workdir != "" {
		t.Fatalf("unsandboxed info = %+v", plain)
	}
}

// The whole path with a real process: the contributor fronts a host tool on the
// room tools server; the agent reaches it with its bearer; the process has the
// tool's environment and the relay but none of the bridge's own, runs in the
// session's directory, and ends with the session.
func TestHostToolEndToEnd(t *testing.T) {
	t.Setenv("BNBLOC_BRIDGE_SECRET", "bridge-only")
	g := newActivityGateway(t, "room-1")
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "alice"})
	stop, err := b.EnableRoomTools("/opt/bnbloc/bnbloc-bridge")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	contrib := b.sessionContributors()[0]
	workdir := t.TempDir()
	info := SessionInfo{RoomID: "room-1", ChannelID: "ch", Owner: "alice", Core: "core-a", Provider: "codex", JobID: "job-1", RequesterUserID: "alice",
		Sandboxed: true, Workdir: workdir, MCPTransports: []string{corepkg.MCPStdio, corepkg.MCPHTTP},
		HostTools: []HostTool{{Server: mailerServer(), Relayed: true}}}

	part, err := contrib.ContributeSession(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if len(part.MCPServers) != 2 {
		t.Fatalf("servers = %+v; want the room tools and the host tool", part.MCPServers)
	}
	host := part.MCPServers[1]
	if host.Name != "mailer" || host.Transport() != corepkg.MCPHTTP || !strings.HasSuffix(host.URL, "/tools/mailer") || len(host.Headers) != 1 {
		t.Fatalf("host tool server = %+v; the agent gets the endpoint under the tool's own name", host)
	}
	if host.Command != "" || len(host.Env) != 0 {
		t.Fatalf("the agent was given a command or environment: %+v", host)
	}
	token := strings.TrimPrefix(host.Headers[0].Value, "Bearer ")

	// The handshake works before a turn; the tool's own list comes through.
	init := hostToolPost(t, host.URL, token, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if si, _ := init["result"].(map[string]any)["serverInfo"].(map[string]any); si["name"] != "mailer" {
		t.Fatalf("initialize = %v", init)
	}
	if list := hostToolPost(t, host.URL, token, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); !strings.Contains(toJSON(list), "whoami") {
		t.Fatalf("tools/list = %v", list)
	}
	// A call needs the turn.
	if refused := hostToolPost(t, host.URL, token, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"whoami"}}`); refused["error"] == nil {
		t.Fatalf("a call between turns = %v", refused)
	}
	part.Turn(TurnInfo{JobID: "job-1", RequesterUserID: "alice", ChannelID: "ch", MessageSeq: 3})
	res := hostToolPost(t, host.URL, token, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"whoami"}}`)["result"].(map[string]any)
	if res["tool"] != "from-settings" {
		t.Fatalf("the tool's settings did not reach it: %v", res)
	}
	if res["bridgeLeak"] != false || res["bridgeEnvOK"] != true {
		t.Fatalf("environment: bridge's own leaked=%v, base present=%v", res["bridgeLeak"], res["bridgeEnvOK"])
	}
	if res["relayToken"] != true || !strings.Contains(res["relayURL"].(string), "/relay") {
		t.Fatalf("a relayed tool got no room relay: %v", res)
	}
	if cwd, _ := filepath.EvalSymlinks(res["cwd"].(string)); cwd != mustEval(t, workdir) {
		t.Fatalf("cwd = %v, want the session's %s", res["cwd"], workdir)
	}
	pid := int(res["pid"].(float64))
	if pid == os.Getpid() || !alive(pid) {
		t.Fatalf("the tool is not a process of its own (pid %d)", pid)
	}

	// The release ends the session: the bearer is refused and the process exits.
	part.Release()
	deadline := time.Now().Add(5 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if alive(pid) {
		t.Fatal("the host tool outlived its session")
	}
}

func toJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// After a bridge restart the session keeps its host tools' endpoints; the
// restore fronts them again and the tool starts on its next call.
func TestHostToolsAreFrontedAgainWhenASessionIsRestored(t *testing.T) {
	g := newActivityGateway(t, "room-1")
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "alice"})
	stop, err := b.EnableRoomTools("/opt/bnbloc/bnbloc-bridge")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	contrib := b.sessionContributors()[0]
	info := SessionInfo{RoomID: "room-1", ChannelID: "ch", Owner: "alice", Core: "core-a", Provider: "codex", JobID: "job-1", RequesterUserID: "alice",
		Sandboxed: true, Workdir: t.TempDir(), MCPTransports: []string{corepkg.MCPStdio, corepkg.MCPHTTP},
		HostTools: []HostTool{{Server: mailerServer()}}}
	part, err := contrib.ContributeSession(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	host := part.MCPServers[1]
	token := strings.TrimPrefix(host.Headers[0].Value, "Bearer ")
	part.Release() // the bridge went away: the grant and its tools with it

	restored, ok := contrib.(SessionRestorer).RestoreSession(context.Background(), info, part.State)
	if !ok {
		t.Fatal("the session was not restored")
	}
	defer restored.Release()
	if out := hostToolPost(t, host.URL, token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); !strings.Contains(toJSON(out), "whoami") {
		t.Fatalf("after a restore = %v", out)
	}
}

// An agent that accepts no http MCP server gets the host tool through the
// stdio shim, with the bearer in its environment and never in argv.
func TestHostToolThroughTheStdioShim(t *testing.T) {
	g := newActivityGateway(t, "room-1")
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "alice"})
	stop, err := b.EnableRoomTools("/opt/bnbloc/bnbloc-bridge")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	info := SessionInfo{RoomID: "room-1", Owner: "alice", Core: "core-a", Provider: "codex", Sandboxed: true, Workdir: t.TempDir(),
		HostTools: []HostTool{{Server: mailerServer()}}}
	part, err := b.sessionContributors()[0].ContributeSession(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	defer part.Release()
	h := part.MCPServers[1]
	if h.Transport() != corepkg.MCPStdio || h.Command != "/opt/bnbloc/bnbloc-bridge" || h.Args[0] != "roomtools-stdio" || !strings.HasSuffix(h.Args[2], "/tools/mailer") ||
		len(h.Env) != 1 || h.Env[0].Name != "BNBLOC_ROOMTOOLS_TOKEN" {
		t.Fatalf("shim attachment = %+v", h)
	}
	for _, a := range h.Args {
		if a == h.Env[0].Value {
			t.Fatal("the bearer leaked into argv")
		}
	}
}

// The profile a sandboxed session is created with has the host tool as the
// room tools server's endpoint, never as a command the agent would start.
func TestSandboxedSessionProfileHasNoHostToolCommand(t *testing.T) {
	g := newActivityGateway(t, "room-1")
	prov := &profileProv{}
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "alice",
		Cores: []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: g.srv.URL}}}})
	stop, err := b.EnableRoomTools("/opt/bnbloc/bnbloc-bridge")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	other := corepkg.MCPServer{Name: "codeword", Type: corepkg.MCPStdio, Command: "/tools/codeword"}
	content := &jobContent{mcp: jobMCP{servers: []corepkg.MCPServer{mailerServer(), other}, host: map[string][]string{"mailer": nil}}}
	d := &drive{job: job("job-1", "alice"), owner: "alice", core: "core-a", content: content, caps: driveCaps{profile: true}}
	info := b.sessionInfo(d, corepkg.SessionOptions{Execution: &corepkg.Execution{Sandbox: corepkg.SandboxExecution{Workdir: t.TempDir()}}})
	info.MCPTransports = []string{corepkg.MCPStdio, corepkg.MCPHTTP}

	profile, _, hooks, _ := b.sessionGuidance(context.Background(), d, corepkg.Endpoint{BaseURL: g.srv.URL}, "ws", info)
	defer func() {
		for _, r := range hooks.release {
			r()
		}
	}()
	mailer := serverNamed(profile, "mailer")
	if mailer == nil || mailer.Transport() != corepkg.MCPHTTP || mailer.Command != "" || !strings.Contains(mailer.URL, "/tools/mailer") {
		t.Fatalf("mailer = %+v; the agent gets the endpoint, not the command", mailer)
	}
	count := 0
	for _, s := range profile.MCPServers {
		if s.Name == "mailer" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the tool is attached %d times", count)
	}
	// What the agent keeps is untouched.
	if cw := serverNamed(profile, "codeword"); cw == nil || cw.Command != "/tools/codeword" {
		t.Fatalf("codeword = %+v", cw)
	}
	// The same content in an unsandboxed session leaves the tool with the agent.
	plain := b.sessionInfo(d, corepkg.SessionOptions{})
	plain.MCPTransports = info.MCPTransports
	p2, _, hooks2, _ := b.sessionGuidance(context.Background(), d, corepkg.Endpoint{BaseURL: g.srv.URL}, "ws", plain)
	defer func() {
		for _, r := range hooks2.release {
			r()
		}
	}()
	if m := serverNamed(p2, "mailer"); m == nil || m.Command != os.Args[0] {
		t.Fatalf("unsandboxed mailer = %+v; it stays the agent's child", m)
	}
}

// The turn's end reaches the contributor: the session is unbound and a host tool
// refuses calls until the next turn.
func TestTurnEndUnbindsTheSessionOfAHostTool(t *testing.T) {
	g := newActivityGateway(t, "room-1")
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "alice"})
	stop, err := b.EnableRoomTools("/opt/bnbloc/bnbloc-bridge")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	info := SessionInfo{RoomID: "room-1", Owner: "alice", Core: "core-a", Provider: "codex", Sandboxed: true, Workdir: t.TempDir(),
		MCPTransports: []string{corepkg.MCPStdio, corepkg.MCPHTTP}, HostTools: []HostTool{{Server: mailerServer()}}}
	part, err := b.sessionContributors()[0].ContributeSession(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	defer part.Release()
	if part.TurnEnd == nil {
		t.Fatal("the contribution has no turn-end hook")
	}
	host := part.MCPServers[1]
	token := strings.TrimPrefix(host.Headers[0].Value, "Bearer ")
	call := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"whoami"}}`
	part.Turn(TurnInfo{JobID: "job-1", RequesterUserID: "alice"})
	if r := hostToolPost(t, host.URL, token, call); r["result"] == nil {
		t.Fatalf("in the turn = %v", r)
	}
	part.TurnEnd(TurnInfo{JobID: "job-1"})
	if r := hostToolPost(t, host.URL, token, call); r["error"] == nil {
		t.Fatalf("after the turn's end the tool still answered: %v", r)
	}
}

// A host tool's settings file (member and platform values) is in the bridge's
// private state directory, never in the temp directory a confined agent can read;
// with no state directory the tool is not attached rather than given a readable file.
func TestAHostToolsSettingsFileIsPrivate(t *testing.T) {
	f := newMCPFixture(t)
	f.setMember(t, "codeword", map[string]string{"salt": "s"})
	state := t.TempDir()
	tool := codewordTool(f.sha)
	tool.Placement = pkgmeta.MCPPlacementHost
	refs := []wire.ContentRef{{ComponentID: "cc_reviewer", Owner: "maya", Name: "reviewer"}}

	b := &Bridge{cfg: Config{MCPToolsDir: f.tools, ComponentSettingsDir: f.settings, StateDir: state}}
	mcp, missing, _ := b.resolveMCP([]wire.ContentMCPTool{tool}, refs, "maya", "home", true)
	if len(missing) != 0 || len(mcp.servers) != 1 {
		t.Fatalf("servers=%+v missing=%v", mcp.servers, missing)
	}
	var file string
	for _, e := range mcp.servers[0].Env {
		if e.Name == pkgmeta.ComponentSettingsFileEnv {
			file = e.Value
		}
	}
	if !strings.HasPrefix(file, filepath.Join(state, "host-tool-settings")+string(filepath.Separator)) {
		t.Fatalf("the settings file is at %q; want it under the state directory", file)
	}
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("settings file: %v %v", st, err)
	}
	if st, _ := os.Stat(filepath.Dir(file)); st.Mode().Perm() != 0o700 {
		t.Fatalf("settings dir mode = %v", st.Mode().Perm())
	}
	// An agent-placed tool keeps the temp-dir file as before.
	plain := codewordTool(f.sha)
	mcp, _, _ = b.resolveMCP([]wire.ContentMCPTool{plain}, refs, "maya", "home", true)
	for _, e := range mcp.servers[0].Env {
		if e.Name == pkgmeta.ComponentSettingsFileEnv && strings.HasPrefix(e.Value, state) {
			t.Fatalf("an agent-placed tool's file moved into the state directory: %q", e.Value)
		}
	}
	// No state directory: not attached, and said.
	b2 := &Bridge{cfg: Config{MCPToolsDir: f.tools, ComponentSettingsDir: f.settings}}
	mcp, missing, _ = b2.resolveMCP([]wire.ContentMCPTool{tool}, refs, "maya", "home", true)
	if len(mcp.servers) != 0 || len(missing) != 1 || !strings.Contains(missing[0], "settings file could not be written") {
		t.Fatalf("without a state directory: servers=%+v missing=%v", mcp.servers, missing)
	}
}

// Where a tool runs and what it keeps private are part of a session's identity.
func TestBindingSumFollowsHostPlacement(t *testing.T) {
	servers := []corepkg.MCPServer{mailerServer()}
	agent := &jobContent{mcp: jobMCP{servers: servers}}
	host := &jobContent{mcp: jobMCP{servers: servers, host: map[string][]string{"mailer": {"acme/mail"}}}}
	moved := &jobContent{mcp: jobMCP{servers: servers, host: map[string][]string{"mailer": {"acme/other"}}}}
	same := &jobContent{mcp: jobMCP{servers: servers, host: map[string][]string{"mailer": {"acme/mail"}}}}
	if agent.bindingSum() != agent.profile().BindingSum() {
		t.Fatal("content with no host tool changed its sum: every live session would restart")
	}
	if host.bindingSum() == agent.bindingSum() {
		t.Error("a tool moved from the agent to the host kept its session")
	}
	if host.bindingSum() == moved.bindingSum() {
		t.Error("changed private paths kept the session (its confinement would be the old one)")
	}
	if host.bindingSum() != same.bindingSum() {
		t.Error("equal content got different sums")
	}
}
