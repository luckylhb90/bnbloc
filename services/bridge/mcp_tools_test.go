package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// mcpFixture is a member's machine: the desktop's tools dir with one
// materialized stdio binary (index.json), the member's component settings
// dir and their sign-ins dir.
type mcpFixture struct {
	tools, settings, signIns, binary, sha string
}

func newMCPFixture(t *testing.T) mcpFixture {
	t.Helper()
	f := mcpFixture{tools: t.TempDir(), settings: t.TempDir(), signIns: t.TempDir()}
	content := []byte("#!/bin/sh\necho codeword\n")
	sum := sha256.Sum256(content)
	f.sha = hex.EncodeToString(sum[:])
	f.binary = filepath.Join(f.tools, "mcp-codeword-"+f.sha)
	if err := os.WriteFile(f.binary, content, 0o755); err != nil {
		t.Fatal(err)
	}
	idx, _ := json.Marshal(mcpIndex{Tools: []mcpIndexEntry{{Name: "codeword", SHA256: f.sha, Path: f.binary}}})
	if err := os.WriteFile(filepath.Join(f.tools, mcpIndexFile), idx, 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f mcpFixture) setMember(t *testing.T, tool string, values map[string]string) {
	t.Helper()
	raw, _ := json.Marshal(values)
	if err := os.WriteFile(mcpMemberSettingsFile(f.settings, tool), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f mcpFixture) bridge(g *contentFixtureGateway, prov corepkg.Provider, transports ...string) *Bridge {
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "maya", MCPToolsDir: f.tools, ComponentSettingsDir: f.settings, MCPSignInsDir: f.signIns,
		Cores: []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: g.srv.URL}}}})
	b.rosterMu.Lock()
	if b.targetCache == nil {
		b.targetCache = map[string][]corepkg.Target{}
	}
	b.targetCache["core-a"] = []corepkg.Target{{ID: "agent-a", Provider: "agent-a", MCPTransports: transports}}
	b.rosterMu.Unlock()
	return b
}

func codewordTool(sha string) wire.ContentMCPTool {
	return wire.ContentMCPTool{
		Name: "codeword", Version: "1.0.0", Transport: wire.MCPTransportStdio,
		Artifacts: map[string]string{mcpPlatform(): sha}, Args: []string{"serve"},
		Settings: []wire.ComponentSetting{
			{Key: "salt", Label: "Salt", Type: pkgmeta.SettingSecret, Scope: pkgmeta.SettingScopeMember, Required: true, Deliver: &wire.ComponentSettingDeliver{Env: "CODEWORD_SALT"}},
			{Key: "lang", Label: "Language", Type: pkgmeta.SettingText, Scope: pkgmeta.SettingScopeMember, Deliver: &wire.ComponentSettingDeliver{Env: "CODEWORD_LANG"}},
			{Key: "words", Label: "Words", Type: pkgmeta.SettingLines, Scope: pkgmeta.SettingScopeMember},
		},
		RequiredBy: []string{"cc_reviewer"},
	}
}

func searchTool() wire.ContentMCPTool {
	return wire.ContentMCPTool{
		Name: "search", Version: "2.0.0", Transport: wire.MCPTransportHTTP, URL: "https://tools.example.com/mcp",
		Auth:     &wire.ComponentMCPHTTPAuth{Scheme: "bearer", Setting: "token"},
		Settings: []wire.ComponentSetting{{Key: "token", Label: "API token", Type: pkgmeta.SettingSecret, Scope: pkgmeta.SettingScopeOrg, Required: true}},
		Values:   map[string]string{"token": "org-token"}, RequiredBy: []string{"cc_reviewer"},
	}
}

func runningDiagnostics(g *contentFixtureGateway) map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, st := range g.statuses {
		if st["status"] == "running" {
			d, _ := st["diagnostics"].(map[string]any)
			return d
		}
	}
	return nil
}

func serverNamed(p *corepkg.AgentProfile, name string) *corepkg.MCPServer {
	for i := range p.MCPServers {
		if p.MCPServers[i].Name == name {
			return &p.MCPServers[i]
		}
	}
	return nil
}

