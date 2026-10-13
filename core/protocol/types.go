// Package protocol defines Core Protocol v1, the HTTP+JSON wire contract used
// by bnbloc to drive any conforming local-agent core daemon.
package protocol

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

const (
	PathDiscover        = "/core/v1/discover"
	PathTargets         = "/core/v1/targets"
	PathWorkspaces      = "/core/v1/workspaces"
	PathHealth          = "/core/v1/healthz"
	SupportedMinVersion = "1.0"
	SupportedMaxVersion = "1.0"
)

// Optional capability names advertised in Discovery.Capabilities. They are the
// core.Capability values, so a protocol daemon and a manifest name the same
// capability identically.
const (
	CapabilityInterrupt       = string(core.CapabilityInterrupt)
	CapabilityWriteFile       = string(core.CapabilityWriteFile)
	CapabilityDeleteWorkspace = string(core.CapabilityDeleteWorkspace)
	// CapabilitySandboxExec: the core honors the `execution` descriptor on
	// session create / input (CLI on the host, shell commands in a sandbox
	// space). See Execution.
	CapabilitySandboxExec = string(core.CapabilitySandboxExec)
	// CapabilitySessionFiles: GET .../sessions/{sid}/files lists the session
	// workspace's deliverable files (ListSessionFilesResponse).
	// CapabilitySessionFileBytes: GET /core/v1/workspaces/{ws}/files/{path}
	// returns one file's exact raw bytes. Reads have only this binary-safe
	// form on the wire, so a core advertises the two together (conformance
	// fails a core that advertises one without the other).
	CapabilitySessionFiles     = string(core.CapabilitySessionFiles)
	CapabilitySessionFileBytes = string(core.CapabilitySessionFileBytes)
	// CapabilityAgentProfile: the core honors the `profile` of a session
	// create (instructions, skills, MCP servers, env) and maps it onto its
	// agent natively. See AgentProfile.
	CapabilityAgentProfile = string(core.CapabilityAgentProfile)
	// CapabilityDiscover marks a package whose daemon can be located without an
	// explicit endpoint (package metadata only; not a runtime method).
	CapabilityDiscover = string(core.CapabilityDiscover)
)

// Capabilities lists every Core Protocol capability name: the one registry
// package validation and the runtime share (a Capability* constant missing
// here fails TestCoreCapabilitiesAcceptEveryProtocolCapability).
var Capabilities = []string{
	CapabilityInterrupt, CapabilityWriteFile, CapabilityDeleteWorkspace, CapabilitySandboxExec,
	CapabilitySessionFiles, CapabilitySessionFileBytes, CapabilityAgentProfile, CapabilityDiscover,
}

// KnownCapability reports whether name is a Core Protocol capability.
func KnownCapability(name string) bool {
	for _, c := range Capabilities {
		if c == name {
			return true
		}
	}
	return false
}

type ErrorCode string

const (
	ErrorBadRequest         ErrorCode = "bad_request"
	ErrorUnauthorized       ErrorCode = "unauthorized"
	ErrorForbidden          ErrorCode = "forbidden"
	ErrorTargetNotFound     ErrorCode = "target_not_found"
	ErrorWorkspaceNotFound  ErrorCode = "workspace_not_found"
	ErrorSessionNotFound    ErrorCode = "session_not_found"
	ErrorTurnNotFound       ErrorCode = "turn_not_found"
	ErrorCapabilityMissing  ErrorCode = "capability_missing"
	ErrorVersionUnsupported ErrorCode = "version_unsupported"
	ErrorConflict           ErrorCode = "conflict"
	ErrorRateLimited        ErrorCode = "rate_limited"
	ErrorTimeout            ErrorCode = "timeout"
	ErrorCoreUnavailable    ErrorCode = "core_unavailable"
	ErrorInternal           ErrorCode = "internal"
	// ErrorFileNotFound (404): a file read named no regular file inside the
	// workspace — missing, a directory, or resolving outside it.
	ErrorFileNotFound ErrorCode = "file_not_found"
	// ErrorFileTooLarge (413): the file exceeds the core's read cap.
	ErrorFileTooLarge ErrorCode = "file_too_large"
)

