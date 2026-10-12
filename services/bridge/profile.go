package bridge

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/services/bridge/roomtools"
)

// Agent profiles (docs/prd/2026-10-12-experts-and-teams.md): the role an
// agent session wears — the session guidance, an expert's instructions, the
// skill packs in effect, and MCP servers. A core advertising agentProfile
// receives the profile on session create and maps it natively; any other
// core gets the guidance and the instructions prefixed to its first prompt
// (sessionGuidance decides which).
//
// Session contributors extend the profile of every session the bridge
// creates for an agentProfile core with per-session entries that are not
// content: the room tools MCP server (a loopback endpoint with a
// per-session bearer) is one. A contributor's MCP servers and env never
// count toward the session's content checksum, so a fresh per-session
// credential does not force a new session.

// SessionInfo describes the agent session the bridge is about to create.
type SessionInfo struct {
	TenantID  string
	RoomID    string
	ChannelID string
	Owner     string // the agent's owner
	Core      string
	Provider  string
	Sandboxed bool
	// MCPTransports are the MCP transports the target's agent accepts
	// (core.Target.MCPTransports; empty = stdio only): contribute a server
	// of a transport the agent can reach.
	MCPTransports []string
	// JobID / RequesterUserID are the job whose drive creates the session.
	// Later turns of the same session report theirs through
	// SessionContribution.Turn.
	JobID           string
	RequesterUserID string
}

// TurnInfo is one turn driven in a session: the claimed job and the human
// who asked for it (the requester whose authority the turn runs under).
type TurnInfo struct {
	JobID           string
	RequesterUserID string
	ChannelID       string
	MessageSeq      int64
	// Workspace reads the session's workspace through the core's
	// session-files seam; it reports an error when the core offers none.
	Workspace SessionWorkspace
	// Team is the roster when the turn wears a team expert (team.go).
	Team *roomtools.Team
}

// SessionWorkspace reads one driven session's workspace files.
type SessionWorkspace interface {
	ListFiles(ctx context.Context) ([]corepkg.SessionFile, error)
	ReadFile(ctx context.Context, rel string) ([]byte, error)
}

// sessionWorkspace is a session's workspace as the core exposes it
// (core.SessionFiles). The session id is read at call time: the first
// turn starts before its session exists.
type sessionWorkspace struct {
	b  *Bridge
	rs *roomSession
}

func (w sessionWorkspace) ids() (core, ws, sess string) {
	w.rs.mu.Lock()
	defer w.rs.mu.Unlock()
	return w.rs.core, w.rs.workspaceID, w.rs.sessionID
}

func (w sessionWorkspace) ListFiles(ctx context.Context) ([]corepkg.SessionFile, error) {
	coreName, ws, sess := w.ids()
	files, ep, _, ok := w.b.sessionFiles(ctx, coreName)
	if !ok {
		return nil, errNoSessionFiles
	}
	if ws == "" || sess == "" {
		return nil, errors.New("the session has not started yet")
	}
	return files.ListSessionFiles(ctx, ep, ws, sess)
}

func (w sessionWorkspace) ReadFile(ctx context.Context, rel string) ([]byte, error) {
	coreName, ws, sess := w.ids()
	files, ep, _, ok := w.b.sessionFiles(ctx, coreName)
	if !ok {
		return nil, errNoSessionFiles
	}
	if ws == "" || sess == "" {
		return nil, errors.New("the session has not started yet")
	}
	return files.ReadSessionFile(ctx, ep, ws, sess, rel)
}

// errNoSessionFiles: the session's core does not expose workspace files.
var errNoSessionFiles = errors.New("the agent's core does not expose its workspace files")

// SessionContribution is what a contributor adds to one session.
type SessionContribution struct {
	// MCPServers are attached to the session (ACP shape).
	MCPServers []corepkg.MCPServer
	// Env is extra agent environment (secret values).
	Env map[string]string
	// RoomTools reports that the contribution attaches the bnbloc-room
	// tools: the session guidance then says how to use them
	// (roomlog.Guidance.HasTools). No contributor words guidance itself.
	RoomTools bool
	// Turn, when set, is called at the start of every turn driven in the
	// session (the first one included), before the turn's input reaches
	// the core.
	Turn func(TurnInfo)
	// Release, when set, is called once when the bridge drops the session
	// binding (rebind, room forgotten, core endpoint changed): revoke
	// whatever the contribution granted.
	Release func()
}

// SessionContributor contributes per-session profile entries. It is
// consulted only for sessions on a core that advertises agentProfile (no
// other core can attach MCP servers). An error skips this contributor for
// the session (logged) and never fails the drive.
type SessionContributor interface {
	ContributeSession(ctx context.Context, s SessionInfo) (SessionContribution, error)
}

// SessionContributorFunc adapts a function to SessionContributor.
type SessionContributorFunc func(ctx context.Context, s SessionInfo) (SessionContribution, error)

// ContributeSession implements SessionContributor.
func (f SessionContributorFunc) ContributeSession(ctx context.Context, s SessionInfo) (SessionContribution, error) {
	return f(ctx, s)
}

// contributors is the bridge's registered SessionContributors.
type contributors struct {
	mu   sync.Mutex
	list []SessionContributor
}

// AddSessionContributor registers a contributor for every session created
// from now on. Call it before Run.
func (b *Bridge) AddSessionContributor(c SessionContributor) {
	if c == nil {
		return
	}
	b.contrib.mu.Lock()
	b.contrib.list = append(b.contrib.list, c)
	b.contrib.mu.Unlock()
}