// An own drive gets a stdio server from the desktop's verified binary with
// the member's own values as its env and settings file, and an http server
// with its organization value as the bearer.
func TestMCPToolsReachTheSession(t *testing.T) {
	g := newContentFixtureGateway(t)
	f := newMCPFixture(t)
	f.setMember(t, "codeword", map[string]string{"salt": "member-salt", "words": "alpha\nbeta", "other": "ignored"})
	expert := g.pkg(reviewerForm("Use the tools."), "1.0.0")
	expert.Source = wire.ContentSourceExpert
	g.setEffective("job-1", wire.ContentEffective{Expert: &expert, MCP: []wire.ContentMCPTool{codewordTool(f.sha), searchTool()}})
	prov := &profileProv{}
	f.bridge(g, prov, "stdio", "http").runBridgeJob(context.Background(), contentJob("job-1", expert.ComponentID))

	ps := prov.recordedProfiles()
	if len(ps) != 1 || ps[0] == nil {
		t.Fatalf("profiles = %+v", ps)
	}
	cw := serverNamed(ps[0], "codeword")
	if cw == nil || cw.Transport() != corepkg.MCPStdio || cw.Command != f.binary || strings.Join(cw.Args, " ") != "serve" ||
		len(cw.Env) != 2 || cw.Env[0] != (corepkg.NameValue{Name: "CODEWORD_SALT", Value: "member-salt"}) || cw.Env[1].Name != pkgmeta.ComponentSettingsFileEnv {
		t.Fatalf("codeword = %+v", cw)
	}
	// The settings file: every value (a lines value as a list), owner-only,
	// in the bridge's private temp dir, never its state dir (confinement
	// read-denies the desktop's data directory to a sandboxed tool).
	if !strings.HasPrefix(filepath.Base(filepath.Dir(cw.Env[1].Value)), "bnbloc-bridge-settings-") {
		t.Fatalf("settings file at %s", cw.Env[1].Value)
	}
	st, err := os.Stat(cw.Env[1].Value)
	raw, _ := os.ReadFile(cw.Env[1].Value)
	if err != nil || st.Mode().Perm() != 0o600 || string(raw) != "{\n  \"salt\": \"member-salt\",\n  \"words\": [\n    \"alpha\",\n    \"beta\"\n  ]\n}" {
		t.Fatalf("settings file = %v %v %q", st, err, raw)
	}
	s := serverNamed(ps[0], "search")
	if s == nil || s.Transport() != corepkg.MCPHTTP || s.URL != "https://tools.example.com/mcp" || len(s.Headers) != 1 || s.Headers[0].Value != "Bearer org-token" {
		t.Fatalf("search = %+v", s)
	}
	if d := runningDiagnostics(g); d["contentMissing"] != nil {
		t.Fatalf("diagnostics = %+v", d)
	}
	// The run record keeps which tools the session had, by name, from the
	// running report to the outcome (a room's capture reads them).
	g.mu.Lock()
	for _, st := range g.statuses {
		d, _ := st["diagnostics"].(map[string]any)
		tools, _ := d[wire.RunDiagTools].(map[string]any)
		if got := fmt.Sprint(tools["mcp"]); got != "[codeword search]" {
			t.Errorf("%v report's tools = %s", st["status"], got)
		}
	}
	g.mu.Unlock()
	// Secrets never reach the job's persisted status.
	g.mu.Lock()
	raw, _ = json.Marshal(g.statuses)
	g.mu.Unlock()
	if strings.Contains(string(raw), "member-salt") || strings.Contains(string(raw), "org-token") {
		t.Fatalf("a secret reached job data: %s", raw)
	}
}

