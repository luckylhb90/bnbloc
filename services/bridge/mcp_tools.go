package bridge

import (
	"context"
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
// and the ones its requester installed (the requester may use them; an
// org-credential tool brings its organization connector's values;
// docs/prd/2026-10-17-browser-component.md §3.6). The bridge turns each
// into an MCP server of the session's profile:
//
//   - stdio: the binary the member's desktop materialized and verified
//     (MCPToolsDir/index.json must carry the gateway's sha256 pin for this
//     platform), with the tool's args; the env values become its process
//     environment.
//   - http: the tool's URL with its auth header. An oauth tool's header is
//     the member's own access token, from the sign-in their desktop ran
//     (MCPSettingsDir/oauth/<tool>.json, 0600, refreshed by the desktop).
//
// Member credentials are read from the member's own desktop settings
// (MCPSettingsDir/<tool>.json, written 0600 by Settings → Tools) and only
// for drives the member asked for themselves: a borrowed or platform drive
// never lends the agent owner's accounts. What cannot be attached is
// reported as missing on the job, never a failed turn. Values are secrets:
// never logged, never in the session checksum, never in job data.

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
// the requester asked of their own agent. It returns the servers, the unmet
// tools, and the packages that are not the agent owner's own and require a
// stdio tool (they force the sandboxed drive). Platform content (a
// built-in, Owner "") is no one else's: the platform signs both it and the
// tool.
func (b *Bridge) resolveMCP(tools []wire.ContentMCPTool, refs []wire.ContentRef, owner string, own bool) (jobMCP, []string, []string) {
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
		values, why := b.mcpValues(t, own)
		if why == "" && t.Auth != nil && t.Auth.Scheme == pkgmeta.MCPAuthOAuth {
			values, why = b.mcpOAuthToken(t, own)
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
			server.Command = path
			server.Args = append([]string(nil), t.Args...)
			for _, e := range t.Env {
				if v := values[e.Name]; v != "" {
					server.Env = append(server.Env, corepkg.NameValue{Name: e.Name, Value: v})
				}
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
			if t.Auth != nil {
				switch t.Auth.Scheme {
				case pkgmeta.MCPAuthBearer:
					server.Headers = []corepkg.NameValue{{Name: "Authorization", Value: "Bearer " + values[t.Auth.Env]}}
				case pkgmeta.MCPAuthHeader:
					server.Headers = []corepkg.NameValue{{Name: t.Auth.Header, Value: values[t.Auth.Env]}}
				case pkgmeta.MCPAuthOAuth:
					server.Headers = []corepkg.NameValue{{Name: "Authorization", Value: "Bearer " + values[mcpOAuthAccessKey]}}
				}
			}
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

// mcpValues returns a tool's env values, or why they are not available.
func (b *Bridge) mcpValues(t wire.ContentMCPTool, own bool) (map[string]string, string) {
	if len(t.Env) == 0 {
		return nil, ""
	}
	var values map[string]string
	switch t.Credentials {
	case pkgmeta.MCPCredentialsOrg:
		values = t.Values
	case pkgmeta.MCPCredentialsMember:
		if !own {
			return nil, "member credentials are not lent to someone else's drive"
		}
		values = b.mcpMemberSettings(t.Name)
	default:
		return nil, "unknown credentials source"
	}
	if unset := pkgmeta.UnsetEnv(t.RequiredEnv(), values); len(unset) > 0 {
		if t.Credentials == pkgmeta.MCPCredentialsMember {
			return nil, "set " + strings.Join(unset, ", ") + " in Settings → Tools"
		}
		return nil, "its organization connector is not configured"
	}
	return values, ""
}

// mcpOAuthDir is the subdirectory of MCPSettingsDir holding the member's
// OAuth sign-ins, one <tool>.json each (apart from the env settings files,
// so no tool name can collide with another tool's sign-in).
const mcpOAuthDir = "oauth"

// mcpOAuthAccessKey keys the access token in the values mcpOAuthToken
// returns (never an env name: env names are upper-case).
const mcpOAuthAccessKey = "access_token"

// mcpOAuthExpirySlack treats a token about to expire as expired: a session
// created now must not start with a token that dies in its first request.
const mcpOAuthExpirySlack = 30 * time.Second

// mcpOAuthFile is the part of the desktop's sign-in record the bridge reads.
type mcpOAuthFile struct {
	AccessToken string `json:"accessToken"`
	ExpiresAtMs int64  `json:"expiresAtMs"`
}

// mcpOAuthToken returns the member's access token of an oauth tool, or why
// there is none. Like member credentials it is used only for drives the
// member asked for themselves.
func (b *Bridge) mcpOAuthToken(t wire.ContentMCPTool, own bool) (map[string]string, string) {
	if !own {
		return nil, "member sign-ins are not lent to someone else's drive"
	}
	signIn := "sign in to " + t.Name + " in Settings → Tools"
	dir := strings.TrimSpace(b.cfg.MCPSettingsDir)
	if dir == "" {
		return nil, signIn
	}
	raw, err := os.ReadFile(filepath.Join(dir, mcpOAuthDir, t.Name+".json"))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("bnbloc bridge MCP sign-in unreadable",
				"event", "bnbloc.bridge.mcp_oauth_unreadable", "tool", t.Name, "error", err)
		}
		return nil, signIn
	}
	var f mcpOAuthFile
	if err := json.Unmarshal(raw, &f); err != nil || strings.TrimSpace(f.AccessToken) == "" {
		slog.Warn("bnbloc bridge MCP sign-in invalid",
			"event", "bnbloc.bridge.mcp_oauth_invalid", "tool", t.Name)
		return nil, signIn
	}
	if f.ExpiresAtMs > 0 && time.Now().Add(mcpOAuthExpirySlack).UnixMilli() >= f.ExpiresAtMs {
		return nil, "its sign-in expired: sign in to " + t.Name + " again in Settings → Tools"
	}
	return map[string]string{mcpOAuthAccessKey: strings.TrimSpace(f.AccessToken)}, ""
}

// mcpMemberSettings reads the member's own values of a tool (none when the
// desktop handed no settings dir or nothing is stored).
func (b *Bridge) mcpMemberSettings(name string) map[string]string {
	dir := strings.TrimSpace(b.cfg.MCPSettingsDir)
	if dir == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, name+".json"))
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
	for _, e := range idx.Tools {
		if e.Name != t.Name || e.SHA256 != pin {
			continue
		}
		rel, err := filepath.Rel(dir, e.Path)
		if err != nil || !filepath.IsAbs(e.Path) || rel == "." || strings.HasPrefix(rel, "..") {
			return "", "its binary is outside the tools directory"
		}
		if st, err := os.Stat(e.Path); err != nil || !st.Mode().IsRegular() {
			return "", "not downloaded on this computer yet"
		}
		return e.Path, ""
	}
	return "", "not downloaded on this computer yet"
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
