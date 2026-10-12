// Package core defines the pluggable seam for the local agent core that bnbloc
// drives over HTTP (the outbound control path): create a workspace and session,
// send follow-up input, list agent targets, read the session's replies, and
// poll its turn. Each core registers under its own name and is selected per
// deployment without changing the gateway or bridge.
//
// This seam is outbound only. Inbound activity (core -> bnbloc) stays per-core
// glue that runs inside the core's own process, because it imports the core's
// packages and cannot be abstracted into bnbloc.
package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/extreg"
)

// Endpoint identifies one member's local agent core daemon.
type Endpoint struct {
	BaseURL string
	Token   string
}

// OK reports whether the endpoint is usable.
func (e Endpoint) OK() bool { return e.BaseURL != "" }

// TurnInterrupter is implemented by cores that can stop an in-flight turn. It is
// optional: a core without it cannot be interrupted, and callers fall back to
// abandoning the turn. Interrupt must be idempotent and must not error when no
// turn is running.
type TurnInterrupter interface {
	Interrupt(ctx context.Context, ep Endpoint, workspaceID, sessionID string) error
}

// TargetResolver is implemented by cores whose target ids can be derived from a
// provider name without listing live targets. False means the bridge should use
// the normal ListTargets lookup.
type TargetResolver interface {
	ResolveTarget(provider string) (targetID string, ok bool)
}

// Capability names an optional provider capability (interrupt, workspace-file
// writes, workspace deletion, session file reads, session workdir). Where Core
// Protocol defines the capability, the value is its discovery capability
// string, so a protocol daemon and a manifest describe it with the same name.
type Capability string

const (
	CapabilityInterrupt       Capability = "interrupt"
	CapabilityWriteFile       Capability = "writeFile"
	CapabilityDeleteWorkspace Capability = "deleteWorkspace"
	// CapabilitySessionFiles (SessionFiles) is offered by a manifest that
	// declares the session-file ops and by a Core Protocol daemon that
	// advertises it (GET .../sessions/{sid}/files, reads via
	// sessionFileBytes). CapabilitySessionWorkdir (SessionWorkdirReporter) has
	// no Core Protocol v1 endpoint: only manifest cores offer it.
	CapabilitySessionFiles   Capability = "sessionFiles"
	CapabilitySessionWorkdir Capability = "sessionWorkdir"
	// CapabilitySessionFileBytes marks SessionFiles reads as binary-safe: the
	// core returns a file's exact bytes (a manifest declares
	// readSessionFile.extract.contentBase64; a Core Protocol daemon serves
	// GET /core/v1/workspaces/{ws}/files/{path}). Without it reads are text only,
	// and the bridge publishes no binary deliverables from that core.
	CapabilitySessionFileBytes Capability = "sessionFileBytes"
	// CapabilitySandboxExec marks a core that honors SessionOptions.Execution:
	// it keeps its CLI on the host (confined to the sandbox workdir) and routes
	// the CLI's shell commands into the given sandbox space. A core that does
	// not advertise it never receives an Execution (Supports gates it).
	CapabilitySandboxExec Capability = "sandboxExec"
)

// Supports reports whether p offers capability c for ep. It is the
// method-less counterpart of As, for capabilities that change what a request
// carries rather than which interface is called (sandboxExec): only a
// CapabilityReporter can offer them.
func Supports(ctx context.Context, p Provider, ep Endpoint, c Capability) bool {
	r, ok := p.(CapabilityReporter)
	return ok && r.Supports(ctx, ep, c)
}

// CapabilityReporter is implemented by a provider whose optional capabilities
// are known only from data — a manifest's declared ops, or a protocol daemon's
// discovery document — rather than from its Go type. Such a provider
// implements every optional method and answers Supports per endpoint; Supports
// must be cheap (cached) and side-effect free.
type CapabilityReporter interface {
	Supports(ctx context.Context, ep Endpoint, c Capability) bool
}