// What cannot be attached shows as missing; the turn still runs.
func TestMCPToolsMissingDoNotFailTheTurn(t *testing.T) {
	g := newContentFixtureGateway(t)
	f := newMCPFixture(t)
	f.setMember(t, "codeword", map[string]string{"salt": "s"})
	expert := g.pkg(reviewerForm("Use the tools."), "1.0.0")
	expert.Source = wire.ContentSourceExpert
	stale := codewordTool(strings.Repeat("0", 64)) // the gateway pins a newer binary than the desktop holds
	stale.Name = "codeword"
	unset := codewordTool(f.sha)
	unset.Name = "salted"
	g.setEffective("job-1", wire.ContentEffective{Expert: &expert, MCP: []wire.ContentMCPTool{stale, unset, searchTool()}, Missing: []string{"MCP tool ghost"}})
	prov := &profileProv{}
	f.bridge(g, prov /* stdio only */).runBridgeJob(context.Background(), contentJob("job-1", expert.ComponentID))

	ps := prov.recordedProfiles()
	if len(ps) != 1 || ps[0] == nil || len(ps[0].MCPServers) != 0 {
		t.Fatalf("profiles = %+v", ps)
	}
	missing := strings.Join(toStrings(runningDiagnostics(g)["contentMissing"]), "; ")
	for _, want := range []string{"MCP tool ghost", "codeword (not downloaded on this computer yet)", "salted (needs setup: set Salt on the Components page)", "search (the agent accepts no http MCP server)"} {
		if !strings.Contains(missing, want) {
			t.Fatalf("missing %q in %q", want, missing)
		}
	}
}

// A tool the requester installed and no content requires (RequiredBy
// empty) attaches with no content at all; one that cannot be attached is
// left out without a missing line (docs/prd/2026-10-17-browser-component.md
// §3.6).
func TestMCPOfferedToolsAttachQuietly(t *testing.T) {
	g := newContentFixtureGateway(t)
	f := newMCPFixture(t)
	room := codewordTool(f.sha)
	room.Settings, room.Credentials, room.RequiredBy = nil, pkgmeta.MCPCredentialsRoom, nil
	notHere := codewordTool(strings.Repeat("0", 64))
	notHere.Name, notHere.RequiredBy = "salted", nil
	httpOnly := searchTool()
	httpOnly.RequiredBy = nil
	g.setEffective("job-1", wire.ContentEffective{MCP: []wire.ContentMCPTool{room, notHere, httpOnly}})
	prov := &profileProv{}
	f.bridge(g, prov /* stdio only */).runBridgeJob(context.Background(), contentJob("job-1", ""))

	ps := prov.recordedProfiles()
	if len(ps) != 1 || ps[0] == nil || len(ps[0].MCPServers) != 1 || serverNamed(ps[0], "codeword") == nil {
		t.Fatalf("profiles = %+v", ps)
	}
	if d := runningDiagnostics(g); d["contentMissing"] != nil {
		t.Fatalf("an offered tool showed as missing: %+v", d)
	}
}

// A core without agentProfile cannot attach MCP tools: they are missing.
func TestMCPToolsMissingOnACoreWithoutProfile(t *testing.T) {
	g := newContentFixtureGateway(t)
	f := newMCPFixture(t)
	f.setMember(t, "codeword", map[string]string{"salt": "s"})
	expert := g.pkg(reviewerForm("Use the tools."), "1.0.0")
	expert.Source = wire.ContentSourceExpert
	g.setEffective("job-1", wire.ContentEffective{Expert: &expert, MCP: []wire.ContentMCPTool{codewordTool(f.sha)}})
	prov := &bytesProfileProv{profileProv: profileProv{noProfile: true}}
	f.bridge(g, prov).runBridgeJob(context.Background(), contentJob("job-1", expert.ComponentID))
	missing := strings.Join(toStrings(runningDiagnostics(g)["contentMissing"]), "; ")
	if !strings.Contains(missing, "codeword (the agent's core cannot attach MCP tools)") {
		t.Fatalf("missing = %q", missing)
	}
}

