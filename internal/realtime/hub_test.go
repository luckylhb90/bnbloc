package realtime

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Sequences must stay strictly monotonic across the replay-buffer slide. The
// old implementation derived the sequence from buffer length, so once the
// buffer exceeded replayLimit the length plateaued and sequences repeated —
// the client dedupes by sequence and would silently drop every later event
// (e.g. an agent reply in a busy room). This is the regression guard.
func TestPublishSequenceMonotonicPastReplayLimit(t *testing.T) {
	h := NewHub(4, 8) // small replay limit to force a slide quickly

	var last int64
	seen := map[int64]bool{}
	for i := 0; i < 20; i++ {
		ev := h.Publish(wire.Event{RoomID: "r", Kind: "chat_message"})
		if ev.Sequence <= last {
			t.Fatalf("event %d: sequence %d not greater than previous %d", i, ev.Sequence, last)
		}
		if seen[ev.Sequence] {
			t.Fatalf("event %d: sequence %d repeated", i, ev.Sequence)
		}
		seen[ev.Sequence] = true
		last = ev.Sequence
	}
	if last != 20 {
		t.Fatalf("final sequence = %d, want 20 (one per publish)", last)
	}
}

// A subscriber that joins after the buffer has slid still receives the retained
// tail with the same monotonic sequences (no collisions with earlier events it
// would otherwise dedupe away).
func TestSubscribeReplayCarriesMonotonicSequences(t *testing.T) {
	h := NewHub(3, 8)
	for i := 0; i < 10; i++ {
		h.Publish(wire.Event{RoomID: "r", Kind: "chat_message"})
	}
	sub := h.Subscribe("r")
	defer h.Unsubscribe("r", sub)

	// Drain the replay snapshot delivered on subscribe: the last replayLimit
	// events, with strictly increasing sequences ending at the latest publish.
	var got []int64
	for len(sub.Events()) > 0 {
		got = append(got, (<-sub.Events()).Sequence)
	}
	if len(got) != 3 {
		t.Fatalf("replay length = %d, want 3", len(got))
	}
	if got[0] != 8 || got[1] != 9 || got[2] != 10 {
		t.Fatalf("replay sequences = %v, want [8 9 10]", got)
	}
}

// A failing seeder must not silently restart the room's counter at 0: the
// publish fails, nothing is delivered, and the next publish re-seeds.
func TestPublishCommitSeedFailureSurfacesAndRetries(t *testing.T) {
	h := NewHub(8, 8)
	fail := true
	calls := 0
	h.SeedSeq = func(string) (int64, error) {
		calls++
		if fail {
			return 0, errors.New("db down")
		}
		return 41, nil
	}
	sub := h.SubscribeLive("r")
	defer h.Unsubscribe("r", sub)
	if _, err := h.PublishCommit(wire.Event{RoomID: "r", Kind: "chat_message"}, Durable{}); err == nil {
		t.Fatal("publish with a failing seeder succeeded; want the seed error")
	}
	if len(sub.Events()) != 0 {
		t.Fatal("a durable event was delivered without a seeded sequence")
	}
	// A transient event needs no seed: it carries no sequence, so a store
	// outage never drops presence or typing.
	h.Publish(wire.Event{RoomID: "r", Kind: "presence_typing"})
	if ev := <-sub.Events(); ev.Kind != "presence_typing" || ev.Sequence != 0 {
		t.Fatalf("transient delivery = %s seq=%d, want presence_typing with no sequence", ev.Kind, ev.Sequence)
	}
	if _, err := h.PublishCommit(wire.Event{RoomID: "r", Kind: "chat_message"}, Durable{}); err == nil {
		t.Fatal("second publish with a failing seeder succeeded; want the seed error")
	}
	fail = false
	ev, err := h.PublishCommit(wire.Event{RoomID: "r", Kind: "chat_message"}, Durable{})
	if err != nil || ev.Sequence != 42 {
		t.Fatalf("after recovery: seq=%d err=%v, want 42 (durable max 41 + 1)", ev.Sequence, err)
	}
	if calls != 3 {
		t.Fatalf("seeder calls = %d, want 3 (each failed durable attempt retries; transient never seeds)", calls)
	}
}

