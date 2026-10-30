package realtime

import (
	"log"
	"slices"
	"time"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The hub's side of the bus (bus.go): a room is received from the bus while
// the hub has a stream of it (and busLinger after the last one), and its
// durable events, this hub's and other replicas' alike, are delivered in
// sequence order.

// reorderHold bounds how long a durable event waits for a missing earlier
// sequence (another replica's message still on its way) before it is
// delivered anyway and the room's streams are told to read the difference.
// Telegram waits 0.5 s on a gap in its pts.
const reorderHold = 300 * time.Millisecond

// busLinger is how long a room stays on the bus after its last stream here
// ended: a reconnecting client finds it still received, ordered and in the
// replay buffer.
const busLinger = 30 * time.Second

// maxHeld bounds the events a room holds for a missing sequence; past it
// they are delivered at once.
const maxHeld = 1024

// busRoom is a room the hub receives from the bus, under the hub's mu.
type busRoom struct {
	// ready is closed once the subscription is in place and the room's
	// order starts (joinBus waits on it).
	ready chan struct{}
	// synced: the order has its start (RoomHead); until then events wait
	// in early.
	synced bool
	early  []wire.Event
	cancel func()
	// last is the highest durable sequence delivered, or the room's head;
	// known reports that there is one (else the first that arrives starts
	// the order).
	last  int64
	known bool
	// held are durable events past a missing sequence, with the timer that
	// stops waiting for it.
	held  map[int64]wire.Event
	timer *time.Timer
	// idle, when set, ends the subscription (no stream of the room left).
	idle *time.Timer
}

// joinBus makes sure the hub receives roomID from the bus before a stream
// subscribes: subscribe, then read the room's head, so an event committed
// meanwhile is either in history (at or below the head, the stream reads
// it) or arrives on the bus, never neither.
func (h *Hub) joinBus(roomID string) {
	if h.Bus == nil {
		return
	}
	h.mu.Lock()
	if br := h.busRooms[roomID]; br != nil {
		if br.idle != nil {
			br.idle.Stop()
			br.idle = nil
		}
		h.mu.Unlock()
		<-br.ready
		return
	}
	br := &busRoom{ready: make(chan struct{}), held: map[int64]wire.Event{}}
	h.busRooms[roomID] = br
	h.mu.Unlock()
	defer close(br.ready)
	cancel, err := h.Bus.Subscribe(roomID,
		func(ev wire.Event) { h.receive(roomID, br, ev) },
		func() { h.busLost(roomID, br) })
	if err != nil {
		// The stream serves this replica's events only; the room's next
		// stream tries again.
		log.Printf("realtime: hub: bus subscribe room=%s: %v", roomID, err)
		h.mu.Lock()
		if h.busRooms[roomID] == br {
			delete(h.busRooms, roomID)
		}
		h.mu.Unlock()
		return
	}
	h.mu.Lock()
	br.cancel = cancel
	h.mu.Unlock()
	h.syncBus(roomID, br)
}

// syncBus starts the room's order at its stored head. The replay buffer
// goes: what it holds was not ordered with other replicas' events, so a
// stream resuming from it could skip one; the stream reads history instead.
func (h *Hub) syncBus(roomID string, br *busRoom) {
	var head int64
	known := false
	if h.RoomHead != nil {
		n, err := guard("room head", func() (int64, error) { return h.RoomHead(roomID) })
		if err != nil {
			log.Printf("realtime: hub: room head room=%s: %v (the first sequence received starts the order)", roomID, err)
		}
		head, known = n, err == nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.busRooms[roomID] != br {
		return
	}
	delete(h.replay, roomID)
	br.last, br.known, br.synced = head, known, true
	early := br.early
	br.early = nil
	for _, ev := range early {
		h.orderLocked(roomID, br, ev)
	}
	if h.Joined != nil {
		go h.Joined(roomID)
	}
}

// idleBusLocked schedules the room's leave from the bus after its last
// stream here ended; the caller holds mu.
func (h *Hub) idleBusLocked(roomID string) {
	br := h.busRooms[roomID]
	if br == nil || br.idle != nil {
		return
	}
	var t *time.Timer
	t = time.AfterFunc(h.lingerFor, func() {
		h.mu.Lock()
		if h.busRooms[roomID] != br || br.idle != t || len(h.subs[roomID]) > 0 {
			h.mu.Unlock()
			return
		}
		delete(h.busRooms, roomID)
		if br.timer != nil {
			br.timer.Stop()
		}
		cancel := br.cancel
		h.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	})
	br.idle = t
}

// acceptLocked delivers one of the hub's own events, in the room's order
// when the room is on the bus; the caller holds mu.
func (h *Hub) acceptLocked(ev wire.Event) {
	if br := h.busRooms[ev.RoomID]; br != nil && ev.Sequence > 0 {
		h.orderLocked(ev.RoomID, br, ev)
		return
	}
	h.fanoutLocked(ev)
}

// receive delivers an event another replica published.
func (h *Hub) receive(roomID string, br *busRoom, ev wire.Event) {
	h.Metrics.CounterInc(metricBusEvents, obs.L("outcome", "received"))
	h.mu.Lock()
	if h.busRooms[roomID] != br {
		h.mu.Unlock()
		return
	}
	if ev.Sequence > 0 {
		h.orderLocked(roomID, br, ev)
	} else {
		h.fanoutLocked(ev)
	}
	h.mu.Unlock()
	if h.Received != nil {
		h.Received(ev)
	}
}

// orderLocked delivers a durable event in sequence order: the next one at
// once (and whatever held after it), an earlier one never again, a later
// one held until the missing one arrives or holdFor passes. The caller
// holds mu.
func (h *Hub) orderLocked(roomID string, br *busRoom, ev wire.Event) {
	if !br.synced {
		br.early = append(br.early, ev)
		return
	}
	switch {
	case !br.known || ev.Sequence == br.last+1:
		h.fanoutLocked(ev)
		br.last, br.known = ev.Sequence, true
		for {
			next, ok := br.held[br.last+1]
			if !ok {
				break
			}
			delete(br.held, br.last+1)
			h.fanoutLocked(next)
			br.last = next.Sequence
		}
		if len(br.held) == 0 && br.timer != nil {
			br.timer.Stop()
			br.timer = nil
		}
	case ev.Sequence <= br.last:
		h.Metrics.CounterInc(metricBusEvents, obs.L("outcome", "duplicate"))
	default:
		br.held[ev.Sequence] = ev
		if len(br.held) > maxHeld {
			h.flushHeldLocked(roomID, br)
			return
		}
		if br.timer == nil {
			br.timer = time.AfterFunc(h.holdFor, func() {
				h.mu.Lock()
				defer h.mu.Unlock()
				if h.busRooms[roomID] == br {
					h.flushHeldLocked(roomID, br)
				}
			})
		}
	}
}

// flushHeldLocked stops waiting for a missing sequence: the held events are
// delivered in order, and the room's streams read the difference (the
// missing one is in history, or was never committed). The caller holds mu.
func (h *Hub) flushHeldLocked(roomID string, br *busRoom) {
	if br.timer != nil {
		br.timer.Stop()
		br.timer = nil
	}
	if len(br.held) == 0 {
		return
	}
	seqs := make([]int64, 0, len(br.held))
	for seq := range br.held {
		seqs = append(seqs, seq)
	}
	slices.Sort(seqs)
	for _, seq := range seqs {
		h.fanoutLocked(br.held[seq])
		h.Metrics.CounterInc(metricBusEvents, obs.L("outcome", "held"))
	}
	br.last, br.known = seqs[len(seqs)-1], true
	clear(br.held)
	h.gapRoomLocked(roomID)
}

// gapRoomLocked tells every stream of the room to read the difference from
// its last sequence (wire.EventStreamGap); the caller holds mu.
func (h *Hub) gapRoomLocked(roomID string) {
	for sub := range h.subs[roomID] {
		if sub.gapped.CompareAndSwap(false, true) {
			h.Metrics.CounterInc(metricSSEGaps)
		}
	}
}

// busLost handles a subscription that may have missed events: the room's
// streams read the difference, and the order starts again at the room's
// head.
func (h *Hub) busLost(roomID string, br *busRoom) {
	h.Metrics.CounterInc(metricBusEvents, obs.L("outcome", "lost"))
	h.mu.Lock()
	if h.busRooms[roomID] != br {
		h.mu.Unlock()
		return
	}
	h.gapRoomLocked(roomID)
	br.synced, br.early = false, nil
	if br.timer != nil {
		br.timer.Stop()
		br.timer = nil
	}
	clear(br.held)
	h.mu.Unlock()
	h.syncBus(roomID, br)
}

// publishBus hands the hub's own delivered events to the other replicas,
// internal kinds included (their room state drops what those change). A
// failure is counted and logged: those replicas' streams of the room read
// the difference when the next sequence shows the gap.
func (h *Hub) publishBus(evs []wire.Event) {
	if h.Bus == nil {
		return
	}
	for _, ev := range evs {
		if err := h.Bus.Publish(ev); err != nil {
			h.Metrics.CounterInc(metricBusEvents, obs.L("outcome", "publish_error"))
			log.Printf("realtime: hub: bus publish %s room=%s: %v", ev.Kind, ev.RoomID, err)
			continue
		}
		h.Metrics.CounterInc(metricBusEvents, obs.L("outcome", "published"))
	}
}
