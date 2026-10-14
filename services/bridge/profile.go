package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	Deliverable(ctx context.Context, rel string) (string, error)
}

// sessionWorkspace is a session's workspace as the core exposes it
// (core.SessionFiles). The session id is read at call time: the first
// turn starts before its session exists.
type sessionWorkspace struct {
	b       *Bridge
	rs      *roomSession
	room    string
	channel string
	turn    deliverableTurn
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

// Deliverable is the room deliverable of workspace file rel, published now
// when its current content is not yet (save_component): a file the agent
// wrote this turn has an id before the turn's own publish pass, and before
// the room records (.room/artifacts.md, written at turn start) list it.
// Any file but bnbloc's own may be named, build output included: naming it
// is the agent's choice.
func (w sessionWorkspace) Deliverable(ctx context.Context, rel string) (string, error) {
	if dependencyPath(rel, false) || corepkg.ProtectedWorkspacePath(rel) {
		return "", fmt.Errorf("%s is not a deliverable (hidden, dependency or bnbloc's own files)", rel)
	}
	coreName, ws, sess := w.ids()
	files, ep, binarySafe, ok := w.b.sessionFiles(ctx, coreName)
	if !ok {
		return "", errNoSessionFiles
	}
	if ws == "" || sess == "" {
		return "", errors.New("the session has not started yet")
	}
	content, err := files.ReadSessionFile(ctx, ep, ws, sess, rel)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", rel, err)
	}
	maxBytes, err := w.b.artifactMaxBytes(ctx, w.room)
	if err != nil {
		return "", fmt.Errorf("the room's limits are unavailable: %w", err)
	}
	d, why := deliverableFor(rel, content, binarySafe, maxBytes, w.turn)
	if why != "" {
		return "", fmt.Errorf("%s cannot be a deliverable: %s", rel, why)
	}
	// A path a room file was placed at for editing is that file's.
	d.artifactID = w.rs.shared().editTargetOf(rel)
	if err := w.b.publishArtifactNow(ctx, w.room, w.channel, w.rs, d, "agent"); err != nil {
		return "", fmt.Errorf("publish %s: %w", rel, err)
	}
	return firstNonEmpty(d.artifactID, artifactIDForFile(w.room, w.rs, rel)), nil
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
	// State is what the contributor needs to reattach to the session after
	// a bridge restart (SessionRestorer), kept with the session's persisted
	// binding: never a secret. A contribution that grants anything without
	// a State makes the session unrestorable (a restart starts it anew).
	State json.RawMessage
}

// SessionRestorer is a SessionContributor that can reattach what it granted
// a session to the same session after the bridge restarted (the core keeps
// the session, and the agent its conversation).
type SessionRestorer interface {
	// RestoreSession reattaches from the State ContributeSession returned:
	// the hooks of the reattached grant (MCPServers and Env are ignored: the
	// session already has them). ok=false when it cannot (the session is
	// then started anew).
	RestoreSession(ctx context.Context, s SessionInfo, state json.RawMessage) (part SessionContribution, ok bool)
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

// sessionHooks are a bound session's contribution callbacks, and what
// restores them after a bridge restart: states holds each contributor's
// State by its position (nil = it granted nothing), unrestorable whether a
// grant cannot be restored.
type sessionHooks struct {
	turn         []func(TurnInfo)
	release      []func()
	states       []json.RawMessage
	unrestorable bool
}

// granted reports whether a contribution grants the session anything.
func (part SessionContribution) granted() bool {
	return len(part.MCPServers) > 0 || len(part.Env) > 0 || part.Turn != nil || part.Release != nil || part.RoomTools
}

// contribute asks every contributor for its part of a new session's profile
// and merges it into p (which must be non-nil). It returns the hooks to
// keep with the session and whether a contribution attached the room tools.
func (b *Bridge) contribute(ctx context.Context, info SessionInfo, p *corepkg.AgentProfile) (hooks sessionHooks, roomTools bool) {
	list := b.sessionContributors()
	hooks.states = make([]json.RawMessage, len(list))
	for i, c := range list {
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
		if _, ok := c.(SessionRestorer); ok && len(part.State) > 0 {
			hooks.states[i] = part.State
		} else if part.granted() {
			hooks.unrestorable = true
		}
	}
	return hooks, roomTools
}

// restoreContributions reattaches every contributor's grant to a session
// restored after a bridge restart, from the states its binding kept. ok is
// false (and nothing stays granted) when the states no longer match the
// contributors, or one cannot reattach.
func (b *Bridge) restoreContributions(ctx context.Context, info SessionInfo, states []json.RawMessage) (hooks sessionHooks, ok bool) {
	list := b.sessionContributors()
	if len(states) > len(list) {
		return sessionHooks{}, false
	}
	hooks.states = make([]json.RawMessage, len(list))
	fail := func() (sessionHooks, bool) {
		for _, r := range hooks.release {
			r()
		}
		return sessionHooks{}, false
	}
	for i, st := range states {
		if len(st) == 0 {
			continue
		}
		r, ok := list[i].(SessionRestorer)
		if !ok {
			return fail()
		}
		part, ok := r.RestoreSession(ctx, info, st)
		if !ok {
			return fail()
		}
		if part.Turn != nil {
			hooks.turn = append(hooks.turn, part.Turn)
		}
		if part.Release != nil {
			hooks.release = append(hooks.release, part.Release)
		}
		hooks.states[i] = st
	}
	return hooks, true
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
	facts.Self = agentSpeakerLabel(d.owner, d.job.Provider)
	facts.HasRoomFiles = pull && d.caps.writer != nil
	if facts.HasRoomFiles {
		facts.Channel = b.guidanceChannel(ctx, d.job.RoomID, d.job.ChannelID)
	}
	facts.TeamLead = d.content.teamRoster() != nil
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
func (b *Bridge) unbindLocked(sk sessionKey) {
	rs := b.sessions[sk]
	delete(b.sessions, sk)
	if rs != nil {
		// Dropped for good: a restart must not restore it either.
		rs.mu.Lock()
		id := rs.sessionID
		rs.mu.Unlock()
		b.workspaces.dropSession(sk, id)
		go rs.release()
	}
}

// unbindWorkspaceLocked drops every channel session of workspace slot key
// (its tree is no longer what they were started on). Callers hold b.mu.
func (b *Bridge) unbindWorkspaceLocked(key driveKey) {
	for sk := range b.sessions {
		if sk.driveKey == key {
			b.unbindLocked(sk)
		}
	}
}

// workspaceBound reports whether any channel session of slot key is bound.
func (b *Bridge) workspaceBound(key driveKey) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for sk := range b.sessions {
		if sk.driveKey == key {
			return true
		}
	}
	return false
}
