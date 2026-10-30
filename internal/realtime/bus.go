package realtime

import (
	"sync"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Bus carries a room's events between the realtime replicas serving it
// (docs/prd/2026-10-30-replicas-without-owners.md §3). Each replica holds
// its own endpoint: what one endpoint publishes reaches every other
// endpoint subscribed to the room, never itself (a replica delivers its own
// events directly). Delivery is at most once: an endpoint that may have
// missed messages calls each subscription's lost.
//
// A nil Bus is a replica that is alone: nothing is published, nothing
// received.
type Bus interface {
	// Publish sends ev, encoded (ev.Encoded set), to the room's other
	// subscribers. Events one endpoint publishes for a room arrive in
	// publish order.
	Publish(ev wire.Event) error
	// Subscribe calls deliver with every event another endpoint publishes
	// for roomID, ev.Encoded set, one at a time, until cancel. It returns
	// once the subscription is in place: an event published after that is
	// delivered. lost reports that events may have been missed (a broken
	// connection that came back).
	Subscribe(roomID string, deliver func(ev wire.Event), lost func()) (cancel func(), err error)
}

// MemoryBus connects endpoints in one process: replicas in tests, and
// anything else that runs several hubs side by side.
type MemoryBus struct {
	mu   sync.Mutex
	subs map[string]map[*memorySub]struct{}
}

type memorySub struct {
	from    *memoryEndpoint
	deliver func(wire.Event)
	lost    func()
	// mu orders one subscription's deliveries.
	mu sync.Mutex
}

type memoryEndpoint struct{ bus *MemoryBus }

// NewMemoryBus returns an empty in-process bus.
func NewMemoryBus() *MemoryBus { return &MemoryBus{subs: map[string]map[*memorySub]struct{}{}} }

// Endpoint returns a new endpoint of the bus (one per hub).
func (b *MemoryBus) Endpoint() Bus { return &memoryEndpoint{bus: b} }

// Lose tells every subscription it may have missed events, as a broken
// connection would (tests).
func (b *MemoryBus) Lose() {
	b.mu.Lock()
	var subs []*memorySub
	for _, set := range b.subs {
		for s := range set {
			subs = append(subs, s)
		}
	}
	b.mu.Unlock()
	for _, s := range subs {
		s.lost()
	}
}

func (e *memoryEndpoint) Publish(ev wire.Event) error {
	b := e.bus
	b.mu.Lock()
	var to []*memorySub
	for s := range b.subs[ev.RoomID] {
		if s.from != e {
			to = append(to, s)
		}
	}
	b.mu.Unlock()
	for _, s := range to {
		s.mu.Lock()
		s.deliver(ev)
		s.mu.Unlock()
	}
	return nil
}

func (e *memoryEndpoint) Subscribe(roomID string, deliver func(wire.Event), lost func()) (func(), error) {
	b := e.bus
	s := &memorySub{from: e, deliver: deliver, lost: lost}
	b.mu.Lock()
	if b.subs[roomID] == nil {
		b.subs[roomID] = map[*memorySub]struct{}{}
	}
	b.subs[roomID][s] = struct{}{}
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subs[roomID], s)
		if len(b.subs[roomID]) == 0 {
			delete(b.subs, roomID)
		}
	}, nil
}