// A borrowed drive never lends the owner's member settings; organization
// values still apply.
func TestMCPMemberSettingsAreNotLent(t *testing.T) {
	f := newMCPFixture(t)
	f.setMember(t, "codeword", map[string]string{"salt": "owner-salt"})
	b := &Bridge{cfg: Config{MCPToolsDir: f.tools, ComponentSettingsDir: f.settings}}
	refs := []wire.ContentRef{{ComponentID: "cc_reviewer", Owner: "maya", Name: "reviewer"}}
	mcp, missing, foreign := b.resolveMCP([]wire.ContentMCPTool{codewordTool(f.sha), searchTool()}, refs, "maya", "home", false)
	if len(mcp.servers) != 1 || mcp.servers[0].Name != "search" || len(foreign) != 0 {
		t.Fatalf("servers = %+v foreign = %v", mcp.servers, foreign)
	}
	if len(missing) != 1 || !strings.Contains(missing[0], "not lent") {
		t.Fatalf("missing = %v", missing)
	}
	// A tool whose member settings are optional runs for anyone, without them.
	open := codewordTool(f.sha)
	open.Settings[0].Required = false
	if mcp, missing, _ := b.resolveMCP([]wire.ContentMCPTool{open}, refs, "maya", "home", false); len(mcp.servers) != 1 || len(missing) != 0 || len(mcp.servers[0].Env) != 1 {
		t.Fatalf("servers = %+v missing = %v", mcp.servers, missing)
	}
}

// A room-credential tool needs nothing from anyone: it attaches on any
// drive (its relay acts for whoever the turn is for) and is named for the
// relay. Platform content requiring it (a built-in, no owner) is no one
// else's: it does not force the sandbox; another member's content does.
func TestMCPRoomCredentialTool(t *testing.T) {
	f := newMCPFixture(t)
	b := &Bridge{cfg: Config{MCPToolsDir: f.tools, ComponentSettingsDir: f.settings}}
	tool := codewordTool(f.sha)
	tool.Settings, tool.Credentials, tool.RequiredBy = nil, pkgmeta.MCPCredentialsRoom, []string{"cc_builtin_media-skills"}
	builtin := []wire.ContentRef{{ComponentID: "cc_builtin_media-skills", Name: "media-skills"}}
	mcp, missing, foreign := b.resolveMCP([]wire.ContentMCPTool{tool}, builtin, "maya", "home", false)
	if len(mcp.servers) != 1 || len(missing) != 0 || len(foreign) != 0 || len(mcp.relayed) != 1 || mcp.relayed[0] != "codeword" {
		t.Fatalf("servers = %+v relayed = %v missing = %v foreign = %v", mcp.servers, mcp.relayed, missing, foreign)
	}
	if len(mcp.servers[0].Env) != 0 {
		t.Fatalf("env = %+v; the relay's is set when the session is created", mcp.servers[0].Env)
	}
	tool.RequiredBy = []string{"cc_bob"}
	theirs := []wire.ContentRef{{ComponentID: "cc_bob", Owner: "bob", Name: "bobs-pack"}}
	if _, _, foreign := b.resolveMCP([]wire.ContentMCPTool{tool}, theirs, "maya", "home", false); len(foreign) != 1 {
		t.Fatalf("foreign = %v; another member's content runs a stdio tool sandboxed", foreign)
	}
}

// A binary outside the tools dir, or an index entry for another pin, is
// never used.
func TestMCPBinaryMustBeTheVerifiedOne(t *testing.T) {
	f := newMCPFixture(t)
	b := &Bridge{cfg: Config{MCPToolsDir: f.tools}}
	tool := codewordTool(f.sha)
	outside := &mcpIndex{Tools: []mcpIndexEntry{{Name: "codeword", SHA256: f.sha, Path: "/usr/bin/env"}}}
	if _, why := b.mcpBinary(outside, tool); !strings.Contains(why, "outside") {
		t.Fatalf("why = %q", why)
	}
	other := &mcpIndex{Tools: []mcpIndexEntry{{Name: "codeword", SHA256: strings.Repeat("1", 64), Path: f.binary}}}
	if _, why := b.mcpBinary(other, tool); why == "" {
		t.Fatal("an entry for another pin was used")
	}
	tool.Artifacts = map[string]string{"plan9-mips": f.sha}
	if _, why := b.mcpBinary(b.loadMCPIndex(), tool); !strings.Contains(why, "not published for") {
		t.Fatalf("why = %q", why)
	}
	if path, why := b.mcpBinary(b.loadMCPIndex(), codewordTool(f.sha)); why != "" || path != f.binary {
		t.Fatalf("path = %q why = %q", path, why)
	}
}

