package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// MCP tool components (docs/prd/2026-10-12-experts-and-teams.md §6): the
// gateway resolves the MCP tools of a job, the ones its content requires
// and the ones its requester installed (the requester may use them, each
// with its platform and organization setting values;
// docs/prd/2026-10-17-browser-component.md §3.6). The bridge turns each
// into an MCP server of the session's profile:
//
//   - stdio: the binary the member's desktop materialized and verified
//     (MCPToolsDir/index.json must carry the gateway's sha256 pin for this
//     platform), with the tool's args; its settings' values become its
//     process environment (deliver.env) and its settings file
//     (pkgmeta.ComponentSettingsFileEnv).
//   - http: the tool's URL with its headers (pkgmeta.MCPHeaders). An oauth
//     tool's bearer is the member's own access token, from the sign-in
//     their desktop ran (MCPSignInsDir/<tool>.json, 0600, refreshed by the
//     desktop).
//
// Member settings (docs/prd/2026-10-19-component-settings.md) are read from
// the member's own desktop store (ComponentSettingsDir/mcp@<tool>.json,
// written 0600 by the Components page) and only for drives the member
// asked for themselves: a borrowed or platform drive never lends the agent
// owner's values or accounts. A tool whose required settings are not all
// set needs setup and is not attached. What cannot be attached is reported
// as missing on the job, never a failed turn. Values are secrets: never
// logged, never in the session checksum, never in job data.

// mcpIndexFile is the desktop's index of materialized stdio tools.
const mcpIndexFile = "index.json"

// mcpIndex is MCPToolsDir/index.json, written by the desktop.
type mcpIndex struct {
	Tools []mcpIndexEntry `json:"tools"`
}

type mcpIndexEntry struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Path   string `json:"path"`
}

// mcpPlatform is this machine's artifact platform key ("darwin-arm64"),
// the key sidecar and MCP binaries are published under.
func mcpPlatform() string { return runtime.GOOS + "-" + runtime.GOARCH }

// jobMCP is a turn's resolved MCP servers.
type jobMCP struct {
	servers []corepkg.MCPServer
	// relayed names the servers whose credentials are the room
	// (pkgmeta.MCPCredentialsRoom): each gets the session's room relay.
	relayed []string
	// offered names the servers no content requires (the requester
	// installed them): one the session cannot attach is dropped quietly.
	offered []string
}

// resolveMCP builds the MCP servers of a job's tools. refs are the packages
// in effect (to tell whose content requires a tool); own reports a drive
// the requester asked of their own agent; tenantID is the job's
// organization (whose values the tools carry). It returns the servers, the unmet
// tools, and the packages that are not the agent owner's own and require a
// stdio tool (they force the sandboxed drive). Platform content (a
// built-in, Owner "") is no one else's: the platform signs both it and the
// tool.
func (b *Bridge) resolveMCP(tools []wire.ContentMCPTool, refs []wire.ContentRef, owner, tenantID string, own bool) (jobMCP, []string, []string) {
	var out jobMCP
	var missing, foreign []string
	byID := map[string]wire.ContentRef{}
	for _, r := range refs {
		byID[r.ComponentID] = r
	}
	foreignSeen := map[string]bool{}
	var index *mcpIndex
	for _, t := range tools {
		// A tool nobody's content requires was offered because the
		// requester installed it: not attaching it is no unmet requirement.
		unmet := func(why string) {
			if len(t.RequiredBy) > 0 {
				missing = append(missing, "MCP tool "+t.Name+" ("+why+")")
			} else {
				slog.Info("bnbloc bridge installed MCP tool not attached",
					"event", "bnbloc.bridge.mcp_offered_skipped", "tool", t.Name, "why", why)
			}
		}
		if pkgmeta.ValidMCPToolName(t.Name) != nil {
			unmet("invalid name")
			continue
		}
		values, why := b.mcpSettings(t, own)
		var signIn string
		if why == "" && t.Auth != nil && t.Auth.Scheme == pkgmeta.MCPAuthOAuth {
			signIn, why = b.mcpOAuthToken(t, own)
		}
		if why != "" {
			unmet(why)
			continue
		}
		server := corepkg.MCPServer{Name: t.Name}
		switch t.Transport {
		case wire.MCPTransportStdio:
			if index == nil {
				index = b.loadMCPIndex()
			}
			path, why := b.mcpBinary(index, t)
			if why != "" {
				unmet(why)
				continue
			}
			server.Type = corepkg.MCPStdio
			server.Command, server.Args = b.toolCommand(t.Name, path, t.Args)
			for _, nv := range pkgmeta.SettingsEnv(t.Settings, values) {
				server.Env = append(server.Env, corepkg.NameValue{Name: nv.Name, Value: nv.Value})
			}
			if len(t.Settings) > 0 {
				file, err := b.writeSettingsFile(t.Name, tenantID, own, pkgmeta.SettingsFile(t.Settings, values))
				if err != nil {
					slog.Warn("bnbloc bridge MCP settings file not written",
						"event", "bnbloc.bridge.mcp_settings_file_failed", "tool", t.Name, "error", err)
					unmet("its settings file could not be written")
					continue
				}
				server.Env = append(server.Env, corepkg.NameValue{Name: pkgmeta.ComponentSettingsFileEnv, Value: file})
			}
			if t.Credentials == pkgmeta.MCPCredentialsRoom {
				// The session's room relay is its credential, granted when
				// the session is created (roomtools contributor).
				out.relayed = append(out.relayed, t.Name)
			}
			// A stdio server runs on this machine: someone else's content
			// asking for one forces the sandboxed drive, whether or not it
			// declared mcp.stdio.
			for _, id := range t.RequiredBy {
				r, ok := byID[id]
				if ok && r.Owner != "" && r.Owner != owner && !foreignSeen[id] {
					foreignSeen[id] = true
					foreign = append(foreign, firstNonEmpty(r.DisplayName, r.Name))
				}
			}
		case wire.MCPTransportHTTP:
			if t.URL == "" {
				unmet("no URL")
				continue
			}
			server.Type = corepkg.MCPHTTP
			server.URL = t.URL
			server.Headers = mcpHeaders(t.Auth, t.Settings, values, signIn)
		default:
			unmet("unknown transport " + t.Transport)
			continue
		}
		if len(t.RequiredBy) == 0 {
			out.offered = append(out.offered, t.Name)
		}
		out.servers = append(out.servers, server)
	}
	return out, missing, foreign
}

