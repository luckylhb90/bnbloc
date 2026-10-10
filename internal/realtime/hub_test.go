package realtime

import (
	"errors"
	"testing"
	"time"

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
	if _, err := h.PublishCommit(wire.Event{RoomID: "r", Kind: "chat_message"}, nil); err == nil {
		t.Fatal("publish with a failing seeder succeeded; want the seed error")
	}
	h.Publish(wire.Event{RoomID: "r", Kind: "presence_typing"}) // transient: dropped, logged
	if len(sub.Events()) != 0 {
		t.Fatal("an event was delivered without a seeded sequence")
	}
	fail = false
	ev, err := h.PublishCommit(wire.Event{RoomID: "r", Kind: "chat_message"}, nil)
	if err != nil || ev.Sequence != 42 {
		t.Fatalf("after recovery: seq=%d err=%v, want 42 (durable max 41 + 1)", ev.Sequence, err)
	}
	if calls != 3 {
		t.Fatalf("seeder calls = %d, want 3 (each failed attempt retries)", calls)
	}
}

// A failed commit must neither deliver the event nor consume its sequence, so
// a retried send cannot surface a live duplicate of a message history lacks.
func TestPublishCommitFailureDeliversNothing(t *testing.T) {
	h := NewHub(8, 8)
	sub := h.SubscribeLive("r")
	defer h.Unsubscribe("r", sub)
	if _, err := h.PublishCommit(wire.Event{RoomID: "r", Kind: "chat_message"}, func(wire.Event) error {
		return errors.New("persist failed")
	}); err == nil {
		t.Fatal("commit error not returned")
	}
	if len(sub.Events()) != 0 {
		t.Fatal("event delivered although its commit failed")
	}
	var committed int64
	ev, err := h.PublishCommit(wire.Event{RoomID: "r", Kind: "chat_message"}, func(e wire.Event) error {
		committed = e.Sequence
		return nil
	})
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