// Transient events never consume the durable sequence space: the counter
// stays equal to the durable max, so a new owner (restart or ownership move)
// that seeds from the store re-issues no number a client has already seen,
// and a durable event after a burst of presence/typing continues densely.
func TestTransientEventsDoNotConsumeSequences(t *testing.T) {
	durable := int64(0)
	h := NewHub(16, 16)
	h.SeedSeq = func(string) (int64, error) { return durable, nil }
	sub := h.SubscribeLive("r")
	defer h.Unsubscribe("r", sub)

	j := &testJournal{fn: func(rows []store.RoomMessage) error { durable = rows[len(rows)-1].Seq; return nil }}
	commit := Durable{Journal: j, Row: store.RoomMessage{TenantID: "t"}}
	if ev, err := h.PublishCommit(wire.Event{RoomID: "r", Kind: wire.KindChatMessage}, commit); err != nil || ev.Sequence != 1 {
		t.Fatalf("first chat seq=%d err=%v, want 1", ev.Sequence, err)
	}
	for _, kind := range []string{"presence_delta", "presence_snapshot", "presence_typing", "agent_roster", "agent_job_created", "agent_session_state"} {
		before := j.calls()
		ev, err := h.PublishCommit(wire.Event{RoomID: "r", Kind: kind}, commit)
		if err != nil || ev.Sequence != 0 || j.calls() != before {
			t.Fatalf("%s: seq=%d err=%v journal calls %d->%d, want an unsequenced delivery with no commit", kind, ev.Sequence, err, before, j.calls())
		}
	}
	ev, err := h.PublishCommit(wire.Event{RoomID: "r", Kind: wire.KindAgentMessages}, commit)
	if err != nil || ev.Sequence != 2 {
		t.Fatalf("agent reply after transient burst seq=%d err=%v, want 2 (dense durable space)", ev.Sequence, err)
	}

	// A new owner seeds from the durable max and continues at 3: the number
	// it issues is above every number any subscriber of the old owner saw.
	next := NewHub(16, 16)
	next.SeedSeq = func(string) (int64, error) { return durable, nil }
	ev, err = next.PublishCommit(wire.Event{RoomID: "r", Kind: wire.KindChatMessage}, commit)
	if err != nil || ev.Sequence != 3 {
		t.Fatalf("new owner first chat seq=%d err=%v, want 3", ev.Sequence, err)
	}
	var maxSeen int64
	for len(sub.Events()) > 0 {
		if s := (<-sub.Events()).Sequence; s > maxSeen {
			maxSeen = s
		}
	}
	if ev.Sequence <= maxSeen {
		t.Fatalf("new owner issued %d, not above the %d an old-owner client saw", ev.Sequence, maxSeen)
	}
}

// ReleaseRooms closes the streams of rooms the replica no longer owns and
// forgets their replay buffer and counter, so a room that moves back seeds
// afresh from the durable max; owned rooms are untouched.
func TestReleaseRoomsClosesAndForgetsUnownedRooms(t *testing.T) {
	durable := map[string]int64{"gone": 0, "kept": 0}
	h := NewHub(16, 16)
	h.SeedSeq = func(room string) (int64, error) { return durable[room], nil }
	gone := h.Subscribe("gone")
	kept := h.Subscribe("kept")
	defer h.Unsubscribe("kept", kept)
	for _, room := range []string{"gone", "kept"} {
		if _, err := h.PublishCommit(wire.Event{RoomID: room, Kind: wire.KindChatMessage}, Durable{}); err != nil {
			t.Fatal(err)
		}
	}
	if n := h.ReleaseRooms(func(room string) bool { return room == "kept" }); n != 1 {
		t.Fatalf("closed %d streams, want 1", n)
	}
	<-gone.Events() // the buffered chat
	if _, open := <-gone.Events(); open {
		t.Fatal("released room's stream still open")
	}
	h.Unsubscribe("gone", gone) // the handler's deferred unsubscribe is a no-op
	if h.SubscriberCount("kept") != 1 {
		t.Fatal("owned room's stream was closed")
	}
	// Another replica committed seq 1..5 while the room was away; moving back
	// seeds from that max instead of resuming the forgotten counter at 2.
	durable["gone"] = 5
	ev, err := h.PublishCommit(wire.Event{RoomID: "gone", Kind: wire.KindChatMessage}, Durable{})
	if err != nil || ev.Sequence != 6 {
		t.Fatalf("after move back seq=%d err=%v, want 6", ev.Sequence, err)
	}
	if late := h.Subscribe("gone"); len(late.Events()) != 1 {
		t.Fatalf("replay after move back has %d events, want only the new one", len(late.Events()))
	}
}