func (b *Bridge) sessionContributors() []SessionContributor {
	b.contrib.mu.Lock()
	defer b.contrib.mu.Unlock()
	return append([]SessionContributor(nil), b.contrib.list...)
}

// sessionHooks are a bound session's contribution callbacks.
type sessionHooks struct {
	turn    []func(TurnInfo)
	release []func()
}

// contribute asks every contributor for its part of a new session's profile
// and merges it into p (which must be non-nil). It returns the hooks to
// keep with the session and whether a contribution attached the room tools.
func (b *Bridge) contribute(ctx context.Context, info SessionInfo, p *corepkg.AgentProfile) (hooks sessionHooks, roomTools bool) {
	for _, c := range b.sessionContributors() {
		part, err := c.ContributeSession(ctx, info)
		if err != nil {
			slog.Warn("bnbloc bridge session contributor failed",
				"event", "bnbloc.bridge.session_contributor_failed", "room", info.RoomID, "core", info.Core, "provider", info.Provider, "error", err)
			continue
		}
		p.MCPServers = append(p.MCPServers, part.MCPServers...)
		if len(part.Env) > 0 {
			if p.Env == nil {
				p.Env = map[string]string{}
			}
			for k, v := range part.Env {
				p.Env[k] = v
			}
		}
		roomTools = roomTools || part.RoomTools
		if part.Turn != nil {
			hooks.turn = append(hooks.turn, part.Turn)
		}
		if part.Release != nil {
			hooks.release = append(hooks.release, part.Release)
		}
	}
	return hooks, roomTools
}

// appendInstructions appends one block of profile instructions to another,
// a blank line between them (either may be empty).
func appendInstructions(base, more string) string {
	more = strings.TrimSpace(more)
	switch {
	case more == "":
		return base
	case strings.TrimSpace(base) == "":
		return more
	}
	return strings.TrimRight(base, "\n") + "\n\n" + more
}

// sessionGuidance is the one place that decides where a new session's
// guidance goes: what the session is told once, before its first turn, and
// never again per turn. The guidance is roomlog.SessionGuidance rendered
// with the session's facts (first, so it is a stable prefix providers
// cache), then the content the turn wears (the expert's instructions and
// skills) and, on a core advertising agentProfile, the session
// contributors' MCP servers and env.
//
//   - A core advertising agentProfile gets it all in the profile its session
//     is created with (profile; prefix is empty).
//   - Any other core gets the guidance and the content as the head of the
//     session's first prompt (prefix; profile is nil), the skill files
//     written under .agents/skills/ when it accepts workspace files. Its
//     follow-up turns carry none of it.
//
// The facts are kept with the session (the .room/README.md it is written
// with). The guidance is bnbloc's own, stable text: like the contributors'
// per-session servers it stays out of the session's binding sum, which is
// the content's (jobContent.bindingSum).
func (b *Bridge) sessionGuidance(ctx context.Context, d *drive, ep corepkg.Endpoint, workspaceID string, info SessionInfo) (profile *corepkg.AgentProfile, prefix string, hooks sessionHooks, facts roomlog.Guidance) {
	_, pull := b.coreEntry(d.core)
	facts.HasRoomFiles = pull && d.caps.writer != nil
	if d.caps.profile {
		profile = d.content.profile()
		if profile == nil {
			profile = &corepkg.AgentProfile{}
		}
		hooks, facts.HasTools = b.contribute(ctx, info, profile)
		profile.Instructions = appendInstructions(roomlog.SessionGuidance(facts), profile.Instructions)
		return profile, "", hooks, facts
	}
	var sb strings.Builder
	sb.WriteString(roomlog.SessionGuidance(facts))
	sb.WriteString("\n")
	if !d.content.empty() {
		written := b.writeFallbackSkills(ctx, d.caps.bytesWriter, ep, d.core, workspaceID, d.content)
		sb.WriteString(d.content.promptPrefix(written))
	}
	sb.WriteString("---\n\n")
	return nil, sb.String(), sessionHooks{}, facts
}

// startTurn runs the session's turn hooks.
func (rs *roomSession) startTurn(t TurnInfo) {
	rs.mu.Lock()
	hooks := append([]func(TurnInfo){}, rs.hooks.turn...)
	rs.mu.Unlock()
	for _, h := range hooks {
		h(t)
	}
}

// release runs the session's release hooks once.
func (rs *roomSession) release() {
	rs.mu.Lock()
	hooks := rs.hooks.release
	rs.hooks = sessionHooks{}
	rs.mu.Unlock()
	for _, h := range hooks {
		h()
	}
}

// targetTransports returns the MCP transports of the target a session is
// created for, from the last listed targets of the core (by target id, else
// by provider).
func (b *Bridge) targetTransports(coreName, target, provider string) []string {
	var byProvider []string
	for _, t := range b.cachedTargets(coreName) {
		if t.ID == target {
			return t.MCPTransports
		}
		if byProvider == nil && strings.EqualFold(t.Provider, provider) {
			byProvider = t.MCPTransports
		}
	}
	return byProvider
}

// unbindLocked drops a session binding and releases what its contributors
// granted (asynchronously: callers hold b.mu). Callers hold b.mu.
func (b *Bridge) unbindLocked(key driveKey) {
	rs := b.sessions[key]
	delete(b.sessions, key)
	if rs != nil {
		go rs.release()
	}
}
