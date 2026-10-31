// Package realtime provides an in-process per-room event fan-out hub with
// presence replay. Realtime reduces latency but is not authority: durable feed
// events (chat_message, agent_messages) carry the room's durable sequence, the
// same number history stores, so consumers dedupe live and paged rows by it.
// Transient events (presence, typing, roster, job/state deltas) carry no
// sequence (0) and never consume one.
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Subscriber receives fanned-out events for one room subscription.
type Subscriber struct {
	ch  chan wire.Event
	seq int64
	// rooms are every room the subscriber is on when it is on more than
	// one (SubscribeRooms): it leaves them all at once.
	rooms []string
	// gapped: the subscriber fell a whole buffer behind once and missed
	// events (TakeGap); a second overflow before the stream took it closes
	// the subscription.
	gapped atomic.Bool
}

// Events exposes the receive channel for a subscriber.
func (s *Subscriber) Events() <-chan wire.Event { return s.ch }

// TakeGap reports, once, that the subscriber missed events because it fell
// a whole buffer behind: its stream tells the client to fetch the
// difference (wire.EventStreamGap) instead of being closed.
func (s *Subscriber) TakeGap() bool { return s.gapped.Swap(false) }

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
	// seq numbers durable events published with no journal (in-memory
	// mode, tests), per room. With a journal the store owns the room's
	// sequence (store.ChatStore.AppendNextRoomMessages), so any replica may
	// commit to any room and none needs to know where another stopped.
	seq map[string]int64
	// queues holds each room's group commit queue (see the Hub doc).
	queues map[string]*roomQueue
	// recent holds each room's recently committed client message ids and
	// their sequences (RecentClientMsg), under mu.
	recent      map[string]*recentIDs
	replayLimit int
	buffer      int
	// now is the replay clock (time.Now; tests move it).
	now func() time.Time
	// Bus carries the hub's events to the other replicas serving the same
	// rooms and theirs to it (nil: this replica is alone). See bus.go.
	Bus Bus
	// RoomHead, with a Bus, returns the room's newest stored sequence: where
	// the room's live order starts when the hub begins receiving the room
	// from the bus (what is older the room's streams read from history).
	// Nil or failing: the first sequence that arrives starts it.
	RoomHead func(roomID string) (int64, error)
	// Received, when set, sees every event another replica published for a
	// room this hub receives, after its delivery (realtime's room state
	// drops what it changed). It must not block.
	Received func(ev wire.Event)
	// Joined, when set, is told each time the hub starts receiving a room
	// from the bus (and again after a lost subscription): realtime asks the
	// other replicas for their views of the room (RoomViews.Query).
	Joined func(roomID string)
	// busRooms are the rooms the hub receives from the bus, under mu.
	busRooms map[string]*busRoom
	// holdFor bounds how long a durable event waits for a missing earlier
	// sequence before it is delivered anyway (reorderHold), lingerFor how
	// long a room stays on the bus after its last stream (busLinger); tests
	// shorten both.
	holdFor, lingerFor time.Duration
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
		recent:      map[string]*recentIDs{},
		busRooms:    map[string]*busRoom{},
		holdFor:     reorderHold,
		lingerFor:   busLinger,
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
	sub, _ := h.SubscribeSince(roomID, 0)
	return sub
}

// SubscribeSince is Subscribe for a client that holds the room's events up
// to sequence since: the replay snapshot sends only later events (Telegram's
// difference from the client's pts). gap reports that the buffer does not
// reach back to since (the client fetches what lies between from history).
// since 0 replays the whole snapshot.
func (h *Hub) SubscribeSince(roomID string, since int64) (sub *Subscriber, gap bool) {
	h.joinBus(roomID)
	h.mu.Lock()
	defer h.mu.Unlock()
	snapshot := h.pruneReplayLocked(roomID, h.now())
	if since > 0 {
		kept := snapshot[:0:0]
		for _, e := range snapshot {
			if e.ev.Sequence > since {
				kept = append(kept, e)
			}
		}
		// Unless the buffer reaches back to since, the client may lack
		// events in between (the buffer was freed, evicted, or started
		// again when the room joined the bus): it fetches the difference. A gap that holds
		// nothing costs one history read.
		gap = len(snapshot) == 0 || snapshot[0].ev.Sequence > since+1
		snapshot = kept
	}
	return h.subscribeLocked(roomID, snapshot), gap
}

// SubscribeLive registers a subscriber for live events only. Bridge consumers
// use this so they do not drive old replayed chat messages as fresh work.
func (h *Hub) SubscribeLive(roomID string) *Subscriber {
	return h.subscribe(roomID, false)
}

