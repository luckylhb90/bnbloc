// Package realtime provides an in-process per-room event fan-out hub with
// presence replay. Realtime reduces latency but is not authority: durable feed
// events (chat_message, agent_messages) carry the room's durable sequence, the
// same number history stores, so consumers dedupe live and paged rows by it.
// Transient events (presence, typing, roster, job/state deltas) carry no
// sequence (0) and never consume one.
package realtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
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
// Locking: mu guards the maps and delivery, and is never held across I/O.
// Each room has a group commit queue (PublishCommit): one leader at a time
// sequences, stores and delivers a batch of the room's durable events, so
// they reach subscribers in sequence order and only after they are durable,
// while publishes that arrive during a commit wait for the next batch instead
// of a commit each — and one room's database latency never blocks another
// room.
type Hub struct {
	mu     sync.Mutex
	subs   map[string]map[*Subscriber]struct{}
	replay map[string][]replayEntry
	// seq is a per-room monotonic sequence counter for DURABLE events only. It
	// is independent of the replay buffer length: once the buffer slides past
	// replayLimit, buffer length plateaus, so deriving the sequence from it
	// would repeat numbers and the client (which dedupes by sequence) would
	// silently drop later events — e.g. agent replies after a busy room fills
	// the buffer never render. Transient events never advance it, so the
	// counter always equals the durable max: a new owner (restart, ownership
	// move) that seeds from the store continues exactly where the old one
	// stopped and re-issues no number any client has seen.
	seq map[string]int64
	// queues holds each room's group commit queue (see the Hub doc).
	queues      map[string]*roomQueue
	replayLimit int
	buffer      int
	// now is the replay clock (time.Now; tests move it).
	now func() time.Time
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
	// Committed, when set, receives the rows of every successful group
	// commit to a journal (live publishes and history imports alike),
	// right after the commit and before delivery, on the commit leader's
	// goroutine. It must not block or fail: it is how the external search
	// index learns of new rows (search.Updater.Committed only queues), and
	// the room's next batch waits for it.
	Committed func(rows []store.RoomMessage)
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
		replay:      map[string][]replayEntry{},
		seq:         map[string]int64{},
		queues:      map[string]*roomQueue{},
		replayLimit: replayLimit,
		buffer:      buffer,
		now:         time.Now,
	}
}

// replayMaxAge bounds how long an event stays in a room's replay buffer.
// Replay is only a latency optimization: it lets a client that just
// connected render the room's recent events without waiting for history,
// and every client pages history on connect anyway, so dropping an old entry
// loses nothing. The bound matters for data retention: a quiet room that
// keeps a subscriber connected never empties its buffer, and without an age
// limit a message retention deleted from history would still replay to a
// new subscriber. One day is far below the shortest message retention
// (wire.RetentionMinDays, 31 days; TestReplayMaxAgeBelowRetentionMinimum
// pins the relation), so a retention-deleted message is never replayed.
const replayMaxAge = 24 * time.Hour

// replayEntry is one retained event with the hub time it was retained at.
// The event's own AtUnixMs is not used: a client may set it.
type replayEntry struct {
	ev wire.Event
	at time.Time
}

// pruneReplayLocked drops the room's replay entries older than replayMaxAge
// (entries are in retention order, so the old ones are a prefix). The caller
// holds mu.
func (h *Hub) pruneReplayLocked(roomID string, now time.Time) []replayEntry {
	buf := h.replay[roomID]
	cut := 0
	for cut < len(buf) && now.Sub(buf[cut].at) > replayMaxAge {
		cut++
	}
	if cut == len(buf) {
		delete(h.replay, roomID)
		return nil
	}
	if cut > 0 {
		buf = append(buf[:0:0], buf[cut:]...)
		h.replay[roomID] = buf
	}
	return buf
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
	var snapshot []replayEntry
	if replay {
		snapshot = h.pruneReplayLocked(roomID, h.now())
		buffer += len(snapshot)
	}
	sub := &Subscriber{ch: make(chan wire.Event, buffer)}
	if h.subs[roomID] == nil {
		h.subs[roomID] = map[*Subscriber]struct{}{}
	}
	h.subs[roomID][sub] = struct{}{}
	for _, e := range snapshot {
		sub.ch <- e.ev
	}
	return sub
}

