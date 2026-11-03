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
// Each subscription's advertisement is its own (Advertise returns its handle,
// Withdraw takes it): an owner's agents in a room are those of the owner's
// newest live subscription, never whichever subscription advertised last. A
// bridge re-subscribes when its list changes, so an older subscription still
// open (closing, or a retry that lands late) carries an older list; with one
// list per owner it overwrote the newer one, and nothing corrected it
// (docs/bugs/2026-11-03-room-roster-stuck-on-a-short-list.md). Liveness
// mirrors Presence: when an owner's last subscription closes their agents are
// withdrawn after a short grace window, so a reconnect (SSE drop + retry) does
// not churn. A snapshot is emitted whenever the room's advertised set changes.
type AgentRoster struct {
	mu      sync.Mutex
	rooms   map[string]map[string]*rosterOwner // roomID -> ownerID -> state
	pending map[string]*time.Timer             // "roomID\x00ownerID" -> pending withdraw
	seq     uint64                             // the last subscription's order
	publish func(wire.Event)
	after   func(time.Duration, func()) *time.Timer
}

// rosterOwner is one owner's live subscriptions in a room, by handle, and the
// list kept through a pending withdraw (the grace window).
type rosterOwner struct {
	subs    map[uint64][]AgentRef
	lingers []AgentRef
}

// RosterSub is one subscription's advertisement: the handle Withdraw takes.
type RosterSub struct {
	room, owner string
	seq         uint64
}

// agents is the owner's current list: the newest live subscription's, else
// the one kept through a pending withdraw.
func (o *rosterOwner) agents() []AgentRef {
	var newest uint64
	var out []AgentRef
	found := false
	for seq, a := range o.subs {
		if !found || seq > newest {
			newest, out, found = seq, a, true
		}
	}
	if !found {
		return o.lingers
	}
	return out
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

// Advertise records a new subscription of ownerID's bridge in roomID serving
// agents, and returns its handle. A subscription opened during a pending
// withdraw cancels it; the room's snapshot is published when its set changed.
func (r *AgentRoster) Advertise(roomID, ownerID string, agents []AgentRef) RosterSub {
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
		cur = &rosterOwner{subs: map[uint64][]AgentRef{}}
		r.rooms[roomID][ownerID] = cur
	}
	before := r.snapshotLocked(roomID)
	r.seq++
	sub := RosterSub{room: roomID, owner: ownerID, seq: r.seq}
	cur.subs[sub.seq] = agents
	cur.lingers = nil
	after := r.snapshotLocked(roomID)
	r.mu.Unlock()
	// Publish only on a genuine change to the room's advertised set, so a
	// reconnect that re-advertises the same agents does not churn subscribers.
	if !sameRoster(before, after) {
		r.emit(roomID, after)
	}
	return sub
}

// Withdraw ends one subscription. Its list stops counting at once (an older
// subscription closing never leaves its list behind); when it was the owner's
// last, their agents stay through leaveGrace and go unless a new
// subscription arrives, so a flaky bridge connection never flickers the
// roster.
func (r *AgentRoster) Withdraw(sub RosterSub) {
	r.mu.Lock()
	cur := r.rooms[sub.room][sub.owner]
	if cur == nil {
		r.mu.Unlock()
		return
	}
	if _, live := cur.subs[sub.seq]; !live {
		r.mu.Unlock()
		return
	}
	before := r.snapshotLocked(sub.room)
	last := cur.agents()
	delete(cur.subs, sub.seq)
	if len(cur.subs) == 0 {
		cur.lingers = last
		key := rosterKey(sub.room, sub.owner)
		if t := r.pending[key]; t != nil {
			t.Stop()
		}
		r.pending[key] = r.after(leaveGrace, func() { r.finalizeWithdraw(sub.room, sub.owner) })
	}
	after := r.snapshotLocked(sub.room)
	r.mu.Unlock()
	if !sameRoster(before, after) {
		r.emit(sub.room, after)
	}
}

// finalizeWithdraw removes an owner whose grace window elapsed with no new
// subscription, then publishes the shrunken snapshot.
func (r *AgentRoster) finalizeWithdraw(roomID, ownerID string) {
	r.mu.Lock()
	key := rosterKey(roomID, ownerID)
	delete(r.pending, key)
	owners := r.rooms[roomID]
	cur := owners[ownerID]
	// A subscription that arrived during the window keeps the owner.
	if cur == nil || len(cur.subs) > 0 {
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
		for _, a := range st.agents() {
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

// sameRoster reports whether two snapshots are equal.
func sameRoster(a, b []wire.RosterAgent) bool {
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