// A failed commit must neither deliver the event nor consume its sequence, so
// a retried send cannot surface a live duplicate of a message history lacks.
func TestPublishCommitFailureDeliversNothing(t *testing.T) {
	h := NewHub(8, 8)
	sub := h.SubscribeLive("r")
	defer h.Unsubscribe("r", sub)
	failing := &testJournal{fn: func([]store.RoomMessage) error { return errors.New("persist failed") }}
	if _, err := h.PublishCommit(wire.Event{RoomID: "r", Kind: "chat_message"}, Durable{Journal: failing, Row: store.RoomMessage{TenantID: "t"}}); err == nil {
		t.Fatal("commit error not returned")
	}
	if len(sub.Events()) != 0 {
		t.Fatal("event delivered although its commit failed")
	}
	var committed int64
	ok := &testJournal{fn: func(rows []store.RoomMessage) error { committed = rows[0].Seq; return nil }}
	ev, err := h.PublishCommit(wire.Event{RoomID: "r", Kind: "chat_message"}, Durable{Journal: ok, Row: store.RoomMessage{TenantID: "t"}})
	if err != nil || ev.Sequence != 1 || committed != 1 {
		t.Fatalf("retry seq=%d committed=%d err=%v, want 1/1/nil", ev.Sequence, committed, err)
	}
	if got := (<-sub.Events()).Sequence; got != 1 {
		t.Fatalf("delivered seq = %d, want 1", got)
	}
}

