package core

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"regexp"
	"sort"
	"strings"
)

// CapabilityAgentProfile marks a core that honors SessionRequest.Profile: it
// maps the profile's instructions, skills, MCP servers and env onto its agent
// natively (a system-prompt append, an instructions file, the agent's skills
// directory, its MCP config). A core without it never receives a profile
// (Supports gates it); the bridge falls back to prefixing the instructions to
// the prompt and summarizing the skills into them.
const CapabilityAgentProfile Capability = "agentProfile"

// AgentProfile is the role an agent session wears: the always-on
// instructions of an expert, the skills it loads on demand, the MCP servers
// it may call and the env those need. It is carried on session create only;
// a different profile is a different session.
//
// Env values and MCP header/env values are secrets: never logged (LogValue
// redacts them) and never persisted by bnbloc or a core.
type AgentProfile struct {
	// Instructions are appended to the agent's own system instructions (the
	// member's own agent configuration still loads alongside).
	Instructions string
	// Skills are delivered as files the agent loads from their
	// descriptions (SKILL.md frontmatter).
	Skills []ProfileSkill
	// MCPServers are attached to the session in the ACP McpServer shape.
	MCPServers []MCPServer
	// Env is extra process environment for the agent (secret values).
	Env map[string]string
}

// ProfileSkill is one skill: a directory named Name holding Files
// (SKILL.md plus optional scripts/ and references/).
type ProfileSkill struct {
	Name  string
	Files []ProfileFile
}

// ProfileFile is one file of a skill, Path relative to the skill directory
// (slash-separated, a valid bundle path).
type ProfileFile struct {
	Path    string
	Content []byte
}

// MCP server transports (the ACP McpServer union).
const (
	MCPStdio = "stdio"
	MCPHTTP  = "http"
	MCPSSE   = "sse"
)

// MCPServer is one MCP server in the ACP McpServer shape: a stdio server
// (Command, Args, Env) or an http/sse server (URL, Headers).
type MCPServer struct {
	Name    string
	Type    string // MCPStdio (default when empty), MCPHTTP or MCPSSE
	Command string
	Args    []string
	Env     []NameValue
	URL     string
	Headers []NameValue
}

// NameValue is one env variable or HTTP header (the value may be a secret).
type NameValue struct {
	Name  string
	Value string
}

// Transport is the server's transport, MCPStdio when unset.
func (m MCPServer) Transport() string {
	if m.Type == "" {
		return MCPStdio
	}
	return m.Type
}

// Empty reports whether the profile carries nothing.
func (p *AgentProfile) Empty() bool {
	return p == nil || (strings.TrimSpace(p.Instructions) == "" && len(p.Skills) == 0 && len(p.MCPServers) == 0 && len(p.Env) == 0)
}

// LogValue keeps secrets (env values, MCP header and env values) out of
// every log line: only names and sizes are logged.
func (p *AgentProfile) LogValue() slog.Value {
	if p == nil {
		return slog.StringValue("none")
	}
	skills := make([]string, 0, len(p.Skills))
	for _, s := range p.Skills {
		skills = append(skills, s.Name)
	}
	servers := make([]string, 0, len(p.MCPServers))
	for _, m := range p.MCPServers {
		servers = append(servers, m.Name+"("+m.Transport()+")")
	}
	envNames := make([]string, 0, len(p.Env))
	for k := range p.Env {
		envNames = append(envNames, k)
	}
	sort.Strings(envNames)
	return slog.GroupValue(
		slog.Int("instructionsBytes", len(p.Instructions)),
		slog.Any("skills", skills),
		slog.Any("mcpServers", servers),
		slog.Any("envNames", envNames),
	)
}

// BindingSum is the checksum that binds an agent session to the profile it
// was created with: a session whose profile's BindingSum differs is
// re-created (and its files re-materialized). It covers the instructions,
// every skill file, and each MCP server's non-secret identity — name,
// transport, command, URL, args, and the NAMES of its env and headers (a
// tool added, upgraded or newly configured binds a new session). Env and
// header values, and the profile's env, never enter it: they are secrets,
// and a per-session credential (a room tools bearer) must not force a new
// session. The bridge computes it on the content's profile, before session
// contributors add their per-session servers.
func (p *AgentProfile) BindingSum() string {
	if p == nil {
		return ""
	}
	h := sha256.New()
	h.Write([]byte("instructions\x00"))
	h.Write([]byte(p.Instructions))
	for _, s := range p.Skills {
		h.Write([]byte("\x00skill\x00" + s.Name))
		files := append([]ProfileFile(nil), s.Files...)
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		for _, f := range files {
			sum := sha256.Sum256(f.Content)
			h.Write([]byte("\x00" + f.Path + "\x00" + hex.EncodeToString(sum[:])))
		}
	}
	ids := make([]string, 0, len(p.MCPServers))
	for _, s := range p.MCPServers {
		var names []string
		for _, e := range s.Env {
			names = append(names, "env:"+e.Name)
		}
		for _, hd := range s.Headers {
			names = append(names, "header:"+hd.Name)
		}
		ids = append(ids, strings.Join([]string{s.Name, s.Transport(), s.Command, s.URL, strings.Join(s.Args, "\x00"), strings.Join(names, ",")}, "\x00"))
	}
	sort.Strings(ids)
	for _, id := range ids {
		h.Write([]byte("\x00mcp\x00" + id))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// maxBundlePath bounds one bundle path.
const maxBundlePath = 200

var bundlePathSegmentRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidBundlePath reports whether p is a safe bundle-relative file path:
// non-empty "/"-separated segments of [A-Za-z0-9._-], none "." or "..", no
// leading slash, at most 200 characters. It is the one path rule for package
// entries, archive members, served request paths and profile skill files.
func ValidBundlePath(p string) bool {
	if p == "" || len(p) > maxBundlePath {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." || !bundlePathSegmentRE.MatchString(seg) {
			return false
		}
	}
	return true
}