// mcpSettings returns a tool's effective setting values
// (pkgmeta.EffectiveSettings over its platform and organization values and,
// on the member's own drive, their member values), or why it needs setup.
func (b *Bridge) mcpSettings(t wire.ContentMCPTool, own bool) (map[string]string, string) {
	if len(t.Settings) == 0 {
		return nil, ""
	}
	values := map[string]string{}
	for k, v := range t.Values {
		values[k] = v
	}
	member := pkgmeta.SettingsOfScope(t.Settings, pkgmeta.SettingScopeMember)
	if own && len(member) > 0 {
		stored := b.mcpMemberSettings(t.Name)
		for _, s := range member {
			values[s.Key] = stored[s.Key]
		}
	}
	unset := pkgmeta.UnsetSettings(t.Settings, values)
	if len(unset) == 0 {
		return pkgmeta.EffectiveSettings(t.Settings, values), ""
	}
	var labels []string
	memberUnset := false
	for _, s := range t.Settings {
		if slices.Contains(unset, s.Key) {
			labels = append(labels, s.Label)
			memberUnset = memberUnset || s.Scope == pkgmeta.SettingScopeMember
		}
	}
	switch {
	case memberUnset && !own:
		return nil, "member settings are not lent to someone else's drive"
	case memberUnset:
		return nil, "needs setup: set " + strings.Join(labels, ", ") + " on the Components page"
	}
	return nil, "needs setup: an admin has not set " + strings.Join(labels, ", ")
}

// mcpHeaders is the request headers an http MCP tool gets from its
// effective values: the auth block's credential (Authorization: Bearer
// <value>, or <header>: <value>; an oauth tool's bearer is the member's
// sign-in) and each setting's deliver.header or deliver.bearer, for every
// setting with a value (pkgmeta validated that no two name one header).
func mcpHeaders(auth *wire.ComponentMCPHTTPAuth, decl []wire.ComponentSetting, values map[string]string, signIn string) []corepkg.NameValue {
	var out []corepkg.NameValue
	bearer := func(v string) corepkg.NameValue {
		return corepkg.NameValue{Name: "Authorization", Value: "Bearer " + v}
	}
	if auth != nil {
		switch v := values[auth.Setting]; {
		case auth.Scheme == pkgmeta.MCPAuthOAuth && signIn != "":
			out = append(out, bearer(signIn))
		case auth.Scheme == pkgmeta.MCPAuthBearer && v != "":
			out = append(out, bearer(v))
		case auth.Scheme == pkgmeta.MCPAuthHeader && v != "":
			out = append(out, corepkg.NameValue{Name: auth.Header, Value: v})
		}
	}
	for _, s := range decl {
		v := values[s.Key]
		if s.Deliver == nil || v == "" {
			continue
		}
		switch {
		case s.Deliver.Bearer:
			out = append(out, bearer(v))
		case s.Deliver.Header != "":
			out = append(out, corepkg.NameValue{Name: s.Deliver.Header, Value: v})
		}
	}
	return out
}

