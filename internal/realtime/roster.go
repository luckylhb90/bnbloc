package realtime

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// AgentRef is one agent a member's bridge serves in a room: the daemon core
// hosting it and the provider (harness) it runs.
type AgentRef struct {
	Core     string `json:"core,omitempty"`
	Provider string `json:"provider"`
	// Sandboxed: the owner's bridge runs this agent's borrowed drives in its
	// agent sandbox (shell commands in a VM space; the CLI host-confined).
	Sandboxed bool `json:"sandboxed,omitempty"`
}

// AgentRoster tracks which agents each member can actually be driven for in a
// room, advertised by that member's bridge over its live subscription. It is
// the remote-safe replacement for the single-host picker heuristic that assumed
// every member ran the same local cores: the gateway cannot reach a remote
// user's laptop to list their agents, but the user's own bridge dials OUTBOUND
// and advertises exactly what it serves.
//
// Liveness mirrors Presence: the roster is refcounted per (room, owner) so a
// bridge's reconnect (SSE drop + retry) does not churn, and an owner's agents
// are withdrawn only after a short grace window with no live subscription. A
// snapshot is emitted whenever the room's advertised set actually changes.
type AgentRoster struct {
	mu      sync.Mutex
	rooms   map[string]map[string]*rosterOwner // roomID -> ownerID -> state
	pending map[string]*time.Timer             // "roomID\x00ownerID" -> pending withdraw
	publish func(wire.Event)
	after   func(time.Duration, func()) *time.Timer
}

// rosterOwner is one owner's advertised agents in a room, refcounted by live
// subscriptions so multiple devices/reconnects collapse to one entry.
type rosterOwner struct {
	refs   int
	agents []AgentRef
}

// NewAgentRoster constructs a tracker that emits snapshots via publish.
func NewAgentRoster(publish func(wire.Event)) *AgentRoster {
	return &AgentRoster{
		rooms:   map[string]map[string]*rosterOwner{},
		pending: map[string]*time.Timer{},
		publish: publish,
		after:   time.AfterFunc,
	}
}

func rosterKey(roomID, ownerID string) string { return roomID + "\x00" + ownerID }

// Advertise records that ownerID's bridge serves agents in roomID. The first
// live reference (or one arriving during a pending withdraw) publishes the new
// snapshot; a reconnect within the grace window cancels the withdraw without
// churn. Refreshing an owner's agent set to a new value also republishes.
func (r *AgentRoster) Advertise(roomID, ownerID string, agents []AgentRef) {
	agents = normalizeAgents(agents)
	r.mu.Lock()
	if r.rooms[roomID] == nil {
		r.rooms[roomID] = map[string]*rosterOwner{}
	}
	key := rosterKey(roomID, ownerID)
	if t := r.pending[key]; t != nil {
		t.Stop()
		delete(r.pending, key)
	}
	cur := r.rooms[roomID][ownerID]
	if cur == nil {
		cur = &rosterOwner{}
		r.rooms[roomID][ownerID] = cur
	}
	changed := !sameAgents(cur.agents, agents)
	cur.refs++
	cur.agents = agents
	snapshot := r.snapshotLocked(roomID)
	r.mu.Unlock()

	// Publish only on a genuine change to the room's advertised set, so a
	// reconnect that re-advertises the same agents does not churn subscribers.
	if changed {
		r.emit(roomID, snapshot)
	}
}

// Withdraw drops one live reference for ownerID in roomID. When the last
// reference goes, the withdraw is deferred by leaveGrace; a re-Advertise within
// the window cancels it, so a flaky bridge connection never flickers the roster.
func (r *AgentRoster) Withdraw(roomID, ownerID string) {
	r.mu.Lock()
	owners := r.rooms[roomID]
	cur := owners[ownerID]
	if cur == nil || cur.refs == 0 {
		r.mu.Unlock()
		return
	}
	cur.refs--
	if cur.refs > 0 {
		r.mu.Unlock()
		return // other live subscriptions remain
	}
	key := rosterKey(roomID, ownerID)
	if t := r.pending[key]; t != nil {
		t.Stop()
	}
	r.pending[key] = r.after(leaveGrace, func() { r.finalizeWithdraw(roomID, ownerID) })
	r.mu.Unlock()
}

// finalizeWithdraw removes an owner whose grace window elapsed with no
// re-advertise, then publishes the shrunken snapshot.
func (r *AgentRoster) finalizeWithdraw(roomID, ownerID string) {
	r.mu.Lock()
	key := rosterKey(roomID, ownerID)
	delete(r.pending, key)
	owners := r.rooms[roomID]
	cur := owners[ownerID]
	// A re-advertise during the window would have incremented refs; only
	// finalize if still at zero live references.
	if cur == nil || cur.refs > 0 {
		r.mu.Unlock()
		return
	}
	delete(owners, ownerID)
	if len(owners) == 0 {
		delete(r.rooms, roomID)
	}
	snapshot := r.snapshotLocked(roomID)
	r.mu.Unlock()

	r.emit(roomID, snapshot)
}

// Snapshot returns the current advertised agents for a room, owner by owner.
func (r *AgentRoster) Snapshot(roomID string) []wire.RosterAgent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked(roomID)
}

func (r *AgentRoster) snapshotLocked(roomID string) []wire.RosterAgent {
	owners := r.rooms[roomID]
	out := make([]wire.RosterAgent, 0)
	for owner, st := range owners {
		for _, a := range st.agents {
			out = append(out, wire.RosterAgent{OwnerUserID: owner, Core: a.Core, Provider: a.Provider, Sandboxed: a.Sandboxed})
		}
	}
	// Stable order so the snapshot is deterministic (tests, dedup on the client).
	sortRoster(out)
	return out
}

func (r *AgentRoster) emit(roomID string, agents []wire.RosterAgent) {
	if r.publish != nil {
		r.publish(wire.Event{RoomID: roomID, Kind: wire.EventAgentRoster, Payload: map[string]any{"agents": agents}})
	}
}

// normalizeAgents lower-cases, trims, and de-dups an advertised agent set,
// dropping entries with no provider. It keeps the set canonical so equality
// checks (change detection) are stable regardless of client formatting.
func normalizeAgents(in []AgentRef) []AgentRef {
	seen := map[string]int{}
	out := make([]AgentRef, 0, len(in))
	for _, a := range in {
		core := strings.ToLower(strings.TrimSpace(a.Core))
		provider := strings.ToLower(strings.TrimSpace(a.Provider))
		if provider == "" {
			continue
		}
		key := core + "/" + provider
		if i, ok := seen[key]; ok {
			out[i].Sandboxed = out[i].Sandboxed || a.Sandboxed
			continue
		}
		seen[key] = len(out)
		out = append(out, AgentRef{Core: core, Provider: provider, Sandboxed: a.Sandboxed})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Core != out[j].Core {
			return out[i].Core < out[j].Core
		}
		return out[i].Provider < out[j].Provider
	})
	return out
}

// sameAgents reports whether two normalized agent sets are equal.
func sameAgents(a, b []AgentRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