// Seeding happens outside the hub-wide mutex: a slow seeder for one room must
// not block publishes to another room.
func TestSeedDoesNotBlockOtherRooms(t *testing.T) {
	h := NewHub(8, 8)
	release := make(chan struct{})
	entered := make(chan struct{})
	h.SeedSeq = func(room string) (int64, error) {
		if room == "slow" {
			close(entered)
			<-release
		}
		return 0, nil
	}
	go h.Publish(wire.Event{RoomID: "slow", Kind: "chat_message"})
	<-entered
	done := make(chan struct{})
	go func() {
		h.Publish(wire.Event{RoomID: "fast", Kind: "chat_message"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish to another room blocked behind a slow seed")
	}
	close(release)
}

// Access-revocation events are live-only: a replay must never deliver a stale
// member_removed / room_deleted that would close a later, legitimate stream.
func TestRevocationEventsAreNotReplayed(t *testing.T) {
	h := NewHub(8, 8)
	h.Publish(wire.Event{RoomID: "r", Kind: "chat_message"})
	h.Publish(wire.Event{RoomID: "r", Kind: wire.EventMemberRemoved, Payload: wire.MemberRemovedPayload{UserID: "bob"}})
	sub := h.Subscribe("r")
	got := []string{}
	for len(sub.Events()) > 0 {
		got = append(got, (<-sub.Events()).Kind)
	}
	h.Unsubscribe("r", sub)
	if len(got) != 1 || got[0] != "chat_message" {
		t.Fatalf("replay = %v, want only the chat message", got)
	}
	keep := h.SubscribeLive("r") // keep the room's replay alive across the delete
	defer h.Unsubscribe("r", keep)
	h.Publish(wire.Event{RoomID: "r", Kind: wire.EventRoomDeleted})
	late := h.Subscribe("r")
	defer h.Unsubscribe("r", late)
	if n := len(late.Events()); n != 0 {
		t.Fatalf("replay after room_deleted has %d events, want 0", n)
	}
}

// Only replay kinds (wire.RoomEventKinds) are retained: a reconnecting
// stream is never sent a transient event again (a typing hint, a "saved"
// toast, a Files badge, a stale artifact state), which the client would
// treat as new.
func TestTransientKindsAreNotReplayed(t *testing.T) {
	h := NewHub(32, 32)
	keep := h.SubscribeLive("r")
	defer h.Unsubscribe("r", keep)
	for _, k := range wire.RoomEventKinds {
		if k.Name == wire.EventRoomDeleted {
			continue
		}
		h.Publish(wire.Event{RoomID: "r", Kind: k.Name})
	}
	late := h.Subscribe("r")
	defer h.Unsubscribe("r", late)
	var got []string
	for _, ev := range drain(late) {
		got = append(got, ev.Kind)
	}
	if len(got) != 2 || got[0] != wire.KindChatMessage || got[1] != wire.KindAgentMessages {
		t.Fatalf("replay = %v, want only the durable chat and agent messages", got)
	}
}

// The replay buffer is a latency optimization for connected clients, not a
// second history: an entry older than replayMaxAge is dropped on subscribe
// and on publish, so a quiet room whose subscriber stayed connected never
// replays a message data retention has deleted from history.
func TestReplayDropsEntriesOlderThanMaxAge(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := NewHub(16, 16)
	h.now = func() time.Time { return now }
	keep := h.SubscribeLive("r") // a connected client keeps the room's buffer alive
	defer h.Unsubscribe("r", keep)

	h.Publish(wire.Event{RoomID: "r", Kind: wire.KindChatMessage, Payload: "old"})
	now = now.Add(replayMaxAge - time.Minute)
	h.Publish(wire.Event{RoomID: "r", Kind: wire.KindChatMessage, Payload: "recent"})
	if late := h.Subscribe("r"); len(late.Events()) != 2 {
		t.Fatalf("replay within the max age has %d events, want 2", len(late.Events()))
	}

	// The room goes quiet past the first entry's age: a new subscriber gets
	// only the entry that is still young enough.
	now = now.Add(2 * time.Minute)
	late := h.Subscribe("r")
	got := drain(late)
	if len(got) != 1 || got[0].Payload != "recent" {
		t.Fatalf("replay after the max age = %v, want only the recent event", got)
	}

	// A publish prunes too, and an all-expired buffer is freed.
	now = now.Add(replayMaxAge)
	h.Publish(wire.Event{RoomID: "r", Kind: wire.KindChatMessage, Payload: "new"})
	h.mu.Lock()
	n := len(h.replay["r"])
	h.mu.Unlock()
	if n != 1 {
		t.Fatalf("buffer holds %d entries after a publish, want only the new one", n)
	}
	now = now.Add(replayMaxAge + time.Second)
	if quiet := h.Subscribe("r"); len(quiet.Events()) != 0 {
		t.Fatalf("an expired buffer replayed %d events", len(quiet.Events()))
	}
	h.mu.Lock()
	_, kept := h.replay["r"]
	h.mu.Unlock()
	if kept {
		t.Fatal("an all-expired replay buffer was not freed")
	}
}

// The replay age must stay far below the shortest message retention, or a
// retention-deleted message could replay.
func TestReplayMaxAgeBelowRetentionMinimum(t *testing.T) {
	if min := time.Duration(wire.RetentionMinDays) * 24 * time.Hour; replayMaxAge*7 > min {
		t.Fatalf("replayMaxAge %v is not far below the %v retention minimum", replayMaxAge, min)
	}
}

// A subscriber that falls a whole buffer behind is never silently skipped:
// the first time it is marked gapped (its stream tells the client to fetch
// the difference, TakeGap), and a second overflow before the stream took
// the first closes it (the client reconnects and catches up from history).
// A group commit delivers a batch as one burst, so this is load-bearing.
func TestOverflowedSubscriberIsGappedThenClosed(t *testing.T) {
	h := NewHub(16, 2)
	slow := h.SubscribeLive("r")
	fast := h.SubscribeLive("r")
	defer h.Unsubscribe("r", fast)
	for i := 0; i < 2; i++ {
		h.Publish(wire.Event{RoomID: "r", Kind: wire.KindChatMessage})
		<-fast.Events()
	}
	h.Publish(wire.Event{RoomID: "r", Kind: wire.KindChatMessage}) // slow's buffer is full: a gap
	if ev := <-fast.Events(); ev.Sequence != 3 {
		t.Fatalf("the keeping-up subscriber got seq %d, want 3", ev.Sequence)
	}
	if h.SubscriberCount("r") != 2 {
		t.Fatal("the first overflow closed the subscriber")
	}
	h.Publish(wire.Event{RoomID: "r", Kind: wire.KindChatMessage}) // again, gap not taken: closed
	<-fast.Events()
	var got []int64
	for ev := range slow.Events() { // ends: the stream was closed
		got = append(got, ev.Sequence)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("overflowed subscriber received %v before closing, want [1 2]", got)
	}
	// A subscriber whose stream takes the gap keeps its connection.
	kept := h.SubscribeLive("r")
	defer h.Unsubscribe("r", kept)
	for i := 0; i < 3; i++ {
		h.Publish(wire.Event{RoomID: "r", Kind: wire.KindChatMessage})
		<-fast.Events()
	}
	<-kept.Events()
	<-kept.Events()
	if !kept.TakeGap() || kept.TakeGap() {
		t.Fatal("the gap is reported once")
	}
	h.Publish(wire.Event{RoomID: "r", Kind: wire.KindChatMessage})
	<-fast.Events()
	if ev := <-kept.Events(); ev.Sequence != 8 {
		t.Fatalf("after taking the gap the subscriber got %d, want 8", ev.Sequence)
	}
	if n := h.SubscriberCount("r"); n != 2 {
		t.Fatalf("subscribers = %d, want the one keeping up and the gapped one", n)
	}
	h.Unsubscribe("r", slow) // the handler's deferred unsubscribe is a no-op
}

// A delivered event carries its JSON, encoded once for every stream: the same
// bytes an encoding of the event gives. A committed client id is answered
// from memory afterwards.
func TestDeliveredEventIsEncodedOnce(t *testing.T) {
	h := NewHub(4, 8)
	sub := h.Subscribe("r")
	defer h.Unsubscribe("r", sub)
	pub, err := h.PublishCommit(wire.Event{RoomID: "r", ChannelID: "c", Kind: wire.KindChatMessage, Payload: map[string]any{"text": "hi"}},
		Durable{Journal: store.NewMemoryChat(), Row: store.RoomMessage{TenantID: "t", ClientMsgID: "cm-1"}})
	if err != nil {
		t.Fatal(err)
	}
	got := <-sub.Events()
	want, _ := json.Marshal(wire.Event{Sequence: pub.Sequence, RoomID: "r", ChannelID: "c", Kind: wire.KindChatMessage, Payload: map[string]any{"text": "hi"}})
	if string(got.Encoded) != string(want) {
		t.Fatalf("encoded = %s, want %s", got.Encoded, want)
	}
	if seq, ok := h.RecentClientMsg("r", "cm-1"); !ok || seq != pub.Sequence {
		t.Fatalf("recent client id = %d %v, want %d", seq, ok, pub.Sequence)
	}
	if _, ok := h.RecentClientMsg("r", "cm-2"); ok {
		t.Fatal("an unknown client id answered")
	}
}

// A client resuming from its last sequence is sent only what is newer; a
// buffer that does not reach back to it is a gap.
func TestSubscribeSinceSendsTheDifference(t *testing.T) {
	h := NewHub(3, 16)
	keep := h.SubscribeLive("r") // a room with nobody frees its buffer
	defer h.Unsubscribe("r", keep)
	for i := 0; i < 6; i++ {
		h.Publish(wire.Event{RoomID: "r", Kind: wire.KindChatMessage})
	}
	// The buffer holds 4, 5, 6.
	sub, gap := h.SubscribeSince("r", 4)
	if gap || len(sub.Events()) != 2 || (<-sub.Events()).Sequence != 5 {
		t.Fatalf("since 4: gap %v, %d queued", gap, len(sub.Events()))
	}
	h.Unsubscribe("r", sub)
	if sub, gap = h.SubscribeSince("r", 1); !gap || len(sub.Events()) != 3 {
		t.Fatalf("since 1: gap %v, %d queued; want a gap and 4..6", gap, len(sub.Events()))
	}
	h.Unsubscribe("r", sub)
	if sub, gap = h.SubscribeSince("r", 6); gap || len(sub.Events()) != 0 {
		t.Fatalf("since 6 (up to date): gap %v, %d queued", gap, len(sub.Events()))
	}
	h.Unsubscribe("r", sub)
}

// A message another replica stored (the room's previous owner) is delivered
// once, and the room's counter moves past it.
func TestDeliverCommittedOnceAndPastTheCounter(t *testing.T) {
	h := NewHub(8, 8)
	sub := h.SubscribeLive("r")
	defer h.Unsubscribe("r", sub)
	h.Publish(wire.Event{RoomID: "r", Kind: wire.KindChatMessage}) // seq 1, delivered here
	<-sub.Events()
	if h.DeliverCommitted(wire.Event{RoomID: "r", Sequence: 1, Kind: wire.KindChatMessage}) {
		t.Fatal("a sequence this hub delivered was delivered again")
	}
	if !h.DeliverCommitted(wire.Event{RoomID: "r", Sequence: 2, Kind: wire.KindChatMessage}) || (<-sub.Events()).Sequence != 2 {
		t.Fatal("the other replica's message was not delivered")
	}
	if h.DeliverCommitted(wire.Event{RoomID: "r", Sequence: 2, Kind: wire.KindChatMessage}) {
		t.Fatal("delivered twice")
	}
	if ev := h.Publish(wire.Event{RoomID: "r", Kind: wire.KindChatMessage}); ev.Sequence != 3 {
		t.Fatalf("the next sequence = %d, want 3 (past the other replica's)", ev.Sequence)
	}
}