// As returns p as the optional capability interface T when p implements T and,
// if p is a CapabilityReporter, reports capability c supported for ep. Callers
// probe optional capabilities only through As, so a provider never appears to
// offer what its manifest or daemon does not.
func As[T any](ctx context.Context, p Provider, ep Endpoint, c Capability) (T, bool) {
	var zero T
	t, ok := p.(T)
	if !ok {
		return zero, false
	}
	if r, ok := p.(CapabilityReporter); ok && !r.Supports(ctx, ep, c) {
		return zero, false
	}
	return t, true
}

// SessionFile is one file in an agent session's workspace listing. Bytes and
// MtimeMs are the change-detection stamp: size alone misses a same-length
// rewrite, so a caller compares both.
type SessionFile struct {
	Path    string // workspace-relative, slash-separated
	Bytes   int64
	MtimeMs int64
}

// SessionFiles is implemented by cores that expose an agent session's workspace
// files — the deliverables the agent wrote. The bridge lists them after each
// turn and reads the changed ones to publish as room artifact versions. A core
// without it publishes no deliverables (the turn's reply is unaffected).
type SessionFiles interface {
	// ListSessionFiles returns every file in the session workspace with its
	// change-detection stamp.
	ListSessionFiles(ctx context.Context, ep Endpoint, workspaceID, sessionID string) ([]SessionFile, error)
	// ReadSessionFile returns one file's content; relPath is a path from
	// ListSessionFiles. The bytes are exact only when the core also reports
	// CapabilitySessionFileBytes; otherwise they are the core's text rendering.
	ReadSessionFile(ctx context.Context, ep Endpoint, workspaceID, sessionID, relPath string) ([]byte, error)
}

// SessionWorkdirReporter is implemented by cores that report an agent
// session's working directory on the core's host. The bridge mirrors that
// directory for room live sync when no explicit sync directory is configured;
// a core without it leaves the default sync directory unset.
type SessionWorkdirReporter interface {
	SessionWorkdir(ctx context.Context, ep Endpoint, workspaceID, sessionID string) (string, error)
}

// WorkspaceFileWriter is implemented by cores whose session workspaces can
// receive bnbloc-owned files: the bridge materializes the shared room history
// under .room/ so the agent consults it with its own file tools (pull) instead
// of having history pushed into every prompt, and the agent-config files of a
// profile (skills a core without agentProfile reads from .agents/skills/).
// relPath must pass WorkspaceFileAllowed: a file under one of
// BnblocOwnedPaths; a core refuses any other path. A core without it keeps
// the push-based prompt context.
type WorkspaceFileWriter interface {
	WriteWorkspaceFile(ctx context.Context, ep Endpoint, workspaceID, relPath, content string) error
}

// WorkspaceFileBytesWriter is the binary counterpart of WorkspaceFileWriter: a
// core that can accept binary workspace files (images, PDFs) implements it so
// the bridge can materialize a chat attachment under .room/attachments/ for the
// agent to open with its own file/vision tools. relPath must pass
// WorkspaceFileAllowed, like WorkspaceFileWriter's.
// A core without it does not receive binary attachments (the turn still names
// them by path); this keeps attachment interpretation in the core runtime, not
// in bnbloc.
type WorkspaceFileBytesWriter interface {
	WriteWorkspaceFileBytes(ctx context.Context, ep Endpoint, workspaceID, relPath string, content []byte) error
}

// SessionOptions carries per-drive agent tuning selected by the sender: the
// model and the reasoning effort. Both are optional; empty means the core/CLI
// default. They are forwarded end-to-end (chat -> bridge -> core -> CLI) so a
// room member's model/reasoning choice actually reaches the agent process.
type SessionOptions struct {
	Model           string // model id, e.g. "gpt-5.5" or "" for the default
	ReasoningEffort string // "low" | "medium" | "high" | "" (default)
	// Execution, when set, asks the core to run this drive's shell commands
	// in a sandbox (capability sandboxExec). nil = run on the host as always.
	// The bridge sets it only for a core that advertises sandboxExec, on the
	// session create AND every follow-up turn of a sandboxed session.
	Execution *Execution
}