// mcpMemberSettingsFile is the desktop's store of a tool's member values in
// ComponentSettingsDir: "<kind>@<name>.json", the desktop's file stem of a
// component (component_settings.rs).
func mcpMemberSettingsFile(dir, name string) string {
	return filepath.Join(dir, "mcp@"+name+".json")
}

// settingsFilesDir holds the settings files the bridge writes for the stdio
// tools it starts: a private (0700) directory of this process under the
// system temp dir. Never under StateDir: that lies in the desktop's data
// directory, which confinement read-denies to the agent CLI and so to the
// tools it starts, and a sandboxed tool could not read its own settings.
// Nothing is exposed by this: the CLI already holds a stdio tool's
// environment, which carries the same values.
func (b *Bridge) settingsFilesDir() (string, error) {
	b.settingsTmp.Do(func() {
		b.settingsTmpDir, b.settingsTmpErr = os.MkdirTemp("", "bnbloc-bridge-settings-")
	})
	return b.settingsTmpDir, b.settingsTmpErr
}

// writeSettingsFile writes a stdio tool's settings file (every effective
// value, secrets included, as pkgmeta.SettingsFile shapes it; owner-only)
// and returns its path. A tool's file
// is one per organization and per drive kind (the member's own values, or
// none for a lent drive), rewritten whenever a session is set up, so a
// running server reads the current values at its next start.
func (b *Bridge) writeSettingsFile(name, tenantID string, own bool, values map[string]any) (string, error) {
	dir, err := b.settingsFilesDir()
	if err != nil {
		return "", err
	}
	variant := "lent"
	if own {
		variant = "own"
	}
	sum := sha256.Sum256([]byte(variant + ":" + tenantID))
	path := filepath.Join(dir, name+"-"+hex.EncodeToString(sum[:6])+".json")
	return path, writeFileAtomic(path, values)
}

// mcpOAuthExpirySlack treats a token about to expire as expired: a session
// created now must not start with a token that dies in its first request.
const mcpOAuthExpirySlack = 30 * time.Second

// mcpOAuthFile is the part of the desktop's sign-in record the bridge reads.
type mcpOAuthFile struct {
	AccessToken string `json:"accessToken"`
	ExpiresAtMs int64  `json:"expiresAtMs"`
}

// mcpOAuthToken returns the member's access token of an oauth tool, or why
// there is none. Like member settings it is used only for drives the
// member asked for themselves.
func (b *Bridge) mcpOAuthToken(t wire.ContentMCPTool, own bool) (string, string) {
	if !own {
		return "", "member sign-ins are not lent to someone else's drive"
	}
	signIn := "sign in to " + t.Name + " on the Components page"
	dir := strings.TrimSpace(b.cfg.MCPSignInsDir)
	if dir == "" {
		return "", signIn
	}
	raw, err := os.ReadFile(filepath.Join(dir, t.Name+".json"))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("bnbloc bridge MCP sign-in unreadable",
				"event", "bnbloc.bridge.mcp_oauth_unreadable", "tool", t.Name, "error", err)
		}
		return "", signIn
	}
	var f mcpOAuthFile
	if err := json.Unmarshal(raw, &f); err != nil || strings.TrimSpace(f.AccessToken) == "" {
		slog.Warn("bnbloc bridge MCP sign-in invalid",
			"event", "bnbloc.bridge.mcp_oauth_invalid", "tool", t.Name)
		return "", signIn
	}
	if f.ExpiresAtMs > 0 && time.Now().Add(mcpOAuthExpirySlack).UnixMilli() >= f.ExpiresAtMs {
		return "", "its sign-in expired: sign in to " + t.Name + " again on the Components page"
	}
	return strings.TrimSpace(f.AccessToken), ""
}

// mcpMemberSettings reads the member's own values of a tool (none when the
// desktop handed no settings dir or nothing is stored).
func (b *Bridge) mcpMemberSettings(name string) map[string]string {
	dir := strings.TrimSpace(b.cfg.ComponentSettingsDir)
	if dir == "" {
		return nil
	}
	raw, err := os.ReadFile(mcpMemberSettingsFile(dir, name))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("bnbloc bridge MCP settings unreadable",
				"event", "bnbloc.bridge.mcp_settings_unreadable", "tool", name, "error", err)
		}
		return nil
	}
	values := map[string]string{}
	if err := json.Unmarshal(raw, &values); err != nil {
		slog.Warn("bnbloc bridge MCP settings invalid",
			"event", "bnbloc.bridge.mcp_settings_invalid", "tool", name)
		return nil
	}
	return values
}

