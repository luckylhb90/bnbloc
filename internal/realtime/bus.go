package realtime

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Topics is the transport between the realtime replicas
// (docs/prd/2026-10-30-replicas-without-owners.md §3): messages on named
// topics. Each replica holds its own endpoint: what one endpoint publishes
// reaches every other endpoint subscribed to the topic, never itself.
// Delivery is at most once: an endpoint that may have missed messages calls
// each subscription's lost. Messages one endpoint publishes on a topic
// arrive in publish order.
type Topics interface {
	Publish(topic string, msg []byte) error
	// Subscribe calls deliver with every message another endpoint publishes
	// on topic, one at a time, until cancel. It returns once the
	// subscription is in place: a message published after that is
	// delivered. lost reports that messages may have been missed (a broken
	// connection that came back). One subscription per topic and endpoint.
	Subscribe(topic string, deliver func(msg []byte), lost func()) (cancel func(), err error)
}

// Bus carries a room's events between the replicas serving it: Topics with
// a topic per room (RoomTopic) and events as their JSON. A replica delivers
// its own events directly, so it never receives them.
//
// A nil Bus is a replica that is alone: nothing is published, nothing
// received.
type Bus interface {
	// Publish sends ev, encoded (ev.Encoded set), to the room's other
	// subscribers.
	Publish(ev wire.Event) error
	// Subscribe calls deliver with every event another endpoint publishes
	// for roomID, ev.Encoded set (see Topics.Subscribe).
	Subscribe(roomID string, deliver func(ev wire.Event), lost func()) (cancel func(), err error)
}

// RoomTopic is the topic of a room's events.
func RoomTopic(roomID string) string { return "room:" + roomID }

// RoomBus is the Bus over t.
func RoomBus(t Topics) Bus { return roomBus{t} }

type roomBus struct{ t Topics }

func (b roomBus) Publish(ev wire.Event) error { return b.t.Publish(RoomTopic(ev.RoomID), ev.Encoded) }

func (b roomBus) Subscribe(roomID string, deliver func(wire.Event), lost func()) (func(), error) {
	return b.t.Subscribe(RoomTopic(roomID), func(msg []byte) {
		var ev wire.Event
		if err := json.Unmarshal(msg, &ev); err != nil {
			log.Printf("realtime: bus: room %s: undecodable event: %v", roomID, err)
			return
		}
		ev.Encoded = msg
		deliver(ev)
	}, lost)
}

// MemoryBus connects endpoints in one process: replicas in tests, and
// anything else that runs several hubs side by side.
type MemoryBus struct {
	mu   sync.Mutex
	subs map[string]map[*memorySub]struct{}
}

type memorySub struct {
	from    *memoryEndpoint
	deliver func([]byte)
	lost    func()
	// mu orders one subscription's deliveries.
	mu sync.Mutex
}

type memoryEndpoint struct{ bus *MemoryBus }

// NewMemoryBus returns an empty in-process bus.
func NewMemoryBus() *MemoryBus { return &MemoryBus{subs: map[string]map[*memorySub]struct{}{}} }

// Endpoint returns a new endpoint of the bus (one per replica).
func (b *MemoryBus) Endpoint() Topics { return &memoryEndpoint{bus: b} }

// Lose tells every subscription it may have missed messages, as a broken
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

// Subscribers counts the endpoints subscribed to topic (tests).
func (b *MemoryBus) Subscribers(topic string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs[topic])
}

func (e *memoryEndpoint) Publish(topic string, msg []byte) error {
	b := e.bus
	b.mu.Lock()
	var to []*memorySub
	for s := range b.subs[topic] {
		if s.from != e {
			to = append(to, s)
		}
	}
	b.mu.Unlock()
	for _, s := range to {
		s.mu.Lock()
		s.deliver(msg)
		s.mu.Unlock()
	}
	return nil
}

func (e *memoryEndpoint) Subscribe(topic string, deliver func([]byte), lost func()) (func(), error) {
	b := e.bus
	s := &memorySub{from: e, deliver: deliver, lost: lost}
	b.mu.Lock()
	if b.subs[topic] == nil {
		b.subs[topic] = map[*memorySub]struct{}{}
	}
	b.subs[topic][s] = struct{}{}
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subs[topic], s)
		if len(b.subs[topic]) == 0 {
			delete(b.subs, topic)
		}
	}, nil
}

// Claims is a claim on a key for a while, shared by every replica: the
// first claim wins, a later one learns how long until the key is free
// (slow mode: one post per member and channel in the gap, whichever replica
// takes it).
type Claims interface {
	Claim(key string, ttl time.Duration) (ok bool, wait time.Duration, err error)
}

// MemoryClaims are Claims in one process (tests, and replicas sharing one
// process); now is the clock (time.Now when nil).
type MemoryClaims struct {
	Now func() time.Time
	mu  sync.Mutex
	end map[string]time.Time
}

// Claim takes key for ttl unless it is taken.
func (c *MemoryClaims) Claim(key string, ttl time.Duration) (bool, time.Duration, error) {
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if end, taken := c.end[key]; taken && now.Before(end) {
		return false, end.Sub(now), nil
	}
	if c.end == nil {
		c.end = map[string]time.Time{}
	}
	for k, end := range c.end {
		if !now.Before(end) {
			delete(c.end, k)
		}
	}
	c.end[key] = now.Add(ttl)
	return true, 0, nil
}

// Marks are notes the replicas share, each held by a replica (its holder)
// for a while and renewed while it holds what it notes: a member's desktop
// stream, whichever replica it reached.
type Marks interface {
	Mark(key, holder string, ttl time.Duration) error
	Unmark(key, holder string) error
	// Marked reports whether any holder's mark of key is live.
	Marked(key string) (bool, error)
}

// MemoryMarks are Marks in one process; now is the clock (time.Now when
// nil).
type MemoryMarks struct {
	Now  func() time.Time
	mu   sync.Mutex
	held map[string]map[string]time.Time
}

func (m *MemoryMarks) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *MemoryMarks) Mark(key, holder string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.held == nil {
		m.held = map[string]map[string]time.Time{}
	}
	if m.held[key] == nil {
		m.held[key] = map[string]time.Time{}
	}
	m.held[key][holder] = m.now().Add(ttl)
	return nil
}

func (m *MemoryMarks) Unmark(key, holder string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.held[key], holder)
	if len(m.held[key]) == 0 {
		delete(m.held, key)
	}
	return nil
}

func (m *MemoryMarks) Marked(key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	for _, end := range m.held[key] {
		if now.Before(end) {
			return true, nil
		}
	}
	return false, nil
}