// Execution is the neutral execution descriptor (wire: protocol.Execution).
type Execution struct {
	Sandbox SandboxExecution
}

// SandboxExecution tells a sandboxExec core where and how to run commands:
// the Sandbox Protocol endpoint, a short-lived space-scoped exec token (a
// secret: never logged, never persisted), the machine + space addressing,
// the bnbloc-vmexec helper path, the host→guest mounts, the host commands
// allowed to bypass the sandbox, and the host confinement of the CLI.
type SandboxExecution struct {
	URL         string
	ExecToken   string
	ExpiresAtMs int64
	Machine     string
	Space       string
	VMExec      string
	Workdir     string
	Mounts      []PathMount
	Passthrough []string
	HostConfine HostConfine
}

// PathMount maps one host directory to its guest path.
type PathMount struct {
	Host  string
	Guest string
}

// HostConfine is the host OS-sandbox scope of the agent CLI process.
// DenyWrite wins over every allow (e.g. routing config the core placed in
// the workdir, which the agent must not edit mid-turn), also for a path
// that does not exist yet: the agent may not create it either. A DenyWrite
// entry ending in "/" names a directory.
type HostConfine struct {
	Workspace  string
	AllowRead  []string
	AllowWrite []string
	DenyWrite  []string
}

// SessionRequest is the neutral request to create an agent session.
type SessionRequest struct {
	WorkspaceID string         // the core workspace/project to run in
	TargetID    string         // agent target/harness id chosen by the caller
	Prompt      string         // initial user message
	Options     SessionOptions // model + reasoning selected by the sender
	// Profile is the role the session wears (capability agentProfile): an
	// expert's instructions, skills, MCP servers and env. nil = the agent's
	// own configuration only. The bridge sets it only for a core that
	// advertises agentProfile; its secrets are never logged or persisted.
	Profile *AgentProfile
}

// SessionResult is the neutral result of creating a session. TurnID and Phase
// are best-effort: a core that does not surface them leaves them empty.
type SessionResult struct {
	SessionID string
	TurnID    string
	Phase     string
}

// Message is one assistant reply produced in a session, in the neutral shape the
// reflow path needs. Version is a monotonic cursor; UserVisible distinguishes a
// real answer from the core's internal notices; Completed is false for a
// streaming snapshot that must not be reflowed as a fragment.
type Message struct {
	Version     int64
	Role        string
	Text        string
	UserVisible bool
	Completed   bool
	// MessageID / Seq / CreatedAtMs carry the core's identifiers for the room
	// post (dedupe + ordering + timestamp). Seq, when reported, must be the
	// message's stable, strictly increasing sequence within the session (two
	// messages never share one — see RoomSeq). A core that lacks them leaves
	// them zero.
	MessageID   string
	Seq         int64
	CreatedAtMs int64
	// Thinking is the model's reasoning trace for this turn, when the core
	// reports one (reasoning models only); empty otherwise. Metrics is the
	// turn's usage/timing/cost as a free-form map the core supplies verbatim
	// (token counts, duration, cost) — neutral so the seam is not coupled to any
	// one core's metric shape; nil when the core reports none.
	Thinking string
	Metrics  map[string]any
}

// RoomSeq is the message's room-activity sequence: the core's stable
// per-message Seq, or its Version cursor when the core reports none. Activity
// deduplicates replies per (room, agent session) by this value — anything at
// or below the session's stored cursor is treated as a replay — so it must be
// strictly increasing within a session and stable across re-posts of the same
// message (a retried post must dedupe, not duplicate).
func (m Message) RoomSeq() int64 {
	if m.Seq > 0 {
		return m.Seq
	}
	return m.Version
}

// TurnStatus is the settled state of a session's active turn. Idle means no turn
// is in flight (safe to send / done draining); Failed with FailMessage surfaces
// an error so a no-reply turn is not silent.
type TurnStatus struct {
	Idle        bool
	Failed      bool
	FailMessage string
}

