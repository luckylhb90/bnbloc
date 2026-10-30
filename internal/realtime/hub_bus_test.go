package realtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// busHub is a hub on bus whose room head is chat's stored max.
func busHub(bus *MemoryBus, chat *store.MemoryChat) *Hub {
	h := NewHub(64, 64)
	h.Bus = bus.Endpoint()
	h.RoomHead = func(room string) (int64, error) { return chat.MaxRoomSeq(context.Background(), room) }
	h.holdFor, h.lingerFor = 50*time.Millisecond, time.Hour
	return h
}

// recv waits for the next event on sub.
func recv(t *testing.T, sub *Subscriber) wire.Event {
	t.Helper()
	select {
	case ev, ok := <-sub.Events():
		if !ok {
			t.Fatal("stream closed")
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}
	return wire.Event{}
}

func quiet(t *testing.T, sub *Subscriber) {
	t.Helper()
	select {
	case ev := <-sub.Events():
		t.Fatalf("unexpected %s seq=%d", ev.Kind, ev.Sequence)
	case <-time.After(80 * time.Millisecond):
	}
}

// A message one replica commits reaches another replica's streams of the
// room once, in order, encoded; its own streams get it directly and the
// bus never brings it back.
func TestBusDeliversOtherReplicasMessages(t *testing.T) {
	bus, chat := NewMemoryBus(), store.NewMemoryChat()
	a, b := busHub(bus, chat), busHub(bus, chat)
	var received []wire.Event
	var mu sync.Mutex
	b.Received = func(ev wire.Event) { mu.Lock(); received = append(received, ev); mu.Unlock() }
	j := &testJournal{inner: chat}
	onA, onB := a.SubscribeLive("r"), b.SubscribeLive("r")
	defer a.Unsubscribe("r", onA)
	defer b.Unsubscribe("r", onB)

	for i, text := range []string{"one", "two"} {
		ev, err := a.PublishCommit(chatEvent("r", text), chatDurable(j, text))
		if err != nil || ev.Sequence != int64(i+1) {
			t.Fatalf("publish %s: seq=%d err=%v", text, ev.Sequence, err)
		}
	}
	if _, err := b.PublishCommit(chatEvent("r", "three"), chatDurable(j, "three")); err != nil {
		t.Fatal(err)
	}
	a.Publish(wire.Event{RoomID: "r", Kind: "presence_typing"})
	for _, sub := range []*Subscriber{onA, onB} {
		for want := int64(1); want <= 3; want++ {
			if ev := recv(t, sub); ev.Sequence != want || len(ev.Encoded) == 0 {
				t.Fatalf("got seq %d (encoded %d bytes), want %d", ev.Sequence, len(ev.Encoded), want)
			}
		}
		if ev := recv(t, sub); ev.Kind != "presence_typing" {
			t.Fatalf("got %s, want the typing hint", ev.Kind)
		}
		quiet(t, sub)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 3 {
		t.Fatalf("Received saw %d events, want the 3 replica a published", len(received))
	}
}

// A durable event that arrives before an earlier one waits for it; one
// already delivered is not delivered again; a missing one is waited for at
// most holdFor, then the room's streams read the difference.
func TestBusOrdersHoldsAndDedupes(t *testing.T) {
	bus, chat := NewMemoryBus(), store.NewMemoryChat()
	h := busHub(bus, chat)
	h.holdFor = 400 * time.Millisecond
	sub := h.SubscribeLive("r")
	defer h.Unsubscribe("r", sub)
	other := bus.Endpoint()
	send := func(seq int64) {
		ev := wire.Event{RoomID: "r", Kind: wire.KindChatMessage, Sequence: seq, Encoded: []byte(`{}`)}
		if err := other.Publish(ev); err != nil {
			t.Fatal(err)
		}
	}
	send(2)
	quiet(t, sub) // waiting for 1 (the head is 0)
	send(1)
	if a, b := recv(t, sub), recv(t, sub); a.Sequence != 1 || b.Sequence != 2 {
		t.Fatalf("delivered %d, %d; want 1, 2", a.Sequence, b.Sequence)
	}
	send(2)
	quiet(t, sub)
	if sub.TakeGap() {
		t.Fatal("gapped without a missing sequence")
	}
	send(4) // 3 never comes
	if ev := recv(t, sub); ev.Sequence != 4 {
		t.Fatalf("after the hold delivered %d, want 4", ev.Sequence)
	}
	if !sub.TakeGap() {
		t.Fatal("a missing sequence did not tell the stream to read the difference")
	}
	send(5)
	if ev := recv(t, sub); ev.Sequence != 5 {
		t.Fatalf("then %d, want 5", ev.Sequence)
	}
}

// The room's order starts at its stored head, read after the subscription
// is in place: an event at or below the head (the stream reads it from
// history) is not delivered, one above it is, and a stream joining then is
// told to read the difference.
func TestBusJoinStartsAtTheHead(t *testing.T) {
	bus, chat := NewMemoryBus(), store.NewMemoryChat()
	ctx := tenant.NewContext(context.Background(), testTenantID)
	for i := 0; i < 3; i++ {
		if err := chat.AppendNextRoomMessages(ctx, []store.RoomMessage{{TenantID: testTenantID, RoomID: "r", Kind: wire.KindChatMessage}}); err != nil {
			t.Fatal(err)
		}
	}
	h := busHub(bus, chat)
	other := bus.Endpoint()
	head := h.RoomHead
	h.RoomHead = func(room string) (int64, error) {
		// While the head is read, replica "other" publishes 3 (stored) and 4.
		for _, seq := range []int64{3, 4} {
			_ = other.Publish(wire.Event{RoomID: room, Kind: wire.KindChatMessage, Sequence: seq, Encoded: []byte(`{}`)})
		}
		return head(room)
	}
	// Events this hub published before it received the room are not in the
	// order: the replay buffer starts again.
	h.Publish(chatEvent("r", "before"))
	sub, gap := h.SubscribeSince("r", 2)
	defer h.Unsubscribe("r", sub)
	if !gap {
		t.Fatal("a stream joining a room just received from the bus was not told to read the difference")
	}
	if ev := recv(t, sub); ev.Sequence != 4 {
		t.Fatalf("first event %d, want 4 (3 is history)", ev.Sequence)
	}
	quiet(t, sub)
}

// A subscription that may have missed events tells the room's streams to
// read the difference and starts the order again at the head.
func TestBusLostReadsTheDifference(t *testing.T) {
	bus, chat := NewMemoryBus(), store.NewMemoryChat()
	h := busHub(bus, chat)
	sub := h.SubscribeLive("r")
	defer h.Unsubscribe("r", sub)
	other := bus.Endpoint()
	_ = other.Publish(wire.Event{RoomID: "r", Kind: wire.KindChatMessage, Sequence: 1, Encoded: []byte(`{}`)})
	recv(t, sub)
	// Meanwhile 2..5 were stored and their messages lost.
	ctx := tenant.NewContext(context.Background(), testTenantID)
	for i := 0; i < 5; i++ {
		if err := chat.AppendNextRoomMessages(ctx, []store.RoomMessage{{TenantID: testTenantID, RoomID: "r", Kind: wire.KindChatMessage}}); err != nil {
			t.Fatal(err)
		}
	}
	bus.Lose()
	if !sub.TakeGap() {
		t.Fatal("a lost subscription did not tell the stream to read the difference")
	}
	_ = other.Publish(wire.Event{RoomID: "r", Kind: wire.KindChatMessage, Sequence: 6, Encoded: []byte(`{}`)})
	if ev := recv(t, sub); ev.Sequence != 6 {
		t.Fatalf("after the loss delivered %d, want 6 (no hold for 2..5)", ev.Sequence)
	}
}

// A room stays on the bus a while after its last stream: a stream back
// within it replays what arrived meanwhile; after it the room leaves.
func TestBusLingersAfterTheLastStream(t *testing.T) {
	bus, chat := NewMemoryBus(), store.NewMemoryChat()
	h := busHub(bus, chat)
	h.lingerFor = 100 * time.Millisecond
	other := bus.Endpoint()
	sub := h.Subscribe("r")
	h.Unsubscribe("r", sub)
	_ = other.Publish(wire.Event{RoomID: "r", Kind: wire.KindChatMessage, Sequence: 1, Encoded: []byte(`{}`)})
	back := h.Subscribe("r")
	if ev := recv(t, back); ev.Sequence != 1 {
		t.Fatalf("replayed %d, want 1", ev.Sequence)
	}
	h.Unsubscribe("r", back)
	deadline := time.Now().Add(2 * time.Second)
	for {
		bus.mu.Lock()
		n := len(bus.subs["r"])
		bus.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the room never left the bus")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