func (h *Hub) subscribe(roomID string, replay bool) *Subscriber {
	if replay {
		return h.Subscribe(roomID)
	}
	h.joinBus(roomID)
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.subscribeLocked(roomID, nil)
}

// subscribeLocked registers a subscriber with snapshot queued first (h.mu
// held).
func (h *Hub) subscribeLocked(roomID string, snapshot []replayEntry) *Subscriber {
	buffer := h.buffer + len(snapshot)
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
	if _, ok := set[sub]; !ok {
		return
	}
	rooms := sub.rooms
	if rooms == nil {
		rooms = []string{roomID}
	}
	for _, r := range rooms {
		h.leaveRoomLocked(r, sub)
	}
	close(sub.ch)
}

// leaveRoomLocked takes sub off one room; the caller holds mu.
func (h *Hub) leaveRoomLocked(roomID string, sub *Subscriber) {
	set := h.subs[roomID]
	delete(set, sub)
	if len(set) == 0 {
		delete(h.subs, roomID)
		// Free the replay buffer for an empty room; it is only a latency
		// optimization for currently-connected subscribers.
		delete(h.replay, roomID)
		h.idleBusLocked(roomID)
	}
}

// SubscribeRooms registers one live subscriber (no replay) on every room of
// rooms, with one buffer: a member's activity stream across their rooms
// (and their own key, wire.MemberActivityKey). Unsubscribe with any of the
// rooms takes it off them all.
func (h *Hub) SubscribeRooms(rooms []string, buffer int) *Subscriber {
	for _, r := range rooms {
		h.joinBus(r)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	sub := &Subscriber{ch: make(chan wire.Event, max(buffer, h.buffer)), rooms: slices.Clone(rooms)}
	for _, r := range rooms {
		if h.subs[r] == nil {
			h.subs[r] = map[*Subscriber]struct{}{}
		}
		h.subs[r][sub] = struct{}{}
	}
	return sub
}

// Journal stores a room's durable events, the room feed, and numbers them:
// AppendNextRoomMessages sets each row's Seq to the room's next sequence (see
// store.ChatStore, which satisfies it). Implementations must be comparable (a
// pointer, or a struct of pointers): a batch commits only writes that name
// the same journal.
type Journal interface {
	AppendNextRoomMessages(ctx context.Context, rows []store.RoomMessage) error
}

// Durable is what a durable publish stores with its event.
type Durable struct {
	// Journal receives the row; nil stores nothing (in-memory mode, tests).
	Journal Journal
	// Row is the stored row. The publisher sets TenantID, Author, ClientMsgID
	// and Payload; the batch leader sets RoomID, ChannelID, Kind and
	// CreatedAtMs from the event, and the journal sets Seq. A non-empty
	// ClientMsgID is the publish's idempotency key.
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
// a durable kind is numbered by the hub (in-memory mode and tests).
func (h *Hub) Publish(ev wire.Event) wire.Event {
	out, err := h.PublishCommit(ev, Durable{})
	if err != nil {
		log.Printf("realtime: hub: dropped %s for room=%s: %v", ev.Kind, ev.RoomID, err)
	}
	return out
}

// PublishCommit publishes ev. A durable kind (wire.PersistedFeedKind) goes
// through the room's group commit: it is stored through d.Journal together
// with the durable publishes queued with it (one transaction), which numbers
// it with the room's next sequence, and only when that commit succeeds is it
// retained for replay and delivered — in sequence order, after every earlier
// durable event the hub committed for the room. When the commit fails
// nothing of the batch is delivered, no sequence is consumed and every
// member gets the error — so a client retry cannot observe a live event that
// history never stored.
//
// Idempotency: a publish whose d.Row.ClientMsgID repeats an earlier publish
// of the same batch, or a row already in history, stores and delivers
// nothing; it returns the original event's sequence with
// store.ErrDuplicateClientMsg.
//
// A transient kind carries no sequence and never waits on a commit: it is
// delivered immediately. The revocation kinds (member_removed, room_deleted)
// are the exception: they queue behind the room's in-flight durable events,
// so a message committed before a revocation is never delivered after it,
// and with a journal that is a Sequencer they take the room's next sequence
// (storing nothing): every replica then delivers them in order with the
// room's messages, so one stored after a revocation never reaches a stream
// the revocation ends, whichever replica stored it.
//
// A subscriber whose buffer is full never blocks the hub: its stream is
// closed (see deliver), and the client reconnects and catches up from
// history.
func (h *Hub) PublishCommit(ev wire.Event, d Durable) (wire.Event, error) {
	spec, _ := wire.LookupEventKind(ev.Kind)
	switch {
	case spec.Durable:
		return h.enqueue(&pending{ev: ev, d: d, durable: true, deliver: true})
	case spec.Revocation:
		ev.Sequence = 0
		return h.enqueue(&pending{ev: ev, d: d, deliver: true, revocation: true})
	default:
		ev.Sequence = 0
		h.deliver([]*pending{{ev: ev, out: ev, deliver: true}}, true)
		return ev, nil
	}
}

// PublishLocal delivers a transient event to this replica's streams of the
// room only, never to the bus: what each replica works out for its own
// streams (RoomViews' presence_delta and agent_roster).
func (h *Hub) PublishLocal(ev wire.Event) {
	ev.Sequence = 0
	h.deliver([]*pending{{ev: ev, out: ev, deliver: true}}, false)
}

// Announce puts a transient event on the bus only, never on this replica's
// streams (RoomViews' wire.EventRoomView). Nothing happens with no bus.
func (h *Hub) Announce(ev wire.Event) {
	if h.Bus == nil {
		return
	}
	ev.Sequence = 0
	b, err := json.Marshal(ev)
	if err != nil {
		log.Printf("realtime: hub: announce %s room=%s: %v", ev.Kind, ev.RoomID, err)
		return
	}
	ev.Encoded = b
	h.publishBus([]wire.Event{ev})
}

// CommitHistory stores ev at the room's next sequence exactly like
// PublishCommit (same group commit and failure semantics), but never
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
	// revocation: sequenced (Sequencer) but not stored; it ends its batch.
	revocation bool
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
	// A revocation ends its batch: what queued after it is numbered after
	// it (sequenceRevocation).
	for ; !first.revocation && n < len(q.queue) && len(batch) < maxBatchWrites; n++ {
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
		if next.revocation {
			n++
			break
		}
	}
	q.queue = append(q.queue[:0:0], q.queue[n:]...)
	q.mu.Unlock()

	t0 := time.Now()
	h.commit(batch)
	h.sequenceRevocation(batch)
	t1 := time.Now()
	h.deliver(batch, true)
	h.Metrics.HistogramObserve(metricFeedCommitTime, t1.Sub(t0).Seconds())
	h.Metrics.HistogramObserve(metricFeedDeliverTime, time.Since(t1).Seconds())

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

// Sequencer is a Journal that also numbers an event it stores nothing of
// (store.ChatStore.NextRoomSeq).
type Sequencer interface {
	NextRoomSeq(ctx context.Context, roomID, tenantID string) (int64, error)
}

// sequenceRevocation gives the batch's revocation (its last member, after
// its messages were numbered) the room's next sequence. Without one it is
// delivered unsequenced, as with no journal: ordered on this replica only.
func (h *Hub) sequenceRevocation(batch []*pending) {
	p := batch[len(batch)-1]
	if !p.revocation {
		return
	}
	sq, ok := p.d.Journal.(Sequencer)
	if !ok || sq == nil {
		return
	}
	seq, err := guard("sequence", func() (int64, error) {
		return sq.NextRoomSeq(context.Background(), p.ev.RoomID, p.d.Row.TenantID)
	})
	if err != nil {
		log.Printf("realtime: hub: %s for room=%s unsequenced: %v", p.ev.Kind, p.ev.RoomID, err)
		return
	}
	p.out.Sequence = seq
}

// commit stores the durable members of a batch, numbered by the journal,
// setting each member's out/err. It runs with no lock held: the room has
// exactly one leader in this hub, so its batches commit and deliver in order.
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

	journal := writes[0].d.Journal
	var rows []store.RoomMessage
	for len(writes) > 0 {
		rows = make([]store.RoomMessage, len(writes))
		for i, p := range writes {
			row := p.d.Row
			row.RoomID, row.ChannelID, row.Seq = p.out.RoomID, p.out.ChannelID, 0
			row.Kind, row.CreatedAtMs = p.out.Kind, p.out.AtUnixMs
			rows[i] = row
		}
		err := h.store(journal, rows)
		if err == nil {
			for i, p := range writes {
				p.out.Sequence = rows[i].Seq
			}
			break
		}
		// Rows whose client id is already in history answer their original
		// sequence; the rest of the batch is stored again (the failed
		// transaction consumed no sequence, so the room's stay dense).
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
		// Anything else fails every remaining member: each caller gets the
		// error, and its idempotent retry (clientMsgId) stores it once.
		fail(writes, err)
		h.Metrics.CounterInc(metricFeedCommits, obs.L("result", "error"))
		return
	}
	if len(writes) == 0 {
		return // every write was in history already
	}
	h.Metrics.CounterInc(metricFeedCommits, obs.L("result", "ok"))
	h.Metrics.CounterAdd(metricFeedCommitEvents, float64(len(writes)))
	if journal != nil && h.Committed != nil {
		h.Committed(rows)
	}
	h.mu.Lock()
	for _, r := range rows {
		if r.ClientMsgID != "" {
			h.recentLocked(roomID).add(r.ClientMsgID, r.Seq)
		}
	}
	h.mu.Unlock()
}

// recentClientIDs bounds the client ids a room remembers: a retry comes
// within seconds of its send, far fewer than this many messages later.
const recentClientIDs = 2048

// recentIDs is a room's ring of recently committed client ids.
type recentIDs struct {
	seq  map[string]int64
	ring []string
	next int
}

func (h *Hub) recentLocked(roomID string) *recentIDs {
	r := h.recent[roomID]
	if r == nil {
		r = &recentIDs{seq: map[string]int64{}, ring: make([]string, recentClientIDs)}
		h.recent[roomID] = r
	}
	return r
}

func (r *recentIDs) add(id string, seq int64) {
	if old := r.ring[r.next]; old != "" {
		delete(r.seq, old)
	}
	r.ring[r.next] = id
	r.seq[id] = seq
	r.next = (r.next + 1) % len(r.ring)
}

// RecentClientMsg answers a client message id this replica committed for the
// room lately with its sequence: a send's retry is answered from memory,
// without reading history. A miss is not proof the id is new (a restart, an
// ownership move, an old send): the batch insert still answers a stored id
// with its stored sequence (store.ClientMsgConflict).
func (h *Hub) RecentClientMsg(roomID, clientMsgID string) (int64, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.recent[roomID]
	if r == nil {
		return 0, false
	}
	seq, ok := r.seq[clientMsgID]
	return seq, ok
}

// store runs one batch insert through the journal, which numbers the rows;
// with no journal (in-memory mode) nothing is stored and the hub numbers
// them. The commit runs to its real outcome: no caller's request context (it
// carries the whole batch) and no timeout. Abandoning an insert client-side
// does not abort it server-side, so a cancelled or timed-out commit may
// still land: its callers would be told it failed and its events never
// delivered live. Waiting through a slow commit keeps every guarantee; a
// dead connection still fails, through the driver and TCP keepalive.
func (h *Hub) store(j Journal, rows []store.RoomMessage) error {
	if j == nil {
		h.mu.Lock()
		cur := h.seq[rows[0].RoomID]
		for i := range rows {
			cur++
			rows[i].Seq = cur
		}
		h.seq[rows[0].RoomID] = cur
		h.mu.Unlock()
		return nil
	}
	_, err := guard("journal", func() (struct{}, error) {
		return struct{}{}, j.AppendNextRoomMessages(context.Background(), rows)
	})
	return err
}

// deliver retains and fans out, in batch order, every member that committed
// (or needs no commit) and is meant to be delivered; own events (this hub
// published them) then go on the bus. Holding mu across the batch keeps it
// contiguous on every stream.
func (h *Hub) deliver(batch []*pending, own bool) {
	// One encoding for every stream of the room (and the replay buffer, and
	// the bus), made before the hub's lock is taken: the lock covers only
	// handing the bytes to the room's streams, so one busy room's delivery
	// holds no other room for long.
	evs := make([]wire.Event, 0, len(batch))
	for _, p := range batch {
		if p.deliver && p.err == nil && p.dupOf == nil {
			if b, err := json.Marshal(p.out); err == nil {
				p.out.Encoded = b
			}
			evs = append(evs, p.out)
		}
	}
	if len(evs) == 0 {
		return
	}
	h.mu.Lock()
	for _, ev := range evs {
		h.acceptLocked(ev)
	}
	h.mu.Unlock()
	if own {
		h.publishBus(evs)
	}
}

// fanoutLocked retains ev for replay (a replay kind) and hands it to the
// room's streams; the caller holds mu.
func (h *Hub) fanoutLocked(ev wire.Event) {
	switch {
	case ev.Kind == wire.EventRoomDeleted:
		// Nothing of a deleted room may replay into a later room that
		// reuses its id, and the revocation itself must not either.
		delete(h.replay, ev.RoomID)
		delete(h.recent, ev.RoomID)
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
			// The stream fell a whole buffer behind. Never a silent drop:
			// the first time, the subscriber is marked gapped and its
			// stream tells the client to fetch the difference from its
			// last sequence (Telegram's updatesTooLong), keeping the
			// connection; a second overflow before the stream took the
			// first ends the stream (the client reconnects and catches up
			// from history).
			if sub.gapped.CompareAndSwap(false, true) {
				h.Metrics.CounterInc(metricSSEGaps)
				continue
			}
			h.dropLocked(ev.RoomID, sub)
			h.Metrics.CounterInc(metricSSEOverflows)
		}
	}
	h.Metrics.CounterInc(metricEventsPublished, obs.L("kind", EventKindLabel(ev.Kind)))
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