// Target is one selectable agent target/harness exposed by the core. Models and
// the reasoning catalogs are discovered at runtime (e.g. a CLI's own model
// list); empty means the caller offers no picker and the core/CLI default is
// used. They carry live data, not names hardcoded anywhere, so a new model
// appears without a code change.
type Target struct {
	ID       string
	Provider string
	Models   []string // selectable model ids, in the core's own order
	// ReasoningEfforts is the target-level reasoning ladder used when a model has
	// no entry in ReasoningByModel (a uniform ladder). Empty = no reasoning.
	ReasoningEfforts []string
	// ReasoningByModel maps a model id to the reasoning efforts valid for THAT
	// model, so the picker can switch options when the model changes (mirrors
	// a daemon's reasoningOptionsByModel). A model absent here falls back to
	// ReasoningEfforts.
	ReasoningByModel map[string][]string
	// DefaultModel / DefaultReasoning are the target's currently effective values
	// (e.g. from the CLI's own config), so the picker can show what is in force
	// without a selection. Empty when the core does not surface them.
	DefaultModel     string
	DefaultReasoning string
	// MCPTransports are the MCP server transports this target's agent
	// accepts in a profile (MCPStdio, MCPHTTP, MCPSSE), so a profile
	// contributor attaches a server the agent can actually reach. Empty =
	// stdio only (every ACP agent takes stdio). Per target: an adapter
	// fronting several agents learns each agent's support separately.
	MCPTransports []string
}

// TransportSupported reports whether the target's agent accepts MCP
// servers of the given transport (stdio always).
func (t Target) TransportSupported(transport string) bool {
	if transport == "" || transport == MCPStdio {
		return true
	}
	for _, s := range t.MCPTransports {
		if s == transport {
			return true
		}
	}
	return false
}

// Provider is the outbound control contract for one agent core. All methods are
// per-request: the caller supplies the member's Endpoint every time, because
// each member runs their own local core with their own address and token.
type Provider interface {
	// Name is the stable registry name, e.g. "acme".
	Name() string
	// CreateWorkspace creates (or resolves) a workspace/project to host sessions
	// and returns its id.
	CreateWorkspace(ctx context.Context, ep Endpoint) (workspaceID string, err error)
	// CreateSession starts an agent session and returns its identifiers.
	CreateSession(ctx context.Context, ep Endpoint, req SessionRequest) (SessionResult, error)
	// SendInput delivers a follow-up user message to an existing session. opts
	// carries the model/reasoning selected for this turn (empty = unchanged).
	SendInput(ctx context.Context, ep Endpoint, workspaceID, sessionID, text string, opts SessionOptions) error
	// ListTargets returns the enabled agent targets on the core.
	ListTargets(ctx context.Context, ep Endpoint) ([]Target, error)
	// Messages returns assistant messages in the session with Version > after,
	// ascending, plus the latest version cursor. This is the inbound (reply ->
	// room) path the bridge reflows; each core maps its own message shape here.
	Messages(ctx context.Context, ep Endpoint, workspaceID, sessionID string, after int64) (msgs []Message, latest int64, err error)
	// PollTurn returns the session's active-turn status for reflow (idle when the
	// turn is done draining; failed to surface an error).
	PollTurn(ctx context.Context, ep Endpoint, workspaceID, sessionID string) (TurnStatus, error)
}

// Factory constructs a fresh provider instance.
type Factory func() Provider

// KindCore is this package's extreg kind. A core is one kind of extension; the
// core registry is a thin specialization of the kind-agnostic extreg registry.
const KindCore extreg.Kind = "core"

func init() {
	extreg.RegisterKind(extreg.KindDescriptor{
		Kind:  KindCore,
		Hosts: []extreg.Service{extreg.ServiceGateway, extreg.ServiceBridge},
	})
}

// BackendKind / SourceClass / CoreExtension alias the kind-agnostic extreg types
// so existing core callers are unchanged while extreg owns the storage.
type BackendKind = extreg.Backend
type SourceClass = extreg.Source
type CoreExtension = extreg.Extension

const (
	BackendNative   = extreg.BackendNative
	BackendManifest = extreg.BackendManifest
	BackendSidecar  = extreg.BackendSidecar
	SourceBuiltin   = extreg.SourceBuiltin
	SourceInstalled = extreg.SourceInstalled
)