type ErrorEnvelope struct {
	Error ProtocolError `json:"error"`
}

// ProtocolError is the envelope's error. Retryable is the daemon's optional
// verdict on repeating the request: absent leaves it to the status code
// (core.Retryable), so a 429/503 envelope without it is retried.
type ProtocolError struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Retryable *bool     `json:"retryable,omitempty"`
}

// Is makes a daemon's capability_missing match core.ErrCapabilityMissing,
// like the client's own local refusal.
func (e *ProtocolError) Is(target error) bool {
	return target == core.ErrCapabilityMissing && e != nil && e.Code == ErrorCapabilityMissing
}

// RetryHint implements core.RetryHinter.
func (e *ProtocolError) RetryHint() (retry, stated bool) {
	if e == nil || e.Retryable == nil {
		return false, false
	}
	return *e.Retryable, true
}

func (e *ProtocolError) Error() string {
	if e == nil {
		return "core protocol: <nil>"
	}
	if e.Message == "" {
		return fmt.Sprintf("core protocol: %s", e.Code)
	}
	return fmt.Sprintf("core protocol: %s: %s", e.Code, e.Message)
}

type ProtocolRange struct {
	Min string `json:"min"`
	Max string `json:"max"`
}

type Limits struct {
	MaxInputBytes      int64 `json:"maxInputBytes,omitempty"`
	MaxConcurrentTurns int   `json:"maxConcurrentTurns,omitempty"`
}

type Discovery struct {
	Name         string        `json:"name"`
	Version      string        `json:"version"`
	Protocol     ProtocolRange `json:"protocol"`
	Capabilities []string      `json:"capabilities"`
	Limits       Limits        `json:"limits,omitempty"`
}

type CreateWorkspaceRequest struct {
	Name string `json:"name,omitempty"`
	// Workdir (sandboxExec cores only) is the absolute host directory the
	// workspace must live in — under the sandbox workspaces root, shared into
	// the guest at the identical path. The bridge sends it only for a session
	// it will drive sandboxed; sandboxed session create / input then carry
	// execution.sandbox.workdir equal to it (the core answers bad_request
	// otherwise, and when it cannot place the workspace there).
	Workdir string `json:"workdir,omitempty"`
}

type CreateWorkspaceResponse struct {
	WorkspaceID string `json:"workspaceId"`
}

// The request/response bodies below are the Core Protocol v1 wire contract.
// They are deliberately distinct from the in-process core.* types and carry
// explicit camelCase JSON tags: the wire must not change because a Go field
// in core was renamed. (Before these DTOs existed the core types were
// serialized untagged, i.e. PascalCase; encoding/json decodes keys
// case-insensitively, so Go peers built against that shape interoperate.)

