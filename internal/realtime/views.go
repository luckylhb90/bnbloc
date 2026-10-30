package realtime

import (
	"crypto/rand"
	"encoding/hex"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// RoomViews joins the replicas' views of a room's live state into the
// room's: who is here (presence) and which agents members' bridges serve
// (the roster). Each replica tracks its own streams (Presence,
// AgentRoster) and hands each change of its view here (LocalPresence,
// LocalRoster); its streams are told what changed in the room's view (a
// presence_delta, an agent_roster), whichever replica the change came from.
//
// With a bus, a replica announces its whole view of a room
// (wire.EventRoomView) when it changes and every viewRefresh, and asks the
// others for theirs when it starts receiving the room (Query); a view not
// refreshed within viewExpiry (its replica died) is dropped. Whole views,
// not changes, so an announcement lost or late is put right by the next.
// With no bus the room's view is this replica's.
type RoomViews struct {
	origin string
	// publish puts an event on the bus (nil: no bus); deliver hands one to
	// this replica's streams only.
	publish func(wire.Event)
	deliver func(wire.Event)
	now     func() time.Time

	mu    sync.Mutex
	rooms map[string]*roomViews
}

// roomViews is one room's views by replica (origin); local is this
// replica's.
type roomViews struct {
	presence map[string]presenceView
	roster   map[string]rosterView
}

type presenceView struct {
	members []string
	at      time.Time
}

type rosterView struct {
	agents []wire.RosterAgent
	at     time.Time
}

// RoomViewPayload is a replica's announcement of its view of a room: either
// part may be absent (that part did not change); Query asks every other
// replica to announce its view of the room.
type RoomViewPayload struct {
	Origin   string              `json:"origin"`
	Presence *[]string           `json:"presence,omitempty"`
	Roster   *[]wire.RosterAgent `json:"roster,omitempty"`
	Query    bool                `json:"query,omitempty"`
}

// How often a replica announces its views again, and how long another
// replica keeps a view it did not hear again.
const (
	viewRefresh = 15 * time.Second
	viewExpiry  = 45 * time.Second
)

// NewRoomViews returns views for one replica. publish is nil with no bus.
func NewRoomViews(publish, deliver func(wire.Event)) *RoomViews {
	var id [8]byte
	_, _ = rand.Read(id[:])
	return &RoomViews{origin: hex.EncodeToString(id[:]), publish: publish, deliver: deliver,
		now: time.Now, rooms: map[string]*roomViews{}}
}

func (v *RoomViews) roomLocked(roomID string) *roomViews {
	r := v.rooms[roomID]
	if r == nil {
		r = &roomViews{presence: map[string]presenceView{}, roster: map[string]rosterView{}}
		v.rooms[roomID] = r
	}
	return r
}

// LocalPresence records who is here on this replica's streams of the room.
func (v *RoomViews) LocalPresence(roomID string, members []string) {
	members = slices.Sorted(slices.Values(members))
	v.mu.Lock()
	delta := v.setPresenceLocked(roomID, v.origin, members)
	v.mu.Unlock()
	v.deliverPresence(roomID, delta)
	v.announce(roomID, RoomViewPayload{Presence: &members})
}

// LocalRoster records the agents this replica's bridge streams of the room
// serve.
func (v *RoomViews) LocalRoster(roomID string, agents []wire.RosterAgent) {
	v.mu.Lock()
	changed, union := v.setRosterLocked(roomID, v.origin, agents)
	v.mu.Unlock()
	if changed {
		v.deliverRoster(roomID, union)
	}
	v.announce(roomID, RoomViewPayload{Roster: &agents})
}

// Remote applies another replica's announcement (a wire.EventRoomView from
// the bus); others are ignored.
func (v *RoomViews) Remote(ev wire.Event) {
	if ev.Kind != wire.EventRoomView {
		return
	}
	p, ok := wire.DecodePayload[RoomViewPayload](ev.Payload)
	if !ok || p.Origin == "" || p.Origin == v.origin {
		return
	}
	var delta presenceDelta
	rosterChanged := false
	var union []wire.RosterAgent
	v.mu.Lock()
	if p.Presence != nil {
		delta = v.setPresenceLocked(ev.RoomID, p.Origin, slices.Sorted(slices.Values(*p.Presence)))
	}
	if p.Roster != nil {
		rosterChanged, union = v.setRosterLocked(ev.RoomID, p.Origin, *p.Roster)
	}
	v.mu.Unlock()
	v.deliverPresence(ev.RoomID, delta)
	if rosterChanged {
		v.deliverRoster(ev.RoomID, union)
	}
	if p.Query {
		v.announceAll(ev.RoomID)
	}
}

// Query asks the other replicas for their views of the room (this replica
// just started receiving it) and announces its own.
func (v *RoomViews) Query(roomID string) {
	if v.publish == nil {
		return
	}
	v.mu.Lock()
	p := v.viewLocked(roomID)
	v.mu.Unlock()
	p.Query = true
	v.announce(roomID, p)
}

// Members is who is here in the room, on any replica.
func (v *RoomViews) Members(roomID string) []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.membersLocked(roomID)
}