// Someone else's content requiring a stdio tool forces the sandbox even
// when it did not declare mcp.stdio: with none available the turn is
// refused before anything runs.
func TestForeignContentRequiringStdioMCPRequiresTheSandbox(t *testing.T) {
	g := newContentFixtureGateway(t)
	f := newMCPFixture(t)
	pack := g.pkg(lintPackForm(), "1.0.0")
	pack.Source, pack.Owner = wire.ContentSourceRoom, "kim"
	if pack.NeedsSandbox {
		t.Fatal("fixture pack declares a sandboxed permission")
	}
	tool := codewordTool(f.sha)
	tool.Settings, tool.Credentials, tool.RequiredBy = nil, "", []string{pack.ComponentID}
	g.setEffective("job-1", wire.ContentEffective{SkillPacks: []wire.ContentRef{pack}, MCP: []wire.ContentMCPTool{tool}})
	prov := &profileProv{}
	f.bridge(g, prov).runBridgeJob(context.Background(), contentJob("job-1", ""))
	if len(prov.recordedProfiles()) != 0 {
		t.Fatal("foreign content's stdio tool ran on the host")
	}
	if st := g.lastStatus(); st["status"] != "failed" || st["failureReason"] != "sandbox_required" {
		t.Fatalf("status = %+v", st)
	}
	if n := g.allNotices(); !strings.Contains(n, "This turn uses lint-kit from someone else") {
		t.Fatalf("notices = %q", n)
	}
}

// mcpSum is the binding checksum of a tool set alone.
func mcpSum(m jobMCP) string {
	return (&corepkg.AgentProfile{MCPServers: m.servers}).BindingSum()
}

// The session binding covers the tool set (adding a tool or newly
// configured credentials start a new session), never the values.
func TestMCPBindingSum(t *testing.T) {
	base := jobMCP{servers: []corepkg.MCPServer{{Name: "codeword", Command: "/t/mcp-codeword-aa", Env: []corepkg.NameValue{{Name: "SALT", Value: "one"}}}}}
	sameNames := jobMCP{servers: []corepkg.MCPServer{{Name: "codeword", Command: "/t/mcp-codeword-aa", Env: []corepkg.NameValue{{Name: "SALT", Value: "two"}}}}}
	if mcpSum(base) != mcpSum(sameNames) {
		t.Fatal("a value change must not churn the session")
	}
	upgraded := jobMCP{servers: []corepkg.MCPServer{{Name: "codeword", Command: "/t/mcp-codeword-bb", Env: base.servers[0].Env}}}
	unset := jobMCP{servers: []corepkg.MCPServer{{Name: "codeword", Command: "/t/mcp-codeword-aa"}}}
	for _, other := range []jobMCP{upgraded, unset, {}} {
		if mcpSum(other) == mcpSum(base) {
			t.Fatalf("%+v binds like %+v", other, base)
		}
	}
	if strings.Contains(mcpSum(base), "one") {
		t.Fatal("bindingSum")
	}
}

func toStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

func driveTool() wire.ContentMCPTool {
	return wire.ContentMCPTool{
		Name: "drive", Version: "1.0.0", Transport: wire.MCPTransportHTTP, URL: "https://tools.example.com/mcp",
		Auth: &wire.ComponentMCPHTTPAuth{Scheme: "oauth"}, RequiredBy: []string{"cc_reviewer"},
	}
}