// SessionOptions is the per-drive agent tuning on the wire.
type SessionOptions struct {
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

// CreateSessionRequest starts a session; the workspace comes from the path.
type CreateSessionRequest struct {
	TargetID string         `json:"targetId,omitempty"`
	Prompt   string         `json:"prompt"`
	Options  SessionOptions `json:"options,omitempty"`
	// Execution is sent only to a core advertising sandboxExec; absent = run
	// on the host as today.
	Execution *Execution `json:"execution,omitempty"`
	// Profile is sent only to a core advertising agentProfile; absent = the
	// agent's own configuration only.
	Profile *AgentProfile `json:"profile,omitempty"`
}

// AgentProfile is the role a session wears (capability agentProfile). The
// core maps it onto its agent natively and keeps the member's own agent
// configuration loading alongside:
//
//   - Instructions are appended to the agent's system instructions (an ACP
//     `_meta` system-prompt append, an env var, or an instructions file such
//     as AGENTS.md / CLAUDE.md in the workspace).
//   - Skills are written as <skills dir>/<name>/<path> (e.g.
//     .claude/skills/, .agents/skills/, .opencode/skills/) so the agent
//     loads them on demand from SKILL.md's description.
//   - McpServers are attached to the agent session (ACP `session/new`
//     mcpServers); an http/sse server goes only to an agent that supports
//     that transport.
//   - Env is extra process environment.
//
// Files the core writes live only under the workspace's agent-config paths
// (core.BnblocOwnedPaths). Env values and MCP header/env values are secrets:
// a core never logs or persists them. A core that cannot honor a profile
// answers bad_request rather than silently dropping it.
type AgentProfile struct {
	Instructions string            `json:"instructions,omitempty"`
	Skills       []ProfileSkill    `json:"skills,omitempty"`
	McpServers   []McpServer       `json:"mcpServers,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
}

// ProfileSkill is one skill directory: Name is its slug (a single valid
// bundle path segment), Files its files relative to that directory.
type ProfileSkill struct {
	Name  string        `json:"name"`
	Files []ProfileFile `json:"files"`
}

// ProfileFile is one skill file. Content carries UTF-8 text; ContentBase64
// carries any other bytes (when set, Content is ignored).
type ProfileFile struct {
	Path          string `json:"path"`
	Content       string `json:"content,omitempty"`
	ContentBase64 string `json:"contentBase64,omitempty"`
}

// McpServer is the ACP McpServer union: a stdio server (`type` omitted or
// "stdio": command, args, env) or an http / sse server (url, headers).
type McpServer struct {
	Type    string      `json:"type,omitempty"`
	Name    string      `json:"name"`
	Command string      `json:"command,omitempty"`
	Args    []string    `json:"args,omitempty"`
	Env     []NameValue `json:"env,omitempty"`
	URL     string      `json:"url,omitempty"`
	Headers []NameValue `json:"headers,omitempty"`
}

// NameValue is an env variable or HTTP header (ACP EnvVariable/HttpHeader).
type NameValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type CreateSessionResponse struct {
	SessionID string `json:"sessionId"`
	TurnID    string `json:"turnId,omitempty"`
	Phase     string `json:"phase,omitempty"`
}

type SendInputRequest struct {
	Text      string         `json:"text"`
	Options   SessionOptions `json:"options,omitempty"`
	Execution *Execution     `json:"execution,omitempty"`
}

// ExecutionModeSandbox is the only Execution.Mode in v1.
const ExecutionModeSandbox = "sandbox"

// Execution is the optional per-request execution descriptor (capability
// sandboxExec), carried on session create AND every follow-up input of a
// sandboxed session. The core keeps its agent CLI on the host — confined by
// the host OS sandbox to HostConfine (bnbloc-vmexec confine) — and routes the
// CLI's shell commands into the sandbox space through bnbloc-vmexec (VMExec);
// file tools, MCP servers and hooks stay on the host. A session created with
// an Execution runs its CLI in Sandbox.Workdir; the bridge never mixes
// sandboxed and host turns in one session. The core must refuse (bad_request)
// an execution it cannot honor rather than silently run on the host.
type Execution struct {
	Mode    string            `json:"mode"`
	Sandbox *SandboxExecution `json:"sandbox,omitempty"`
}

// SandboxExecution addresses the room's space in a Sandbox Protocol
// implementation. ExecToken is a short-lived space-scoped exec token (it can
// only run/exec in Space): a secret the core passes to vmexec through the
// environment, never logs and never persists; a fresh one arrives with every
// turn (ExpiresAtMs is its expiry).
type SandboxExecution struct {
	URL         string `json:"url"`
	ExecToken   string `json:"execToken"`
	ExpiresAtMs int64  `json:"expiresAtMs,omitempty"`
	// Machine is the sandbox machine id or name, Space the space name.
	Machine string `json:"machine"`
	Space   string `json:"space"`
	// VMExec is the absolute host path of the bnbloc-vmexec helper.
	VMExec string `json:"vmexec"`
	// Workdir is the host directory the core runs this session's CLI in. It
	// lies under the shared workspaces root and inside Mounts (mounted at the
	// identical guest path), so host and guest see the same files.
	Workdir string      `json:"workdir"`
	Mounts  []PathMount `json:"mounts,omitempty"`
	// Passthrough lists command names vmexec may run on the host instead of
	// the guest (empty for borrowed drives unless the owner allows).
	Passthrough []string `json:"passthrough,omitempty"`
	// Env lists environment variable names (or PREFIX* patterns) vmexec
	// forwards from the CLI's host environment into the guest beyond the
	// default (ForwardEnvAllowlist): what the owner allowed for their own
	// runs, empty on every run for someone else. Names only, never values.
	Env         []string     `json:"env,omitempty"`
	HostConfine *HostConfine `json:"hostConfine,omitempty"`
}

// PathMount maps one host directory to its guest path.
type PathMount struct {
	Host  string `json:"host"`
	Guest string `json:"guest"`
}

// HostConfine is the host OS-sandbox scope of the agent CLI: read+write
// Workspace, plus AllowRead / AllowWrite extras (the CLI's own install and
// state dirs are added by vmexec confine itself), and DenyWrite paths that
// stay read-only even inside an allowed tree (it wins over every allow; a
// core lists the routing config it places in the workdir here so the agent
// cannot edit it mid-turn). A DenyWrite path is protected whether or not it
// exists yet; an entry ending in "/" names a directory.
type HostConfine struct {
	Workspace  string   `json:"workspace"`
	AllowRead  []string `json:"allowRead,omitempty"`
	AllowWrite []string `json:"allowWrite,omitempty"`
	DenyWrite  []string `json:"denyWrite,omitempty"`
}

// WireExecution maps the neutral descriptor onto the wire (nil stays nil).
func WireExecution(e *core.Execution) *Execution {
	if e == nil {
		return nil
	}
	s := e.Sandbox
	mounts := make([]PathMount, 0, len(s.Mounts))
	for _, m := range s.Mounts {
		mounts = append(mounts, PathMount{Host: m.Host, Guest: m.Guest})
	}
	return &Execution{Mode: ExecutionModeSandbox, Sandbox: &SandboxExecution{
		URL: s.URL, ExecToken: s.ExecToken, ExpiresAtMs: s.ExpiresAtMs, Machine: s.Machine, Space: s.Space,
		VMExec: s.VMExec, Workdir: s.Workdir, Mounts: mounts, Passthrough: s.Passthrough, Env: s.Env,
		HostConfine: &HostConfine{Workspace: s.HostConfine.Workspace, AllowRead: s.HostConfine.AllowRead, AllowWrite: s.HostConfine.AllowWrite, DenyWrite: s.HostConfine.DenyWrite},
	}}
}

// Core maps a wire descriptor back onto the neutral one. An unknown mode or
// a sandbox mode without its sandbox block is an error (refuse, never run on
// the host silently).
func (e *Execution) Core() (*core.Execution, error) {
	if e == nil {
		return nil, nil
	}
	if e.Mode != ExecutionModeSandbox || e.Sandbox == nil {
		return nil, fmt.Errorf("execution mode %q unsupported (want %q with a sandbox block)", e.Mode, ExecutionModeSandbox)
	}
	s := e.Sandbox
	mounts := make([]core.PathMount, 0, len(s.Mounts))
	for _, m := range s.Mounts {
		mounts = append(mounts, core.PathMount{Host: m.Host, Guest: m.Guest})
	}
	out := &core.Execution{Sandbox: core.SandboxExecution{
		URL: s.URL, ExecToken: s.ExecToken, ExpiresAtMs: s.ExpiresAtMs, Machine: s.Machine, Space: s.Space,
		VMExec: s.VMExec, Workdir: s.Workdir, Mounts: mounts, Passthrough: s.Passthrough, Env: s.Env,
	}}
	if s.HostConfine != nil {
		out.Sandbox.HostConfine = core.HostConfine{Workspace: s.HostConfine.Workspace, AllowRead: s.HostConfine.AllowRead, AllowWrite: s.HostConfine.AllowWrite, DenyWrite: s.HostConfine.DenyWrite}
	}
	return out, nil
}

// Target is one selectable agent target on the wire (see core.Target).
type Target struct {
	ID               string              `json:"id"`
	Provider         string              `json:"provider"`
	Models           []string            `json:"models,omitempty"`
	ReasoningEfforts []string            `json:"reasoningEfforts,omitempty"`
	ReasoningByModel map[string][]string `json:"reasoningByModel,omitempty"`
	DefaultModel     string              `json:"defaultModel,omitempty"`
	DefaultReasoning string              `json:"defaultReasoning,omitempty"`
	// MCPTransports lists the MCP transports the target's agent accepts in
	// a session profile ("stdio", "http", "sse"); empty = stdio only. An
	// ACP adapter fills it from the agent's initialize mcpCapabilities.
	MCPTransports []string `json:"mcpTransports,omitempty"`
}

type ListTargetsResponse struct {
	Targets []Target `json:"targets"`
}

// Message is one session message on the wire (see core.Message). Version is
// the session's monotonic message cursor; Seq is the message's stable,
// strictly increasing sequence within the session.
type Message struct {
	Version     int64          `json:"version"`
	Role        string         `json:"role"`
	Text        string         `json:"text"`
	UserVisible bool           `json:"userVisible"`
	Completed   bool           `json:"completed"`
	MessageID   string         `json:"messageId,omitempty"`
	Seq         int64          `json:"seq,omitempty"`
	CreatedAtMs int64          `json:"createdAtMs,omitempty"`
	Thinking    string         `json:"thinking,omitempty"`
	Metrics     map[string]any `json:"metrics,omitempty"`
}

type MessagesResponse struct {
	Messages      []Message `json:"messages"`
	LatestVersion int64     `json:"latestVersion"`
}

type PollTurnResponse struct {
	Idle        bool   `json:"idle"`
	Failed      bool   `json:"failed,omitempty"`
	FailMessage string `json:"failMessage,omitempty"`
}

// --- wire <-> core mapping, used by both the client and protocol servers ---

func WireOptions(o core.SessionOptions) SessionOptions {
	return SessionOptions{Model: o.Model, ReasoningEffort: o.ReasoningEffort}
}

func (o SessionOptions) Core() core.SessionOptions {
	return core.SessionOptions{Model: o.Model, ReasoningEffort: o.ReasoningEffort}
}

func WireCreateSession(r core.SessionRequest) CreateSessionRequest {
	return CreateSessionRequest{TargetID: r.TargetID, Prompt: r.Prompt, Options: WireOptions(r.Options), Execution: WireExecution(r.Options.Execution), Profile: WireProfile(r.Profile)}
}

// Core maps the request onto core.SessionRequest for the workspace named in
// the request path. An execution descriptor or a profile the core cannot
// interpret is an error (answer bad_request).
func (r CreateSessionRequest) Core(workspaceID string) (core.SessionRequest, error) {
	opts := r.Options.Core()
	exec, err := r.Execution.Core()
	if err != nil {
		return core.SessionRequest{}, err
	}
	opts.Execution = exec
	profile, err := r.Profile.Core()
	if err != nil {
		return core.SessionRequest{}, err
	}
	return core.SessionRequest{WorkspaceID: workspaceID, TargetID: r.TargetID, Prompt: r.Prompt, Options: opts, Profile: profile}, nil
}

// WireProfile maps the neutral profile onto the wire (nil stays nil). A file
// whose bytes are not UTF-8 travels as contentBase64.
func WireProfile(p *core.AgentProfile) *AgentProfile {
	if p == nil {
		return nil
	}
	out := &AgentProfile{Instructions: p.Instructions}
	if len(p.Env) > 0 {
		out.Env = make(map[string]string, len(p.Env))
		for k, v := range p.Env {
			out.Env[k] = v
		}
	}
	for _, s := range p.Skills {
		ws := ProfileSkill{Name: s.Name, Files: make([]ProfileFile, 0, len(s.Files))}
		for _, f := range s.Files {
			wf := ProfileFile{Path: f.Path}
			if utf8.Valid(f.Content) {
				wf.Content = string(f.Content)
			} else {
				wf.ContentBase64 = base64.StdEncoding.EncodeToString(f.Content)
			}
			ws.Files = append(ws.Files, wf)
		}
		out.Skills = append(out.Skills, ws)
	}
	for _, m := range p.MCPServers {
		out.McpServers = append(out.McpServers, McpServer{
			Type: wireMcpType(m.Type), Name: m.Name, Command: m.Command, Args: m.Args,
			Env: wireNameValues(m.Env), URL: m.URL, Headers: wireNameValues(m.Headers),
		})
	}
	return out
}

// MaxProfileSkills / MaxProfileMcpServers bound a profile on the wire.
const (
	MaxProfileSkills     = 64
	MaxProfileMcpServers = 32
)

// Core maps a wire profile onto the neutral one, validating it: skill names
// are single bundle path segments, file paths valid bundle paths (no "..",
// no absolute paths), MCP servers named and well-formed for their type.
func (p *AgentProfile) Core() (*core.AgentProfile, error) {
	if p == nil {
		return nil, nil
	}
	if len(p.Skills) > MaxProfileSkills {
		return nil, fmt.Errorf("profile: more than %d skills", MaxProfileSkills)
	}
	if len(p.McpServers) > MaxProfileMcpServers {
		return nil, fmt.Errorf("profile: more than %d mcp servers", MaxProfileMcpServers)
	}
	out := &core.AgentProfile{Instructions: p.Instructions}
	if len(p.Env) > 0 {
		out.Env = make(map[string]string, len(p.Env))
		for k, v := range p.Env {
			if !validEnvName(k) {
				return nil, fmt.Errorf("profile: invalid env name %q", k)
			}
			out.Env[k] = v
		}
	}
	seenSkill := map[string]bool{}
	for _, s := range p.Skills {
		if !core.ValidBundlePath(s.Name) || strings.Contains(s.Name, "/") || strings.HasPrefix(s.Name, ".") {
			return nil, fmt.Errorf("profile: invalid skill name %q", s.Name)
		}
		if seenSkill[s.Name] {
			return nil, fmt.Errorf("profile: duplicate skill %q", s.Name)
		}
		seenSkill[s.Name] = true
		cs := core.ProfileSkill{Name: s.Name}
		seenFile := map[string]bool{}
		for _, f := range s.Files {
			if !core.ValidBundlePath(f.Path) {
				return nil, fmt.Errorf("profile: skill %s: invalid file path %q", s.Name, f.Path)
			}
			if seenFile[f.Path] {
				return nil, fmt.Errorf("profile: skill %s: duplicate file %q", s.Name, f.Path)
			}
			seenFile[f.Path] = true
			content := []byte(f.Content)
			if f.ContentBase64 != "" {
				raw, err := base64.StdEncoding.DecodeString(f.ContentBase64)
				if err != nil {
					return nil, fmt.Errorf("profile: skill %s: file %q: invalid contentBase64", s.Name, f.Path)
				}
				content = raw
			}
			cs.Files = append(cs.Files, core.ProfileFile{Path: f.Path, Content: content})
		}
		out.Skills = append(out.Skills, cs)
	}
	seenServer := map[string]bool{}
	for _, m := range p.McpServers {
		if strings.TrimSpace(m.Name) == "" {
			return nil, errors.New("profile: mcp server without a name")
		}
		if seenServer[m.Name] {
			return nil, fmt.Errorf("profile: duplicate mcp server %q", m.Name)
		}
		seenServer[m.Name] = true
		cm := core.MCPServer{Name: m.Name, Type: m.Type, Command: m.Command, Args: m.Args, Env: coreNameValues(m.Env), URL: m.URL, Headers: coreNameValues(m.Headers)}
		switch cm.Transport() {
		case core.MCPStdio:
			if strings.TrimSpace(m.Command) == "" {
				return nil, fmt.Errorf("profile: mcp server %q: stdio needs a command", m.Name)
			}
			cm.Type = core.MCPStdio
		case core.MCPHTTP, core.MCPSSE:
			if strings.TrimSpace(m.URL) == "" {
				return nil, fmt.Errorf("profile: mcp server %q: %s needs a url", m.Name, m.Type)
			}
		default:
			return nil, fmt.Errorf("profile: mcp server %q: unknown type %q", m.Name, m.Type)
		}
		out.MCPServers = append(out.MCPServers, cm)
	}
	return out, nil
}

// wireMcpType omits "stdio" on the wire (the ACP stdio variant has no type).
func wireMcpType(t string) string {
	if t == core.MCPStdio {
		return ""
	}
	return t
}

func wireNameValues(in []core.NameValue) []NameValue {
	if len(in) == 0 {
		return nil
	}
	out := make([]NameValue, 0, len(in))
	for _, nv := range in {
		out = append(out, NameValue{Name: nv.Name, Value: nv.Value})
	}
	return out
}

func coreNameValues(in []NameValue) []core.NameValue {
	if len(in) == 0 {
		return nil
	}
	out := make([]core.NameValue, 0, len(in))
	for _, nv := range in {
		out = append(out, core.NameValue{Name: nv.Name, Value: nv.Value})
	}
	return out
}

// validEnvName accepts POSIX-style environment variable names.
func validEnvName(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// CoreOptions maps a follow-up input's options and execution onto
// core.SessionOptions.
func (r SendInputRequest) CoreOptions() (core.SessionOptions, error) {
	opts := r.Options.Core()
	exec, err := r.Execution.Core()
	if err != nil {
		return core.SessionOptions{}, err
	}
	opts.Execution = exec
	return opts, nil
}

func WireSessionResult(r core.SessionResult) CreateSessionResponse {
	return CreateSessionResponse{SessionID: r.SessionID, TurnID: r.TurnID, Phase: r.Phase}
}

func (r CreateSessionResponse) Core() core.SessionResult {
	return core.SessionResult{SessionID: r.SessionID, TurnID: r.TurnID, Phase: r.Phase}
}

func WireTargets(ts []core.Target) []Target {
	out := make([]Target, 0, len(ts))
	for _, t := range ts {
		out = append(out, Target{ID: t.ID, Provider: t.Provider, Models: t.Models, ReasoningEfforts: t.ReasoningEfforts,
			ReasoningByModel: t.ReasoningByModel, DefaultModel: t.DefaultModel, DefaultReasoning: t.DefaultReasoning, MCPTransports: t.MCPTransports})
	}
	return out
}

func (r ListTargetsResponse) Core() []core.Target {
	out := make([]core.Target, 0, len(r.Targets))
	for _, t := range r.Targets {
		out = append(out, core.Target{ID: t.ID, Provider: t.Provider, Models: t.Models, ReasoningEfforts: t.ReasoningEfforts,
			ReasoningByModel: t.ReasoningByModel, DefaultModel: t.DefaultModel, DefaultReasoning: t.DefaultReasoning, MCPTransports: t.MCPTransports})
	}
	return out
}

func WireMessages(ms []core.Message) []Message {
	out := make([]Message, 0, len(ms))
	for _, m := range ms {
		out = append(out, Message{Version: m.Version, Role: m.Role, Text: m.Text, UserVisible: m.UserVisible, Completed: m.Completed,
			MessageID: m.MessageID, Seq: m.Seq, CreatedAtMs: m.CreatedAtMs, Thinking: m.Thinking, Metrics: m.Metrics})
	}
	return out
}

func (r MessagesResponse) Core() []core.Message {
	out := make([]core.Message, 0, len(r.Messages))
	for _, m := range r.Messages {
		out = append(out, core.Message{Version: m.Version, Role: m.Role, Text: m.Text, UserVisible: m.UserVisible, Completed: m.Completed,
			MessageID: m.MessageID, Seq: m.Seq, CreatedAtMs: m.CreatedAtMs, Thinking: m.Thinking, Metrics: m.Metrics})
	}
	return out
}

func WireTurnStatus(ts core.TurnStatus) PollTurnResponse {
	return PollTurnResponse{Idle: ts.Idle, Failed: ts.Failed, FailMessage: ts.FailMessage}
}

func (r PollTurnResponse) Core() core.TurnStatus {
	return core.TurnStatus{Idle: r.Idle, Failed: r.Failed, FailMessage: r.FailMessage}
}

// MinWriteFileBytes is the largest file a core that advertises writeFile
// must accept in one write unless its discovery states a smaller
// limits.maxInputBytes: the chat attachment cap, since the bridge hands
// each attachment of a message to the agent's workspace with one write.
const MinWriteFileBytes = wire.MaxAttachmentBytes

// writeFileEnvelope is the JSON around a binary write's base64 content.
var writeFileEnvelope = int64(len(`{"content":"","contentBase64":""}`))

// WriteFileBodyBytes is the request body size of a write of an n-byte file
// as ContentBase64: what a core's limits.maxInputBytes is compared with.
func WriteFileBodyBytes(n int64) int64 {
	return int64(base64.StdEncoding.EncodedLen(int(n))) + writeFileEnvelope
}

// MaxWriteFileBytes is the largest file one binary write sends within a
// request body limit (0 = no file fits).
func MaxWriteFileBytes(maxInput int64) int64 {
	room := maxInput - writeFileEnvelope
	if room < 4 {
		return 0
	}
	return room / 4 * 3
}

type WriteWorkspaceFileRequest struct {
	Content string `json:"content"`
	// ContentBase64 carries binary file bytes (e.g. an image attachment) that
	// cannot be represented as a UTF-8 Content string. When set, the core
	// decodes it and Content is ignored.
	ContentBase64 string `json:"contentBase64,omitempty"`
}

// MaxSessionFiles is the listing size a core should cap
// ListSessionFilesResponse at (setting Truncated): the bridge diffs the
// listing after every turn, so an unbounded tree must not reach the wire.
const MaxSessionFiles = 2000

// SessionFile is one file of a session's workspace on the wire (capability
// sessionFiles). Path is workspace-relative and slash-separated; SizeBytes
// and ModTimeMs are the change-detection stamp the bridge compares.
type SessionFile struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	ModTimeMs int64  `json:"modTimeMs"`
}

// ListSessionFilesResponse answers GET .../sessions/{sid}/files. The core
// lists regular files only (no symlinks, nothing outside the workspace) and
// applies its own ignore rules: VCS and hidden entries (.git, .venv, the
// bridge's .room/ context tree), dependency and environment dirs
// (node_modules, venv, vendor), build outputs (target, dist, build). It
// returns at most MaxSessionFiles entries and sets Truncated when it stopped
// early.
type ListSessionFilesResponse struct {
	Files     []SessionFile `json:"files"`
	Truncated bool          `json:"truncated,omitempty"`
}

func WireSessionFiles(fs []core.SessionFile) []SessionFile {
	out := make([]SessionFile, 0, len(fs))
	for _, f := range fs {
		out = append(out, SessionFile{Path: f.Path, SizeBytes: f.Bytes, ModTimeMs: f.MtimeMs})
	}
	return out
}

func (r ListSessionFilesResponse) Core() []core.SessionFile {
	out := make([]core.SessionFile, 0, len(r.Files))
	for _, f := range r.Files {
		if f.Path == "" {
			continue
		}
		out = append(out, core.SessionFile{Path: f.Path, Bytes: f.SizeBytes, MtimeMs: f.ModTimeMs})
	}
	return out
}

func HasCapability(d Discovery, cap string) bool {
	for _, c := range d.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

func WorkspaceSessionsPath(workspaceID string) string {
	return "/core/v1/workspaces/" + escapePath(workspaceID) + "/sessions"
}

func SessionInputPath(workspaceID, sessionID string) string {
	return WorkspaceSessionsPath(workspaceID) + "/" + escapePath(sessionID) + "/input"
}

func SessionMessagesPath(workspaceID, sessionID string) string {
	return WorkspaceSessionsPath(workspaceID) + "/" + escapePath(sessionID) + "/messages"
}

func SessionTurnPath(workspaceID, sessionID string) string {
	return WorkspaceSessionsPath(workspaceID) + "/" + escapePath(sessionID) + "/turn"
}

func SessionInterruptPath(workspaceID, sessionID string) string {
	return WorkspaceSessionsPath(workspaceID) + "/" + escapePath(sessionID) + "/interrupt"
}

func SessionFilesPath(workspaceID, sessionID string) string {
	return WorkspaceSessionsPath(workspaceID) + "/" + escapePath(sessionID) + "/files"
}

func WorkspacePath(workspaceID string) string {
	return "/core/v1/workspaces/" + escapePath(workspaceID)
}

// WorkspaceFilePath is the one file route of a workspace: PUT writes a
// room-context file (writeFile), GET reads a file's raw bytes
// (sessionFileBytes). relPath is escaped as ONE segment (a "/" travels as
// %2F); the server takes it from a {path...} wildcard.
func WorkspaceFilePath(workspaceID, relPath string) string {
	return WorkspacePath(workspaceID) + "/files/" + escapePath(relPath)
}

func escapePath(v string) string {
	return url.PathEscape(v)
}
