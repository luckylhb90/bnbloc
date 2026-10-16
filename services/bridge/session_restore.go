package bridge

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/luckylhb90/bnbloc/internal/roomlog"
)

// Session bindings outlive a bridge restart
// (docs/prd/2026-10-14-context-overflow-recovery.md). The desktop restarts
// the bridge whenever a core is updated; a binding kept only in memory then
// made every agent start a new core session, and its conversation was gone
// although the core still held it. Each channel session's binding is kept
// with its workspace's (workspaces.go: the core's workspace and session ids,
// the reflow cursor, the content and guidance it was created with, and what
// each session contributor needs to reattach its grant: never a secret).
// A drive that finds no live session for its channel restores the kept one
// (restoreSession) when it still fits: the same workspace, the same content,
// every contributor able to reattach. The existing checks then decide as for
// any reused session: the sandbox fence, and the core still holding it (a
// session the core no longer knows is rebound, a gone workspace replaced).

// sessionRecord is one channel session's binding as persisted.
type sessionRecord struct {
	Room      string `json:"room"`
	Channel   string `json:"channel"`
	Core      string `json:"core"`
	Provider  string `json:"provider"`
	Sandboxed bool   `json:"sandboxed,omitempty"`
	// SeatOwner and Requester are the slot's seat (driveKey): empty for
	// the owner's own.
	SeatOwner     string            `json:"seatOwner,omitempty"`
	Requester     string            `json:"requester,omitempty"`
	Owner         string            `json:"owner"`
	WorkspaceID   string            `json:"workspaceId"`
	SessionID     string            `json:"sessionId"`
	LastVersion   int64             `json:"lastVersion,omitempty"`
	ProfileSum    string            `json:"profileSum,omitempty"`
	Guidance      roomlog.Guidance  `json:"guidance"`
	Context       roomlog.CtxState  `json:"context"`
	Workdir       string            `json:"workdir,omitempty"`
	Contributions []json.RawMessage `json:"contributions,omitempty"`
}

func (r sessionRecord) key() sessionKey {
	return sessionKey{driveKey: driveKey{room: r.Room, core: r.Core, provider: r.Provider, sandboxed: r.Sandboxed, owner: r.SeatOwner, requester: r.Requester}, channel: r.Channel}
}

func sessionRecordLess(a, b sessionRecord) bool {
	if a.Room != b.Room {
		return a.Room < b.Room
	}
	if a.Channel != b.Channel {
		return a.Channel < b.Channel
	}
	if a.Core != b.Core {
		return a.Core < b.Core
	}
	if a.Provider != b.Provider {
		return a.Provider < b.Provider
	}
	if a.Sandboxed != b.Sandboxed {
		return !a.Sandboxed
	}
	if a.SeatOwner != b.SeatOwner {
		return a.SeatOwner < b.SeatOwner
	}
	return a.Requester < b.Requester
}

// persistSession keeps channel session sk's binding (rs) for a restart, or
// forgets it when rs cannot be restored (a contributor without restore).
func (b *Bridge) persistSession(sk sessionKey, rs *roomSession) {
	if rs == nil {
		return
	}
	rs.mu.Lock()
	rec := sessionRecord{
		Room: sk.room, Channel: sk.channel, Core: sk.core, Provider: sk.provider, Sandboxed: sk.sandboxed,
		SeatOwner: sk.owner, Requester: sk.requester,
		Owner: rs.owner, WorkspaceID: rs.workspaceID, SessionID: rs.sessionID, LastVersion: rs.lastVersion,
		ProfileSum: rs.profileSum, Guidance: rs.guidance, Context: rs.ctx, Workdir: rs.workdir,
		Contributions: rs.hooks.states,
	}
	unrestorable := rs.hooks.unrestorable
	rs.mu.Unlock()
	if unrestorable || rec.SessionID == "" {
		b.workspaces.dropSession(sk, "")
		return
	}
	b.workspaces.putSession(sk, rec)
}

// restoreSession binds channel session sk to the session its binding kept
// before a bridge restart, when it still fits this drive: the slot's
// workspace, the content the drive wears, and every contributor's grant
// reattached. It returns the bound session (nil = none restored, and a
// binding that no longer fits is forgotten).
func (b *Bridge) restoreSession(ctx context.Context, d *drive, key driveKey, sk sessionKey, info SessionInfo) *roomSession {
	rec, ok := b.workspaces.session(sk)
	if !ok {
		return nil
	}
	forget := func(why string) *roomSession {
		slog.Info("bnbloc bridge not restoring a kept session",
			"event", "bnbloc.bridge.session_not_restored", "room", sk.room, "channel", sk.channel, "core", sk.core, "provider", sk.provider, "session", rec.SessionID, "why", why)
		b.workspaces.dropSession(sk, rec.SessionID)
		return nil
	}
	if id, ok := b.workspaces.id(key); !ok || id != rec.WorkspaceID {
		return forget("its workspace is no longer the agent's")
	}
	if rec.ProfileSum != d.content.bindingSum() || rec.Owner != d.owner {
		return forget("the drive wears other content")
	}
	hooks, ok := b.restoreContributions(ctx, info, rec.Contributions)
	if !ok {
		if toolsChanged(rec.Contributions) {
			b.noteToolsChanged(sk)
		}
		return forget("a session contributor could not reattach")
	}
	rs := &roomSession{
		owner: rec.Owner, seat: key, provider: rec.Provider, core: rec.Core, workspaceID: rec.WorkspaceID, sessionID: rec.SessionID,
		lastVersion: rec.LastVersion, ws: b.workspaces.shared(key, rec.WorkspaceID), ctx: rec.Context,
		profileSum: rec.ProfileSum, guidance: rec.Guidance, hooks: hooks, workdir: rec.Workdir,
	}
	b.mu.Lock()
	if b.sessions[sk] != nil {
		// Another drive of the channel bound one meanwhile.
		b.mu.Unlock()
		rs.release()
		return nil
	}
	b.sessions[sk] = rs
	b.mu.Unlock()
	slog.Info("bnbloc bridge restored a kept session",
		"event", "bnbloc.bridge.session_restored", "room", sk.room, "channel", sk.channel, "core", sk.core, "provider", sk.provider, "workspace", rec.WorkspaceID, "session", rec.SessionID)
	return rs
}

// noteToolsChanged remembers that channel session sk's next session starts
// after the room tools changed (takeToolsChanged).
func (b *Bridge) noteToolsChanged(sk sessionKey) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.toolsChanged == nil {
		b.toolsChanged = map[sessionKey]bool{}
	}
	b.toolsChanged[sk] = true
}

// takeToolsChanged reports, once, that sk's new session follows a change of the
// room tools.
func (b *Bridge) takeToolsChanged(sk sessionKey) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	was := b.toolsChanged[sk]
	delete(b.toolsChanged, sk)
	return was
}
