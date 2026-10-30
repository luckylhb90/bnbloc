package redisbus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
	"github.com/redis/go-redis/v9"
)

// The bus against a real Redis. Gated on BNBLOC_TEST_REDIS_URL; each test
// uses its own room ids.

func testBus(t *testing.T) *Bus {
	t.Helper()
	url := os.Getenv("BNBLOC_TEST_REDIS_URL")
	if url == "" {
		t.Skip("BNBLOC_TEST_REDIS_URL not set")
	}
	b, err := New(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func testRoom(t *testing.T) string { return fmt.Sprintf("room-%s-%d", t.Name(), time.Now().UnixNano()) }

func encoded(roomID string, seq int64) wire.Event {
	ev := wire.Event{RoomID: roomID, Kind: wire.KindChatMessage, Sequence: seq, Payload: map[string]any{"text": "hi"}}
	ev.Encoded, _ = json.Marshal(ev)
	return ev
}

type inbox struct {
	mu   sync.Mutex
	evs  []wire.Event
	lost int
}

func (in *inbox) deliver(ev wire.Event) { in.mu.Lock(); in.evs = append(in.evs, ev); in.mu.Unlock() }
func (in *inbox) lose()                 { in.mu.Lock(); in.lost++; in.mu.Unlock() }

func (in *inbox) wait(t *testing.T, n int) []wire.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		in.mu.Lock()
		got := append([]wire.Event(nil), in.evs...)
		in.mu.Unlock()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("received %d events, want %d", len(got), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Another endpoint's events arrive in publish order, decoded and encoded;
// an endpoint never receives its own; a cancelled subscription receives
// nothing more.
func TestBusCarriesEventsInOrder(t *testing.T) {
	ta, tb := testBus(t), testBus(t)
	a, b := realtime.RoomBus(ta), realtime.RoomBus(tb)
	r := testRoom(t)
	var onA, onB inbox
	cancelA, err := a.Subscribe(r, onA.deliver, onA.lose)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelA()
	cancelB, err := b.Subscribe(r, onB.deliver, onB.lose)
	if err != nil {
		t.Fatal(err)
	}
	const n = 200
	for i := 1; i <= n; i++ {
		if err := a.Publish(encoded(r, int64(i))); err != nil {
			t.Fatal(err)
		}
	}
	got := onB.wait(t, n)
	for i, ev := range got {
		if ev.Sequence != int64(i+1) || ev.RoomID != r || len(ev.Encoded) == 0 {
			t.Fatalf("event %d: %+v", i, ev)
		}
	}
	if p, _ := wire.DecodePayload[wire.ChatMessagePayload](got[0].Payload); p.Text != "hi" {
		t.Fatalf("payload %+v", got[0].Payload)
	}
	time.Sleep(50 * time.Millisecond)
	if len(onA.evs) != 0 {
		t.Fatalf("the publisher received %d of its own events", len(onA.evs))
	}
	cancelB()
	if err := a.Publish(encoded(r, n+1)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if len(onB.wait(t, n)) != n {
		t.Fatal("a cancelled subscription received")
	}
	if _, err := b.Subscribe(r, onB.deliver, onB.lose); err != nil {
		t.Fatalf("subscribing again: %v", err)
	}
}

// A subscriber connection that breaks is subscribed again, and each room is
// told it may have missed events; delivery goes on.
func TestBusReportsLossAfterAReconnect(t *testing.T) {
	a, b := realtime.RoomBus(testBus(t)), realtime.RoomBus(testBus(t))
	r := testRoom(t)
	var onB inbox
	if _, err := b.Subscribe(r, onB.deliver, onB.lose); err != nil {
		t.Fatal(err)
	}
	opts, _ := redis.ParseURL(os.Getenv("BNBLOC_TEST_REDIS_URL"))
	admin := redis.NewClient(opts)
	defer admin.Close()
	if err := admin.ClientKillByFilter(context.Background(), "TYPE", "pubsub").Err(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		onB.mu.Lock()
		lost := onB.lost
		onB.mu.Unlock()
		if lost > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no loss reported after the connection broke")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := a.Publish(encoded(r, 1)); err != nil {
		t.Fatal(err)
	}
	if got := onB.wait(t, 1); got[0].Sequence != 1 {
		t.Fatalf("after the reconnect got %+v", got[0])
	}
}

// Two hubs on the bus, one store: a stream on either gets every message of
// the room once, in sequence order, whichever replica committed it.
func TestHubsShareARoomOverRedis(t *testing.T) {
	chat := store.NewMemoryChat()
	head := func(room string) (int64, error) { return chat.MaxRoomSeq(context.Background(), room) }
	hubs := make([]*realtime.Hub, 2)
	for i := range hubs {
		hubs[i] = realtime.NewHub(256, 1024)
		hubs[i].Bus, hubs[i].RoomHead = realtime.RoomBus(testBus(t)), head
	}
	r := testRoom(t)
	subs := []*realtime.Subscriber{hubs[0].SubscribeLive(r), hubs[1].SubscribeLive(r)}
	const each = 100
	var wg sync.WaitGroup
	for i, h := range hubs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < each; k++ {
				d := realtime.Durable{Journal: chat, Row: store.RoomMessage{TenantID: "t1", ClientMsgID: fmt.Sprintf("h%d-%d", i, k)}}
				if _, err := h.PublishCommit(wire.Event{RoomID: r, Kind: wire.KindChatMessage}, d); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	for i, sub := range subs {
		for want := int64(1); want <= 2*each; want++ {
			select {
			case ev := <-sub.Events():
				if ev.Sequence != want {
					t.Fatalf("hub %d: got seq %d, want %d", i, ev.Sequence, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("hub %d: no seq %d", i, want)
			}
		}
		if sub.TakeGap() {
			t.Fatalf("hub %d: told to read the difference with nothing missing", i)
		}
	}
}

// A claim is taken once until its time ends; the loser learns how long.
func TestClaimOncePerTTL(t *testing.T) {
	a, b := testBus(t), testBus(t)
	key := testRoom(t)
	if ok, _, err := a.Claim(key, 300*time.Millisecond); err != nil || !ok {
		t.Fatalf("first claim ok=%v err=%v", ok, err)
	}
	ok, wait, err := b.Claim(key, 300*time.Millisecond)
	if err != nil || ok || wait <= 0 || wait > 300*time.Millisecond {
		t.Fatalf("second claim ok=%v wait=%v err=%v, want refused with a wait", ok, wait, err)
	}
	time.Sleep(wait + 20*time.Millisecond)
	if ok, _, err := b.Claim(key, 300*time.Millisecond); err != nil || !ok {
		t.Fatalf("after the wait ok=%v err=%v", ok, err)
	}
}

// A key is marked while any holder's mark of it is live.
func TestMarks(t *testing.T) {
	a, b := testBus(t), testBus(t)
	key := testRoom(t)
	if ok, err := b.Marked(key); err != nil || ok {
		t.Fatalf("unset mark: %v %v", ok, err)
	}
	if err := a.Mark(key, "a", 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if ok, _ := b.Marked(key); !ok {
		t.Fatal("the mark is not seen by another endpoint")
	}
	time.Sleep(250 * time.Millisecond)
	if ok, _ := b.Marked(key); ok {
		t.Fatal("the mark outlived its time")
	}
	_ = a.Mark(key, "a", time.Minute)
	_ = b.Mark(key, "b", time.Minute)
	if err := a.Unmark(key, "a"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := b.Marked(key); !ok {
		t.Fatal("one holder's unmark removed another's mark")
	}
	_ = b.Unmark(key, "b")
	if ok, _ := a.Marked(key); ok {
		t.Fatal("unmarked by every holder, still there")
	}
}
