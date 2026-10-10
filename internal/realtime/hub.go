// Package realtime provides an in-process per-room event fan-out hub with
// presence replay. Realtime reduces latency but is not authority: each event
// carries a per-connection sequence so consumers ignore older deliveries.
package realtime

import (
	"fmt"
	"log"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Subscriber receives fanned-out events for one room subscription.
type Subscriber struct {
	ch  chan wire.Event
	seq int64
}

// Events exposes the receive channel for a subscriber.
func (s *Subscriber) Events() <-chan wire.Event { return s.ch }

// Hub fans out events to per-room subscribers and retains a bounded replay
// buffer so a late subscriber reconciles recent room state on subscribe.
//
// Locking: mu guards the maps and is never held across I/O. Each room also has
// its own publish lock that serializes sequence allocation, the optional commit
// (durable persist), and delivery for that room, so events reach subscribers
// in sequence order and only after they are durable — without one room's
// database latency blocking every other room.
type Hub struct {
	mu     sync.Mutex
	subs   map[string]map[*Subscriber]struct{}
	replay map[string][]wire.Event
	// seq is a per-room monotonic sequence counter. It is independent of the
	// replay buffer length: once the buffer slides past replayLimit, buffer
	// length plateaus, so deriving the sequence from it would repeat numbers and
	// the client (which dedupes by sequence) would silently drop later events —
	// e.g. agent replies after a busy room fills the buffer never render.
	seq map[string]int64
	// roomLocks holds each room's publish lock (see the Hub doc).
	roomLocks   map[string]*sync.Mutex
	replayLimit int
	buffer      int
	// SeedSeq, when set, returns the durable max sequence for a room; the hub
	// seeds its per-room counter from it on the room's first publish so
	// sequences survive a restart. A seed error fails that publish and leaves
	// the room unseeded, so the next publish retries — the counter never
	// silently restarts at 0 (which would re-issue sequences below clients'
	// read cursors and make dedup drop new messages). Nil in pure in-memory
	// mode (dev/tests) — sequences start at 1.
	SeedSeq func(roomID string) (int64, error)
	// Metrics counts published events by kind (nil = off).
	Metrics *obs.Metrics
}

// NewHub constructs a hub. replayLimit bounds retained events per room; buffer
// is the per-subscriber channel depth.
func NewHub(replayLimit, buffer int) *Hub {
	if replayLimit < 0 {
		replayLimit = 0
	}
	if buffer < 1 {
		buffer = 1
	}
	return &Hub{
		subs:        map[string]map[*Subscriber]struct{}{},
		replay:      map[string][]wire.Event{},
		seq:         map[string]int64{},
		roomLocks:   map[string]*sync.Mutex{},
		replayLimit: replayLimit,
		buffer:      buffer,
	}
}

// Subscribe registers a subscriber for a room and returns it with the current
// replay snapshot delivered first.
func (h *Hub) Subscribe(roomID string) *Subscriber {
	return h.subscribe(roomID, true)
}

// SubscribeLive registers a subscriber for live events only. Bridge consumers
// use this so they do not drive old replayed chat messages as fresh work.
func (h *Hub) SubscribeLive(roomID string) *Subscriber {
	return h.subscribe(roomID, false)
}

func (h *Hub) subscribe(roomID string, replay bool) *Subscriber {
	h.mu.Lock()
	defer h.mu.Unlock()
	buffer := h.buffer
	if replay {
		buffer += len(h.replay[roomID])
	}
	sub := &Subscriber{ch: make(chan wire.Event, buffer)}
	if h.subs[roomID] == nil {
		h.subs[roomID] = map[*Subscriber]struct{}{}
	}
	h.subs[roomID][sub] = struct{}{}
	if replay {
		for _, ev := range h.replay[roomID] {
			sub.ch <- ev
		}
	}
	return sub
}

// Unsubscribe removes a subscriber and closes its channel.
func (h *Hub) Unsubscribe(roomID string, sub *Subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set := h.subs[roomID]; set != nil {
		if _, ok := set[sub]; ok {
			delete(set, sub)
			close(sub.ch)
		}
		if len(set) == 0 {
			delete(h.subs, roomID)
			// Free the replay buffer for an empty room; it is only a latency
			// optimization for currently-connected subscribers. The per-room seq
			// counter is retained (one int64) so monotonicity holds without a
			// store re-seed when the room next becomes active.
			delete(h.replay, roomID)
		}
	}
}

// Publish assigns the next sequence and fans the event out with no durable
// commit — for transient kinds (presence, typing, roster, job/state deltas).
// A seed failure drops the event (logged): a transient event without a valid
// sequence must not be delivered, and the room re-seeds on its next publish.
func (h *Hub) Publish(ev wire.Event) wire.Event {
	out, err := h.PublishCommit(ev, nil)
	if err != nil {
		log.Printf("realtime: hub: dropped %s for room=%s: %v", ev.Kind, ev.RoomID, err)
	}
	return out
}

// PublishCommit assigns the room's next sequence, runs commit (typically the
// durable persist) with the sequenced event, and only when commit succeeds
// retains the event for replay and delivers it to every current subscriber.
// When commit (or seeding) fails nothing is delivered, the sequence is not
// consumed, and the error is returned — so a client retry cannot observe a live
// event that history never stored, and a durable store that rejected the slot
// (counter behind the feed) is re-seeded on the next publish.
//
// A slow subscriber whose buffer is full is skipped for this event rather than
// blocking the hub; it reconciles through the authoritative store.
func (h *Hub) PublishCommit(ev wire.Event, commit func(wire.Event) error) (wire.Event, error) {
	lock := h.roomLock(ev.RoomID)
	lock.Lock()
	defer lock.Unlock()

	ev, err := h.sequence(ev, commit)
	if err != nil {
		return ev, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	switch ev.Kind {
	case wire.EventRoomDeleted:
		// Nothing of a deleted room may replay into a later room that reuses
		// its id, and the revocation itself must not either.
		delete(h.replay, ev.RoomID)
	case wire.EventMemberRemoved:
		// Live-only: replayed to a member re-added later, it would close their
		// fresh stream.
	default:
		buf := append(h.replay[ev.RoomID], ev)
		if h.replayLimit > 0 && len(buf) > h.replayLimit {
			buf = buf[len(buf)-h.replayLimit:]
		}
		h.replay[ev.RoomID] = buf
	}
	for sub := range h.subs[ev.RoomID] {
		select {
		case sub.ch <- ev:
		default:
		}
	}
	h.Metrics.CounterInc(metricEventsPublished, obs.L("kind", EventKindLabel(ev.Kind)))
	return ev, nil
}

// CommitHistory assigns the room's next sequence and runs commit (the durable
// persist) exactly like PublishCommit, but never retains the event for replay
// nor delivers it to any subscriber. It is the write path for history that is
// not live chat — a handoff import — so bridges (which drive agents from live
// chat_message events) and UI streams never see it; readers find it through
// history. Sharing the room lock and counter keeps imported rows on the same
// monotonic, durable-max-seeded sequence as published ones.
func (h *Hub) CommitHistory(ev wire.Event, commit func(wire.Event) error) (wire.Event, error) {
	if commit == nil {
		return ev, fmt.Errorf("commit history: nil commit")
	}
	lock := h.roomLock(ev.RoomID)
	lock.Lock()
	defer lock.Unlock()
	return h.sequence(ev, commit)
}

// sequence seeds (once) and advances the room's counter for ev, running commit
// with the sequenced event first; a failed commit consumes no sequence. The
// caller holds the room's publish lock.
func (h *Hub) sequence(ev wire.Event, commit func(wire.Event) error) (wire.Event, error) {
	h.mu.Lock()
	cur, seeded := h.seq[ev.RoomID]
	h.mu.Unlock()
	if !seeded && h.SeedSeq != nil {
		// Seed outside mu: the store round-trip must not stall other rooms. The
		// room lock already excludes concurrent publishers of this room.
		n, err := h.SeedSeq(ev.RoomID)
		if err != nil {
			return ev, fmt.Errorf("seed sequence: %w", err)
		}
		cur = n
	}
	ev.Sequence = cur + 1
	if commit != nil {
		if err := commit(ev); err != nil {
			if h.SeedSeq != nil {
				// Re-read the durable max next time: the store may hold rows this
				// counter does not know about.
				h.mu.Lock()
				delete(h.seq, ev.RoomID)
				h.mu.Unlock()
			}
			return ev, err
		}
	}

	h.mu.Lock()
	h.seq[ev.RoomID] = ev.Sequence
	h.mu.Unlock()
	return ev, nil
}

// roomLock returns the room's publish lock, creating it on first use.
func (h *Hub) roomLock(roomID string) *sync.Mutex {
	h.mu.Lock()
	defer h.mu.Unlock()
	l := h.roomLocks[roomID]
	if l == nil {
		l = &sync.Mutex{}
		h.roomLocks[roomID] = l
	}
	return l
}

// SubscriberCount reports the number of active subscribers for a room.
func (h *Hub) SubscriberCount(roomID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[roomID])
}
