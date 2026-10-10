package realtime

import (
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
	for _, kind := range []string{"presence_join", "presence_snapshot", "presence_typing", "agent_roster", "agent_job_created", "agent_session_state"} {
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