// Snapshot is the presence_snapshot a stream writes to itself on opening.
func (v *RoomViews) Snapshot(roomID string) wire.Event {
	return wire.Event{RoomID: roomID, Kind: wire.EventPresenceSnapshot, Payload: map[string]any{"members": v.Members(roomID)}}
}

// Roster is the agents members' bridges serve in the room, on any replica.
func (v *RoomViews) Roster(roomID string) []wire.RosterAgent {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.rosterLocked(roomID)
}

// Run announces this replica's views every viewRefresh and drops the
// views of replicas not heard within viewExpiry, until stop is closed.
func (v *RoomViews) Run(stop <-chan struct{}) {
	if v.publish == nil {
		return
	}
	t := time.NewTicker(viewRefresh)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			v.refresh()
		}
	}
}

// refresh is one round of Run.
func (v *RoomViews) refresh() {
	now := v.now()
	type change struct {
		room          string
		delta         presenceDelta
		rosterChanged bool
		union         []wire.RosterAgent
		mine          RoomViewPayload
		announce      bool
	}
	var changes []change
	v.mu.Lock()
	for roomID, r := range v.rooms {
		c := change{room: roomID}
		before := v.membersLocked(roomID)
		rosterBefore := v.rosterLocked(roomID)
		for origin, pv := range r.presence {
			if origin != v.origin && now.Sub(pv.at) > viewExpiry {
				delete(r.presence, origin)
			}
		}
		for origin, rv := range r.roster {
			if origin != v.origin && now.Sub(rv.at) > viewExpiry {
				delete(r.roster, origin)
			}
		}
		c.delta = diffMembers(before, v.membersLocked(roomID))
		c.union = v.rosterLocked(roomID)
		c.rosterChanged = !slices.Equal(rosterBefore, c.union)
		_, mineP := r.presence[v.origin]
		_, mineR := r.roster[v.origin]
		if mineP || mineR {
			c.mine, c.announce = v.viewLocked(roomID), true
		}
		if len(r.presence) == 0 && len(r.roster) == 0 {
			delete(v.rooms, roomID)
		}
		changes = append(changes, c)
	}
	v.mu.Unlock()
	for _, c := range changes {
		v.deliverPresence(c.room, c.delta)
		if c.rosterChanged {
			v.deliverRoster(c.room, c.union)
		}
		if c.announce {
			v.announce(c.room, c.mine)
		}
	}
}

// presenceDelta is a change of the room's view of who is here.
type presenceDelta struct{ joined, left []string }