func (f mcpFixture) setSignIn(t *testing.T, tool, token string, expiresAtMs int64) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"accessToken": token, "expiresAtMs": expiresAtMs, "refreshToken": "never-read"})
	if err := os.WriteFile(filepath.Join(f.signIns, tool+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// An oauth tool carries the member's own access token as a bearer header,
// only on their own drives; a missing or expired sign-in shows as missing
// and the turn still runs. The token never reaches the session checksum.
func TestMCPOAuthSignIn(t *testing.T) {
	f := newMCPFixture(t)
	b := &Bridge{cfg: Config{MCPToolsDir: f.tools, MCPSignInsDir: f.signIns}}
	refs := []wire.ContentRef{{ComponentID: "cc_reviewer", Owner: "maya", Name: "reviewer"}}

	_, missing, _ := b.resolveMCP([]wire.ContentMCPTool{driveTool()}, refs, "maya", "home", true)
	if len(missing) != 1 || !strings.Contains(missing[0], "sign in to drive on the Components page") {
		t.Fatalf("not signed in: missing = %v", missing)
	}
	f.setSignIn(t, "drive", "old-token", time.Now().Add(10*time.Second).UnixMilli())
	if _, missing, _ := b.resolveMCP([]wire.ContentMCPTool{driveTool()}, refs, "maya", "home", true); len(missing) != 1 || !strings.Contains(missing[0], "expired") {
		t.Fatalf("expiring: missing = %v", missing)
	}
	f.setSignIn(t, "drive", "member-token", time.Now().Add(time.Hour).UnixMilli())
	mcp, missing, _ := b.resolveMCP([]wire.ContentMCPTool{driveTool()}, refs, "maya", "home", true)
	if len(missing) != 0 || len(mcp.servers) != 1 {
		t.Fatalf("servers = %+v missing = %v", mcp.servers, missing)
	}
	h := mcp.servers[0].Headers
	if len(h) != 1 || h[0].Name != "Authorization" || h[0].Value != "Bearer member-token" {
		t.Fatalf("headers = %+v", h)
	}
	if strings.Contains(mcpSum(mcp), "member-token") {
		t.Fatal("token in the checksum")
	}
	f.setSignIn(t, "drive", "rotated-token", 0)
	again, _, _ := b.resolveMCP([]wire.ContentMCPTool{driveTool()}, refs, "maya", "home", true)
	if mcpSum(again) != mcpSum(mcp) {
		t.Fatal("a refreshed token must not churn the session")
	}
	// Never lent: a borrowed or platform drive does not get it.
	if mcp, missing, _ := b.resolveMCP([]wire.ContentMCPTool{driveTool()}, refs, "maya", "home", false); len(mcp.servers) != 0 || len(missing) != 1 || !strings.Contains(missing[0], "not lent") {
		t.Fatalf("lent: servers = %+v missing = %v", mcp.servers, missing)
	}
}

// An http tool's headers: the auth block's credential and each setting's
// own header, only for the values that are set.
func TestMCPHeaders(t *testing.T) {
	decl := []wire.ComponentSetting{
		{Key: "token", Type: pkgmeta.SettingSecret, Scope: pkgmeta.SettingScopeOrg},
		{Key: "workspace", Type: pkgmeta.SettingText, Scope: pkgmeta.SettingScopeMember, Deliver: &wire.ComponentSettingDeliver{Header: "X-Workspace"}},
		{Key: "region", Type: pkgmeta.SettingText, Scope: pkgmeta.SettingScopePlatform, Deliver: &wire.ComponentSettingDeliver{Header: "X-Region"}},
	}
	got := mcpHeaders(&wire.ComponentMCPHTTPAuth{Scheme: "bearer", Setting: "token"}, decl, map[string]string{"token": "t", "workspace": "w"}, "")
	if !slices.Equal(got, []corepkg.NameValue{{Name: "Authorization", Value: "Bearer t"}, {Name: "X-Workspace", Value: "w"}}) {
		t.Fatalf("headers = %v", got)
	}
	got = mcpHeaders(&wire.ComponentMCPHTTPAuth{Scheme: "header", Header: "X-Api-Key", Setting: "token"}, decl[:1], map[string]string{"token": "t"}, "")
	if !slices.Equal(got, []corepkg.NameValue{{Name: "X-Api-Key", Value: "t"}}) {
		t.Fatalf("header scheme = %v", got)
	}
	got = mcpHeaders(&wire.ComponentMCPHTTPAuth{Scheme: "oauth"}, decl[1:2], map[string]string{"workspace": "w"}, "member-token")
	if !slices.Equal(got, []corepkg.NameValue{{Name: "Authorization", Value: "Bearer member-token"}, {Name: "X-Workspace", Value: "w"}}) {
		t.Fatalf("oauth = %v", got)
	}
}