// loadMCPIndex reads the desktop's index of materialized stdio tools (an
// empty index when there is none).
func (b *Bridge) loadMCPIndex() *mcpIndex {
	idx := &mcpIndex{}
	dir := strings.TrimSpace(b.cfg.MCPToolsDir)
	if dir == "" {
		return idx
	}
	raw, err := os.ReadFile(filepath.Join(dir, mcpIndexFile))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("bnbloc bridge MCP tools index unreadable",
				"event", "bnbloc.bridge.mcp_index_unreadable", "error", err)
		}
		return idx
	}
	if err := json.Unmarshal(raw, idx); err != nil {
		slog.Warn("bnbloc bridge MCP tools index invalid",
			"event", "bnbloc.bridge.mcp_index_invalid", "error", err)
		return &mcpIndex{}
	}
	return idx
}

// mcpBinary finds a stdio tool's materialized binary: the index entry of
// its name carrying the gateway's pin for this platform, inside the tools
// dir.
func (b *Bridge) mcpBinary(idx *mcpIndex, t wire.ContentMCPTool) (string, string) {
	pin := t.Artifacts[mcpPlatform()]
	if pin == "" {
		return "", "not published for " + mcpPlatform()
	}
	dir := strings.TrimSpace(b.cfg.MCPToolsDir)
	if dir == "" {
		return "", "stdio tools need the desktop app"
	}
	return materializedTool(dir, idx, t.Name, pin)
}

// fitMCPToTarget drops the MCP servers the session cannot attach: none on a
// core without agentProfile, http ones on a target that accepts only stdio.
// The required ones join the turn's missing requirements.
func (c *jobContent) fitMCPToTarget(profile bool, transports []string) {
	if c == nil || len(c.mcp.servers) == 0 {
		return
	}
	target := corepkg.Target{MCPTransports: transports}
	kept := c.mcp.servers[:0]
	for _, s := range c.mcp.servers {
		why := ""
		switch {
		case !profile:
			why = "the agent's core cannot attach MCP tools"
		case !target.TransportSupported(s.Transport()):
			why = "the agent accepts no " + s.Transport() + " MCP server"
		default:
			kept = append(kept, s)
			continue
		}
		if !slices.Contains(c.mcp.offered, s.Name) {
			c.missing = append(c.missing, "MCP tool "+s.Name+" ("+why+")")
		}
	}
	c.mcp.servers = kept
}

// withTools adds the turn's tools to a job-status diagnostics map
// (wire.RunDiagTools), the one writer of that key: the MCP tools attached
// to the turn's session, by name, once they are fitted to the target (the
// run record says which tools a room's runs had, never their settings), and
// ran, the room tool calls the run made (the outcome's report only:
// Bridge.roomCalls; tool, action and outcome, never arguments). The map is
// returned for chaining.
func (c *jobContent) withTools(diag map[string]any, ran wire.AgentRunTools) map[string]any {
	if c != nil {
		for _, s := range c.mcp.servers {
			ran.MCP = append(ran.MCP, s.Name)
		}
	}
	if len(ran.MCP) == 0 && len(ran.Calls) == 0 && ran.CallsDropped == 0 {
		return diag
	}
	diag[wire.RunDiagTools] = ran
	return diag
}

// roomToolCalls takes the room tool calls job made (none without room
// tools): once, for its outcome's report.
func (b *Bridge) roomToolCalls(jobID string) wire.AgentRunTools {
	if b.roomCalls == nil {
		return wire.AgentRunTools{}
	}
	return b.roomCalls(jobID)
}

// relayedMCP names the content's room-credential servers still attached.
func (c *jobContent) relayedMCP() []string {
	if c == nil {
		return nil
	}
	var out []string
	for _, s := range c.mcp.servers {
		if slices.Contains(c.mcp.relayed, s.Name) {
			out = append(out, s.Name)
		}
	}
	return out
}

// mcpAttach reports whether a core's sessions can carry a profile, and the
// MCP transports the target accepts.
func (b *Bridge) mcpAttach(ctx context.Context, coreName, target, provider string) (bool, []string) {
	entry, ok := b.coreEntry(coreName)
	if !ok || !entry.endpoint.OK() {
		return false, nil
	}
	return corepkg.Supports(ctx, entry.provider, entry.endpoint, corepkg.CapabilityAgentProfile), b.targetTransports(coreName, target, provider)
}