// setPresenceLocked sets origin's view and returns what changed in the
// room's; an empty view of this replica's is forgotten.
func (v *RoomViews) setPresenceLocked(roomID, origin string, members []string) presenceDelta {
	r := v.roomLocked(roomID)
	before := v.membersLocked(roomID)
	if len(members) == 0 {
		delete(r.presence, origin)
	} else {
		r.presence[origin] = presenceView{members: members, at: v.now()}
	}
	return diffMembers(before, v.membersLocked(roomID))
}

// setRosterLocked sets origin's view and reports whether the room's
// changed, with the room's.
func (v *RoomViews) setRosterLocked(roomID, origin string, agents []wire.RosterAgent) (bool, []wire.RosterAgent) {
	r := v.roomLocked(roomID)
	before := v.rosterLocked(roomID)
	if len(agents) == 0 {
		delete(r.roster, origin)
	} else {
		r.roster[origin] = rosterView{agents: agents, at: v.now()}
	}
	after := v.rosterLocked(roomID)
	return !slices.Equal(before, after), after
}

func (v *RoomViews) membersLocked(roomID string) []string {
	r := v.rooms[roomID]
	out := []string{}
	if r == nil {
		return out
	}
	seen := map[string]bool{}
	for _, pv := range r.presence {
		for _, m := range pv.members {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (v *RoomViews) rosterLocked(roomID string) []wire.RosterAgent {
	r := v.rooms[roomID]
	out := make([]wire.RosterAgent, 0)
	if r == nil {
		return out
	}
	seen := map[wire.RosterAgent]bool{}
	for _, rv := range r.roster {
		for _, a := range rv.agents {
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	sortRoster(out)
	return out
}

// viewLocked is this replica's whole view of the room as an announcement.
func (v *RoomViews) viewLocked(roomID string) RoomViewPayload {
	members := []string{}
	agents := []wire.RosterAgent{}
	if r := v.rooms[roomID]; r != nil {
		if pv, ok := r.presence[v.origin]; ok {
			members = pv.members
		}
		if rv, ok := r.roster[v.origin]; ok {
			agents = rv.agents
		}
	}
	return RoomViewPayload{Presence: &members, Roster: &agents}
}

func (v *RoomViews) announceAll(roomID string) {
	v.mu.Lock()
	p := v.viewLocked(roomID)
	v.mu.Unlock()
	v.announce(roomID, p)
}

func (v *RoomViews) announce(roomID string, p RoomViewPayload) {
	if v.publish == nil {
		return
	}
	p.Origin = v.origin
	v.publish(wire.Event{RoomID: roomID, Kind: wire.EventRoomView, Payload: p})
}

func (v *RoomViews) deliverPresence(roomID string, d presenceDelta) {
	if len(d.joined)+len(d.left) == 0 {
		return
	}
	v.deliver(wire.Event{RoomID: roomID, Kind: wire.EventPresenceDelta, Payload: wire.PresenceDeltaPayload{Joined: d.joined, Left: d.left}})
}

func (v *RoomViews) deliverRoster(roomID string, agents []wire.RosterAgent) {
	v.deliver(wire.Event{RoomID: roomID, Kind: wire.EventAgentRoster, Payload: map[string]any{"agents": agents}})
}

// diffMembers is what joined and left between two sorted member lists.
func diffMembers(before, after []string) presenceDelta {
	var d presenceDelta
	in := func(list []string, m string) bool { _, ok := slices.BinarySearch(list, m); return ok }
	for _, m := range after {
		if !in(before, m) {
			d.joined = append(d.joined, m)
		}
	}
	for _, m := range before {
		if !in(after, m) {
			d.left = append(d.left, m)
		}
	}
	return d
}

// sortRoster is a roster's one order: by owner, core, provider.
func sortRoster(out []wire.RosterAgent) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].OwnerUserID != out[j].OwnerUserID {
			return out[i].OwnerUserID < out[j].OwnerUserID
		}
		if out[i].Core != out[j].Core {
			return out[i].Core < out[j].Core
		}
		return out[i].Provider < out[j].Provider
	})
}