// Unsubscribe removes a subscriber and closes its channel. It is a no-op for
// a subscriber the hub already dropped (released room, overflowed stream).
func (h *Hub) Unsubscribe(roomID string, sub *Subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dropLocked(roomID, sub)
}

// dropLocked removes sub from the room and closes its channel; the caller
// holds mu.
func (h *Hub) dropLocked(roomID string, sub *Subscriber) {
	set := h.subs[roomID]
	if set == nil {
		return
	}
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

// Journal stores a room's sequenced durable events: the room feed.
// store.ChatStore satisfies it. Implementations must be comparable (a pointer,
// or a struct of pointers): a batch commits only writes that name the same
// journal.
type Journal interface {
	AppendRoomMessages(ctx context.Context, rows []store.RoomMessage) error
}

// Durable is what a durable publish stores with its event.
type Durable struct {
	// Journal receives the row; nil stores nothing (in-memory mode, tests).
	Journal Journal
	// Row is the stored row. The publisher sets TenantID, Author, ClientMsgID
	// and Payload; the batch leader sets RoomID, ChannelID, Seq, Kind and
	// CreatedAtMs from the event. A non-empty ClientMsgID is the publish's
	// idempotency key.
	Row store.RoomMessage
}

// Group commit bounds. A batch is what queued while the previous commit was
// in flight — never an artificial wait — capped so one statement stays far
// below MySQL's max_allowed_packet (64 MiB by default; a row carries its
// payload twice, as JSON and as search text).
const (
	maxBatchWrites = 64
	maxBatchBytes  = 1 << 20
)

// Publish fans the event out with no durable store. A transient kind
// (presence, typing, roster, job/state deltas) is delivered with no sequence;
// a durable kind is sequenced (in-memory mode and tests). A seed failure of a
// durable kind drops the event (logged) and the room re-seeds on its next
// publish.
func (h *Hub) Publish(ev wire.Event) wire.Event {
	out, err := h.PublishCommit(ev, Durable{})
	if err != nil {
		log.Printf("realtime: hub: dropped %s for room=%s: %v", ev.Kind, ev.RoomID, err)
	}
	return out
}

// PublishCommit publishes ev. A durable kind (wire.PersistedFeedKind) goes
// through the room's group commit: it is assigned the room's next sequence,
// stored through d.Journal together with the durable publishes queued with
// it (one multi-row insert), and only when that commit succeeds is it
// retained for replay and delivered — in sequence order, after every earlier
// durable event of the room. When the commit (or seeding) fails nothing of
// the batch is delivered, no sequence is consumed, the room's counter
// re-seeds on its next publish, and every member gets the error — so a
// client retry cannot observe a live event that history never stored, and a
// durable store that rejected the slots (counter behind the feed: another
// replica briefly owned the room) is re-read.
//
// Idempotency: a publish whose d.Row.ClientMsgID repeats an earlier publish
// of the same batch, or a row already in history, stores and delivers
// nothing; it returns the original event's sequence with
// store.ErrDuplicateClientMsg.
//
// A transient kind carries no sequence, never touches the counter, needs no
// seed and never waits on a commit: it is delivered immediately. The
// revocation kinds (member_removed, room_deleted) are the exception: they
// queue behind the room's in-flight durable events, so a message committed
// before a revocation is never delivered after it.
//
// A subscriber whose buffer is full never blocks the hub: its stream is
// closed (see deliver), and the client reconnects and catches up from
// history.
func (h *Hub) PublishCommit(ev wire.Event, d Durable) (wire.Event, error) {
	switch {
	case wire.PersistedFeedKind(ev.Kind):
		return h.enqueue(&pending{ev: ev, d: d, durable: true, deliver: true})
	case ev.Kind == wire.EventMemberRemoved || ev.Kind == wire.EventRoomDeleted:
		ev.Sequence = 0
		return h.enqueue(&pending{ev: ev, deliver: true})
	default:
		ev.Sequence = 0
		h.deliver([]*pending{{ev: ev, out: ev, deliver: true}})
		return ev, nil
	}
}

// CommitHistory assigns the room's next sequence and stores ev exactly like
// PublishCommit (same group commit, counter and failure semantics), but never
// retains the event for replay nor delivers it to any subscriber. It is the
// write path for history that is not live chat — a handoff import — so
// bridges (which drive agents from live chat_message events) and UI streams
// never see it; readers find it through history.
func (h *Hub) CommitHistory(ev wire.Event, d Durable) (wire.Event, error) {
	if d.Journal == nil {
		return ev, fmt.Errorf("commit history: nil journal")
	}
	if !wire.PersistedFeedKind(ev.Kind) {
		return ev, fmt.Errorf("commit history: %q is not a durable feed kind", ev.Kind)
	}
	return h.enqueue(&pending{ev: ev, d: d, durable: true})
}

// pending is one publish waiting in its room's commit queue.
type pending struct {
	ev      wire.Event
	d       Durable
	durable bool // sequenced and stored
	deliver bool // retained for replay and delivered once committed
	// wake is closed when out/err are set, or when lead is set: this publish
	// leads the room's next batch.
	wake chan struct{}
	lead bool
	out  wire.Event
	err  error
	// dupOf is the earlier publish of the same batch this one repeats.
	dupOf *pending
}

// roomQueue is a room's group commit state: whether a leader is committing,
// and the publishes queued behind it.
type roomQueue struct {
	mu      sync.Mutex
	leading bool
	queue   []*pending
}

// enqueue runs p through its room's group commit. With no leader, p leads;
// otherwise it queues and waits until a leader either finishes it or hands it
// the lead of the next batch.
func (h *Hub) enqueue(p *pending) (wire.Event, error) {
	q := h.roomQueue(p.ev.RoomID)
	q.mu.Lock()
	if q.leading {
		p.wake = make(chan struct{})
		q.queue = append(q.queue, p)
		q.mu.Unlock()
		<-p.wake
		if !p.lead {
			return p.out, p.err
		}
	} else {
		q.leading = true
		q.mu.Unlock()
	}
	h.lead(q, p)
	return p.out, p.err
}

// lead commits one batch: first plus whatever queued behind the previous
// commit (FIFO, bounded, one journal), then hands the lead to the next queued
// publish — so a leader returns as soon as its own batch is done instead of
// serving later arrivals. Exactly one leader per room exists at a time, which
// is what orders the room's batches.
func (h *Hub) lead(q *roomQueue, first *pending) {
	q.mu.Lock()
	batch := []*pending{first}
	journal, haveJournal := first.d.Journal, first.durable
	bytes := len(first.d.Row.Payload)
	n := 0
	for ; n < len(q.queue) && len(batch) < maxBatchWrites; n++ {
		next := q.queue[n]
		if next.durable {
			if !haveJournal {
				journal, haveJournal = next.d.Journal, true
			} else if next.d.Journal != journal {
				break // nil (store nothing) is a journal of its own
			}
		}
		if bytes+len(next.d.Row.Payload) > maxBatchBytes {
			break
		}
		bytes += len(next.d.Row.Payload)
		batch = append(batch, next)
	}
	q.queue = append(q.queue[:0:0], q.queue[n:]...)
	q.mu.Unlock()

	h.commit(batch)
	h.deliver(batch)

	q.mu.Lock()
	var next *pending
	if len(q.queue) > 0 {
		next = q.queue[0]
		q.queue = q.queue[1:]
		next.lead = true
	} else {
		q.leading = false
	}
	q.mu.Unlock()
	for _, p := range batch[1:] {
		close(p.wake)
	}
	if next != nil {
		close(next.wake)
	}
}

// commit sequences and stores the durable members of a batch, setting each
// member's out/err. It runs with no lock held: the room has exactly one
// leader, so nothing else touches the room's counter meanwhile.
func (h *Hub) commit(batch []*pending) {
	roomID := batch[0].ev.RoomID
	var writes []*pending
	// In-batch idempotency, keyed like the store's unique index (room, client
	// id): a repeat answers the earlier write's result once it is known.
	byClientMsg := map[string]*pending{}
	for _, p := range batch {
		p.out = p.ev
		if !p.durable {
			continue
		}
		p.out.Sequence = 0
		if cmi := p.d.Row.ClientMsgID; cmi != "" {
			if orig := byClientMsg[cmi]; orig != nil {
				if orig.d.Row.TenantID != p.d.Row.TenantID {
					// The store would refuse it too (the id is taken in the
					// room); never answer another tenant's sequence.
					p.err = fmt.Errorf("client message id %q is taken in room %s by another tenant", cmi, roomID)
					continue
				}
				p.dupOf = orig
				continue
			}
			byClientMsg[cmi] = p
		}
		writes = append(writes, p)
	}
	defer func() {
		for _, p := range batch {
			if p.dupOf == nil {
				continue
			}
			p.out.Sequence, p.err = p.dupOf.out.Sequence, p.dupOf.err
			if p.err == nil {
				p.err = store.ErrDuplicateClientMsg
			}
		}
	}()
	if len(writes) == 0 {
		return
	}

	h.mu.Lock()
	cur, seeded := h.seq[roomID]
	h.mu.Unlock()
	if !seeded && h.SeedSeq != nil {
		// Seed outside mu: the store round-trip must not stall other rooms.
		n, err := guard("seed", func() (int64, error) { return h.SeedSeq(roomID) })
		if err != nil {
			fail(writes, fmt.Errorf("seed sequence: %w", err))
			return
		}
		cur = n
	}
	journal := writes[0].d.Journal
	var rows []store.RoomMessage
	for len(writes) > 0 {
		rows = make([]store.RoomMessage, len(writes))
		for i, p := range writes {
			p.out.Sequence = cur + int64(i) + 1
			row := p.d.Row
			row.RoomID, row.ChannelID, row.Seq = p.out.RoomID, p.out.ChannelID, p.out.Sequence
			row.Kind, row.CreatedAtMs = p.out.Kind, p.out.AtUnixMs
			rows[i] = row
		}
		err := h.store(journal, rows)
		if err == nil {
			break
		}
		// Rows whose client id is already in history answer their original
		// sequence; the rest of the batch retries at the same base, so the
		// room's sequences stay dense.
		var conflict *store.ClientMsgConflict
		if errors.As(err, &conflict) {
			var kept []*pending
			for _, p := range writes {
				if seq, ok := conflict.Seqs[p.d.Row.ClientMsgID]; ok && p.d.Row.ClientMsgID != "" {
					p.out.Sequence, p.err = seq, store.ErrDuplicateClientMsg
					continue
				}
				kept = append(kept, p)
			}
			if len(kept) < len(writes) {
				writes = kept
				continue
			}
		}
		// Anything else fails every remaining member. That includes the
		// scale-out fence (store.ErrDuplicateSeq: another replica wrote the
		// room's next slots): retrying locally would keep a stale owner
		// writing a room whose live readers are on the new owner, so each
		// caller gets the error and its idempotent retry (clientMsgId) lands
		// on the real owner. The counter re-reads the durable max next time.
		fail(writes, err)
		if h.SeedSeq != nil {
			h.mu.Lock()
			delete(h.seq, roomID)
			h.mu.Unlock()
		}
		h.Metrics.CounterInc(metricFeedCommits, obs.L("result", "error"))
		return
	}
	last := cur // every write may have been in history already
	if len(writes) > 0 {
		last = writes[len(writes)-1].out.Sequence
		h.Metrics.CounterInc(metricFeedCommits, obs.L("result", "ok"))
		h.Metrics.CounterAdd(metricFeedCommitEvents, float64(len(writes)))
		if journal != nil && h.Committed != nil {
			h.Committed(rows)
		}
	}
	h.mu.Lock()
	h.seq[roomID] = last
	h.mu.Unlock()
}

// store runs one batch insert through the journal (nil = in-memory mode,
// nothing stored). The commit runs to its real outcome: no caller's request
// context (it carries the whole batch) and no timeout. Abandoning an insert
// client-side does not abort it server-side, so a cancelled or timed-out
// commit may still land: its callers would be told it failed, its events
// never delivered live, and the re-seeded counter would then collide with
// the late rows. Waiting through a slow commit keeps every guarantee; a dead
// connection still fails, through the driver and TCP keepalive.
func (h *Hub) store(j Journal, rows []store.RoomMessage) error {
	if j == nil {
		return nil
	}
	_, err := guard("journal", func() (struct{}, error) {
		return struct{}{}, j.AppendRoomMessages(context.Background(), rows)
	})
	return err
}

// deliver retains and fans out, in batch order, every member that committed
// (or needs no commit) and is meant to be delivered. Holding mu across the
// batch keeps it contiguous on every stream.
func (h *Hub) deliver(batch []*pending) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range batch {
		if !p.deliver || p.err != nil || p.dupOf != nil {
			continue
		}
		ev := p.out
		switch {
		case ev.Kind == wire.EventRoomDeleted:
			// Nothing of a deleted room may replay into a later room that
			// reuses its id, and the revocation itself must not either.
			delete(h.replay, ev.RoomID)
		case wire.ReplayedKind(ev.Kind):
			// Only replay kinds (wire.RoomEventKinds) are retained: a
			// transient event (a typing hint, a toast, a badge, a stale
			// state, a revocation) delivered again on reconnect would look
			// new to the client.
			now := h.now()
			buf := append(h.pruneReplayLocked(ev.RoomID, now), replayEntry{ev: ev, at: now})
			if h.replayLimit > 0 && len(buf) > h.replayLimit {
				buf = buf[len(buf)-h.replayLimit:]
			}
			h.replay[ev.RoomID] = buf
		}
		for sub := range h.subs[ev.RoomID] {
			select {
			case sub.ch <- ev:
			default:
				// The stream fell a whole buffer behind. Skipping the event
				// would leave the client silently without it: nothing on the
				// stream reveals a gap, and a client pages history only when
				// its stream ends. Ending the stream sends it down that path
				// (reconnect, catch up from history) instead. A group commit
				// delivers a batch as one burst, so this must never be a
				// silent drop.
				h.dropLocked(ev.RoomID, sub)
				h.Metrics.CounterInc(metricSSEOverflows)
			}
		}
		h.Metrics.CounterInc(metricEventsPublished, obs.L("kind", EventKindLabel(ev.Kind)))
	}
}