// Register makes a native Go provider available by name — a non-removable
// built-in ({native, builtin}). Panics on a duplicate so wiring mistakes fail
// loudly at startup. The Factory is carried in the extreg record's Config.
func Register(name string, f Factory) {
	extreg.RegisterBuiltin(extreg.Extension{Kind: KindCore, Name: name, Backend: BackendNative, Config: f})
}

// Reregister installs or replaces a core at runtime without panicking on a
// duplicate. It refuses to overwrite a non-removable built-in. d classifies the
// install ({backend, installed, removable}); f is the factory (carried in Config).
func Reregister(name string, f Factory, d CoreExtension) error {
	d.Kind = KindCore
	d.Name = name
	d.Config = f
	return extreg.Reregister(d)
}

// Unregister removes a runtime-installed (removable) core; refuses a built-in.
func Unregister(name string) error {
	return extreg.Unregister(KindCore, name)
}

// New constructs the provider registered under name from its stored factory.
func New(name string) (Provider, error) {
	e, ok := extreg.Get(KindCore, name)
	if !ok {
		return nil, fmt.Errorf("core: no provider registered as %q", name)
	}
	f, ok := e.Config.(Factory)
	if ok {
		return f(), nil
	}
	provider, ok := e.Instance.(Provider)
	if !ok {
		return nil, fmt.Errorf("core: %q has no provider factory", name)
	}
	return provider, nil
}

// Describe returns the registry descriptor for a core, so callers classify a
// core uniformly instead of hardcoding built-in names.
func Describe(name string) (CoreExtension, bool) {
	return extreg.Get(KindCore, name)
}

// Names lists registered core names (sorted) for diagnostics.
func Names() []string {
	list := extreg.List(KindCore)
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, e.Name)
	}
	return out
}

// Discoverer is an optional capability: a core that can locate its local daemon
// without explicit configuration implements it. Cores that cannot self-discover
// simply do not implement this, and stay configuration-only.
type Discoverer interface {
	// Discover returns the local daemon endpoint and true when found. It must be
	// side-effect free and fast (a file read or a short probe); callers may run
	// it opportunistically at startup.
	Discover(ctx context.Context) (Endpoint, bool)
}

// WorkdirWorkspaceCreator is the workspace half of capability sandboxExec: it
// creates a workspace whose directory is the given host workdir (under the
// sandbox workspaces root), so everything the bridge writes into the
// workspace before the session starts (.room/ context, attachments) lands in
// the directory the sandboxed session runs in and the guest sees. Callers
// reach it through As(..., CapabilitySandboxExec).
type WorkdirWorkspaceCreator interface {
	CreateWorkspaceAt(ctx context.Context, ep Endpoint, workdir string) (workspaceID string, err error)
}

// WorkspaceDeleter is an optional capability: a core that can delete a
// workspace it created implements it, so a caller that provisions a workspace
// but then fails to start a session can undo the empty workspace instead of
// leaking it. Cores without a delete operation simply do not implement this.
type WorkspaceDeleter interface {
	// DeleteWorkspace removes a workspace by id. It is best-effort cleanup:
	// callers log and move on when it errors.
	DeleteWorkspace(ctx context.Context, ep Endpoint, workspaceID string) error
}

// QualifyTargetID prefixes a core-local target id with its core so the combined
// id routes unambiguously across multiple cores. Splitting is on the FIRST slash
// only, so a core-local id may itself contain slashes.
func QualifyTargetID(core, rawID string) string { return core + "/" + rawID }

// SplitTargetID recovers (core, rawID) from a qualified target id. When the id
// carries no core prefix (legacy single-core ids), core is "" and rawID is the
// input unchanged, so callers can fall back to a default core.
func SplitTargetID(qualified string) (core, rawID string) {
	if i := strings.IndexByte(qualified, '/'); i >= 0 {
		return qualified[:i], qualified[i+1:]
	}
	return "", qualified
}
