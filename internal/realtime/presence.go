package realtime

import (
	"sort"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// leaveGrace is how long a user may have zero live subscriptions before they are
// announced as left. A reconnecting client (SSE drop + retry) re-enters within
// this window and cancels the pending leave, so a flaky connection no longer
// churns presence_join/leave. Kept short so a real departure still feels prompt.
const leaveGrace = 5 * time.Second

// Presence tracks which users are present in a room, refcounted so multiple
// devices/subscriptions for one user collapse to one presence entry. It emits
// join/leave/snapshot events through a publish callback. A departing user is
// announced only after a short grace period, so reconnects do not churn.
type Presence struct {
	mu      sync.Mutex
	rooms   map[string]map[string]int // roomID -> userID -> refcount
	pending map[string]*time.Timer    // "roomID\x00userID" -> pending leave timer
	// changes are a room's arrivals and departures not yet announced: they
	// go out together as one presence_delta a flushEvery
	// (docs/prd/2026-10-30-message-path-performance.md §4.3), so a room
	// whose members all reconnect at once (a deploy) sends each stream a
	// few deltas, not one event per member.
	changes map[string]*presenceChanges
	publish func(wire.Event)
	after   func(time.Duration, func()) *time.Timer
}

type presenceChanges struct{ joined, left map[string]bool }

// flushEvery is how often a room's presence changes are announced.
const flushEvery = time.Second

// NewPresence constructs a tracker that emits events via publish.
func NewPresence(publish func(wire.Event)) *Presence {
	return &Presence{
		rooms:   map[string]map[string]int{},
		pending: map[string]*time.Timer{},
		changes: map[string]*presenceChanges{},
		publish: publish,
		after:   time.AfterFunc,
	}
}

func presenceKey(roomID, userID string) string { return roomID + "\x00" + userID }

// Enter records a user's presence. The first live reference emits presence_join;
// a reference that arrives during a pending leave cancels it (a reconnect), so no
// churn. No snapshot is broadcast: each stream writes its own on opening
// (Members), so a room of n members costs n events per arrival, not n
// snapshots of n (a 1,024-member room's streams overflowed on the
// snapshots alone, channels PRD §2.4's load test).
func (p *Presence) Enter(roomID, userID string) {
	p.mu.Lock()
	if p.rooms[roomID] == nil {
		p.rooms[roomID] = map[string]int{}
	}
	// Cancel any pending leave for this user (reconnect within the grace window).
	key := presenceKey(roomID, userID)
	reconnecting := false
	if t := p.pending[key]; t != nil {
		t.Stop()
		delete(p.pending, key)
		reconnecting = true
	}
	first := p.rooms[roomID][userID] == 0
	p.rooms[roomID][userID]++
	p.mu.Unlock()

	// Announce join only on a genuine first arrival, not a reconnect that a
	// pending leave had not yet finalized.
	if first && !reconnecting {
		p.change(roomID, userID, true)
	}
}

// change records an arrival (joined) or a departure for the room's next
// delta; a departure cancels an arrival not yet announced, and the reverse.
func (p *Presence) change(roomID, userID string, joined bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.changes[roomID]
	if c == nil {
		c = &presenceChanges{joined: map[string]bool{}, left: map[string]bool{}}
		p.changes[roomID] = c
		p.after(flushEvery, func() { p.flush(roomID) })
	}
	add, cancel := c.joined, c.left
	if !joined {
		add, cancel = c.left, c.joined
	}
	if cancel[userID] {
		delete(cancel, userID)
		return
	}
	add[userID] = true
}

// flush announces the room's changes as one presence_delta.
func (p *Presence) flush(roomID string) {
	p.mu.Lock()
	c := p.changes[roomID]
	delete(p.changes, roomID)
	p.mu.Unlock()
	if c == nil || len(c.joined)+len(c.left) == 0 {
		return
	}
	p.emit(roomID, wire.EventPresenceDelta, wire.PresenceDeltaPayload{Joined: sortedKeys(c.joined), Left: sortedKeys(c.left)})
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Snapshot is the presence_snapshot a stream writes to itself on opening.
func (p *Presence) Snapshot(roomID string) wire.Event {
	return wire.Event{RoomID: roomID, Kind: wire.EventPresenceSnapshot, Payload: map[string]any{"members": p.Members(roomID)}}
}

// Leave drops a user's presence reference. When the last reference goes, the
// leave is deferred by leaveGrace; a re-Enter within the window cancels it.
func (p *Presence) Leave(roomID, userID string) {
	p.mu.Lock()
	set := p.rooms[roomID]
	if set == nil || set[userID] == 0 {
		p.mu.Unlock()
		return
	}
	set[userID]--
	if set[userID] > 0 {
		p.mu.Unlock()
		return // other live subscriptions remain; no leave
	}
	// Zero live references: schedule a deferred leave.
	key := presenceKey(roomID, userID)
	if t := p.pending[key]; t != nil {
		t.Stop()
	}
	p.pending[key] = p.after(leaveGrace, func() { p.finalizeLeave(roomID, userID) })
	p.mu.Unlock()
}

// finalizeLeave removes a user whose grace window elapsed with no reconnect.
func (p *Presence) finalizeLeave(roomID, userID string) {
	p.mu.Lock()
	key := presenceKey(roomID, userID)
	delete(p.pending, key)
	set := p.rooms[roomID]
	// A reconnect during the window would have incremented the refcount; only
	// finalize if the user is still at zero live references.
	if set == nil || set[userID] > 0 {
		p.mu.Unlock()
		return
	}
	delete(set, userID)
	p.mu.Unlock()

	p.change(roomID, userID, false)
}

// Members returns the current present users in a room.
func (p *Presence) Members(roomID string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.membersLocked(roomID)
}

func (p *Presence) membersLocked(roomID string) []string {
	set := p.rooms[roomID]
	out := make([]string, 0, len(set))
	for user := range set {
		out = append(out, user)
	}
	return out
}

func (p *Presence) emit(roomID, kind string, payload any) {
	if p.publish != nil {
		p.publish(wire.Event{RoomID: roomID, Kind: kind, Payload: payload})
	}
}