// fail sets err on every write and clears its sequence: nothing was consumed.
func fail(writes []*pending, err error) {
	for _, p := range writes {
		p.out.Sequence, p.err = 0, err
	}
}

// guard runs a store callback and turns a panic into an error: a leader that
// died mid-commit would leave its room's queue without a leader forever.
func guard[T any](what string, fn func() (T, error)) (v T, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s panicked: %v", what, r)
		}
	}()
	return fn()
}

// ReleaseRooms drops every room owned reports false for: its subscribers'
// channels are closed (their streams end, and the clients reconnect to the
// room's new owner and catch up from history), and its replay buffer and
// sequence counter are forgotten, so a room that later moves back seeds
// afresh from the durable max instead of resuming a stale counter. It
// returns the number of subscriber streams closed. Called when the replica's
// peer set changes (scale-out ownership).
func (h *Hub) ReleaseRooms(owned func(roomID string) bool) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	closed := 0
	for roomID, set := range h.subs {
		if owned(roomID) {
			continue
		}
		for sub := range set {
			close(sub.ch)
			closed++
		}
		delete(h.subs, roomID)
	}
	for roomID := range h.replay {
		if !owned(roomID) {
			delete(h.replay, roomID)
		}
	}
	for roomID := range h.seq {
		if !owned(roomID) {
			delete(h.seq, roomID)
		}
	}
	return closed
}

// roomQueue returns the room's group commit queue, creating it on first use.
func (h *Hub) roomQueue(roomID string) *roomQueue {
	h.mu.Lock()
	defer h.mu.Unlock()
	q := h.queues[roomID]
	if q == nil {
		q = &roomQueue{}
		h.queues[roomID] = q
	}
	return q
}

// SubscriberCount reports the number of active subscribers for a room.
func (h *Hub) SubscriberCount(roomID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[roomID])
}
