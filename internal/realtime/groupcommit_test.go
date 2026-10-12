package realtime

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// testJournal records every batch the hub commits. fn, when set, runs first
// and may block or fail the batch; inner, when set, then stores it for real.
type testJournal struct {
	mu      sync.Mutex
	fn      func(rows []store.RoomMessage) error
	inner   Journal
	n       int
	batches [][]store.RoomMessage
}

func (j *testJournal) AppendRoomMessages(ctx context.Context, rows []store.RoomMessage) error {
	j.mu.Lock()
	fn := j.fn
	j.n++
	j.mu.Unlock()
	var err error
	if fn != nil {
		err = fn(rows)
	}
	if err == nil && j.inner != nil {
		err = j.inner.AppendRoomMessages(ctx, rows)
	}
	if err == nil {
		j.mu.Lock()
		j.batches = append(j.batches, append([]store.RoomMessage(nil), rows...))
		j.mu.Unlock()
	}
	return err
}

func (j *testJournal) calls() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.n
}

func (j *testJournal) committed() [][]store.RoomMessage {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([][]store.RoomMessage(nil), j.batches...)
}

// gate blocks the journal's first commit until release is closed, so a test
// can queue publishes behind an in-flight commit. entered is closed when the
// first commit starts.
type gate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGate() *gate { return &gate{entered: make(chan struct{}), release: make(chan struct{})} }

func (g *gate) wait() {
	first := false
	g.once.Do(func() { first = true; close(g.entered) })
	if first {
		<-g.release
	}
}

const testTenantID = "t1"

func chatDurable(j Journal, clientMsgID string) Durable {
	return Durable{Journal: j, Row: store.RoomMessage{TenantID: testTenantID, Author: "alice", ClientMsgID: clientMsgID, Payload: []byte(`{}`)}}
}

func chatEvent(room, text string) wire.Event {
	return wire.Event{RoomID: room, Kind: wire.KindChatMessage, Payload: map[string]any{"text": text}}
}

// waitQueued waits until n publishes are queued behind the room's leader.
func waitQueued(t *testing.T, h *Hub, room string, n int) {
	t.Helper()
	q := h.roomQueue(room)
	deadline := time.Now().Add(5 * time.Second)
	for {
		q.mu.Lock()
		got := len(q.queue)
		q.mu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("queued %d publishes, want %d", got, n)
		}
		time.Sleep(time.Millisecond)
	}
}

type result struct {
	ev  wire.Event
	err error
}

// publishAsync starts a publish and returns its result channel.
func publishAsync(h *Hub, ev wire.Event, d Durable) <-chan result {
	out := make(chan result, 1)
	go func() {
		e, err := h.PublishCommit(ev, d)
		out <- result{e, err}
	}()
	return out
}

func drain(sub *Subscriber) []wire.Event {
	var out []wire.Event
	for len(sub.Events()) > 0 {
		out = append(out, <-sub.Events())
	}
	return out
}

// Publishes that queue while a commit is in flight are committed together in
// ONE journal call with consecutive sequences, delivered in order, and each
// caller gets its own event back.
func TestGroupCommitBatchesQueuedPublishes(t *testing.T) {
	g := newGate()
	j := &testJournal{fn: func([]store.RoomMessage) error { g.wait(); return nil }}
	h := NewHub(64, 64)
	sub := h.SubscribeLive("r")
	defer h.Unsubscribe("r", sub)

	first := publishAsync(h, chatEvent("r", "m0"), chatDurable(j, "c0"))
	<-g.entered
	const queued = 5
	var rest []<-chan result
	for i := 1; i <= queued; i++ {
		rest = append(rest, publishAsync(h, chatEvent("r", fmt.Sprintf("m%d", i)), chatDurable(j, fmt.Sprintf("c%d", i))))
		waitQueued(t, h, "r", i) // fixes the queue order: m1, m2, ...
	}
	if got := drain(sub); len(got) != 0 {
		t.Fatalf("delivered %d events before their commit finished", len(got))
	}
	close(g.release)
	if r := <-first; r.err != nil || r.ev.Sequence != 1 {
		t.Fatalf("first publish seq=%d err=%v, want 1", r.ev.Sequence, r.err)
	}
	for i, ch := range rest {
		r := <-ch
		if r.err != nil || r.ev.Sequence != int64(i+2) {
			t.Fatalf("queued publish %d seq=%d err=%v, want %d", i+1, r.ev.Sequence, r.err, i+2)
		}
		if text := r.ev.Payload.(map[string]any)["text"]; text != fmt.Sprintf("m%d", i+1) {
			t.Fatalf("queued publish %d got the event of %v", i+1, text)
		}
	}
	batches := j.committed()
	if len(batches) != 2 || len(batches[0]) != 1 || len(batches[1]) != queued {
		t.Fatalf("batches %d (sizes %v), want [1 %d]", len(batches), batchSizes(batches), queued)
	}
	for i, row := range batches[1] {
		if row.Seq != int64(i+2) || row.ClientMsgID != fmt.Sprintf("c%d", i+1) || row.RoomID != "r" || row.Kind != wire.KindChatMessage || row.TenantID != testTenantID {
			t.Fatalf("row %d = %+v, want seq %d of c%d", i, row, i+2, i+1)
		}
	}
	got := drain(sub)
	if len(got) != queued+1 {
		t.Fatalf("delivered %d, want %d", len(got), queued+1)
	}
	for i, ev := range got {
		if ev.Sequence != int64(i+1) {
			t.Fatalf("delivery %d has seq %d, want %d (sequence order)", i, ev.Sequence, i+1)
		}
	}
}

func batchSizes(b [][]store.RoomMessage) []int {
	out := make([]int, len(b))
	for i := range b {
		out[i] = len(b[i])
	}
	return out
}

// A batch commit that fails fails every member of the batch: nothing is
// delivered, no sequence is consumed, the counter re-seeds from the durable
// max, and each caller's retry succeeds.
func TestGroupCommitFailureFailsWholeBatch(t *testing.T) {
	g := newGate()
	var mu sync.Mutex
	commits := 0
	durableMax := int64(0)
	seeds := 0
	j := &testJournal{fn: func(rows []store.RoomMessage) error {
		g.wait()
		mu.Lock()
		defer mu.Unlock()
		if commits++; commits == 2 { // the queued batch's commit fails
			return errors.New("disk full")
		}
		durableMax = rows[len(rows)-1].Seq
		return nil
	}}
	h := NewHub(64, 64)
	h.SeedSeq = func(string) (int64, error) {
		mu.Lock()
		defer mu.Unlock()
		seeds++
		return durableMax, nil
	}
	sub := h.SubscribeLive("r")
	defer h.Unsubscribe("r", sub)

	first := publishAsync(h, chatEvent("r", "ok"), chatDurable(j, "ok"))
	<-g.entered
	var batch []<-chan result
	for i := 0; i < 4; i++ {
		batch = append(batch, publishAsync(h, chatEvent("r", fmt.Sprintf("b%d", i)), chatDurable(j, fmt.Sprintf("b%d", i))))
		waitQueued(t, h, "r", i+1)
	}
	close(g.release)
	if r := <-first; r.err != nil || r.ev.Sequence != 1 {
		t.Fatalf("first seq=%d err=%v, want 1", r.ev.Sequence, r.err)
	}
	for i, ch := range batch {
		if r := <-ch; r.err == nil || r.ev.Sequence != 0 {
			t.Fatalf("member %d of the failed batch: seq=%d err=%v, want an error and no sequence", i, r.ev.Sequence, r.err)
		}
	}
	if got := drain(sub); len(got) != 1 || got[0].Sequence != 1 {
		t.Fatalf("delivered %v, want only seq 1", got)
	}
	// Each caller retries (idempotent by client id): sequences continue at 2,
	// re-seeded from the durable max.
	for i := 0; i < 4; i++ {
		ev, err := h.PublishCommit(chatEvent("r", fmt.Sprintf("b%d", i)), chatDurable(j, fmt.Sprintf("b%d", i)))
		if err != nil || ev.Sequence != int64(i+2) {
			t.Fatalf("retry %d seq=%d err=%v, want %d", i, ev.Sequence, err, i+2)
		}
	}
	if seeds != 2 {
		t.Fatalf("seeds = %d, want 2 (first publish, and once after the failed commit)", seeds)
	}
}

// Two publishes with one client id in the same batch store and deliver one
// message; the repeat answers the original sequence with
// ErrDuplicateClientMsg.
func TestGroupCommitDuplicateClientMsgInBatch(t *testing.T) {
	g := newGate()
	j := &testJournal{fn: func([]store.RoomMessage) error { g.wait(); return nil }}
	h := NewHub(64, 64)
	sub := h.SubscribeLive("r")
	defer h.Unsubscribe("r", sub)

	first := publishAsync(h, chatEvent("r", "x"), chatDurable(j, "x"))
	<-g.entered
	a := publishAsync(h, chatEvent("r", "dup"), chatDurable(j, "dup"))
	waitQueued(t, h, "r", 1)
	b := publishAsync(h, chatEvent("r", "dup again"), chatDurable(j, "dup"))
	waitQueued(t, h, "r", 2)
	c := publishAsync(h, chatEvent("r", "other"), chatDurable(j, "other"))
	waitQueued(t, h, "r", 3)
	close(g.release)
	<-first
	ra, rb, rc := <-a, <-b, <-c
	if ra.err != nil || ra.ev.Sequence != 2 {
		t.Fatalf("original seq=%d err=%v, want 2", ra.ev.Sequence, ra.err)
	}
	if !errors.Is(rb.err, store.ErrDuplicateClientMsg) || rb.ev.Sequence != 2 {
		t.Fatalf("repeat seq=%d err=%v, want the original's 2 with ErrDuplicateClientMsg", rb.ev.Sequence, rb.err)
	}
	if rc.err != nil || rc.ev.Sequence != 3 {
		t.Fatalf("next seq=%d err=%v, want 3 (the repeat consumed none)", rc.ev.Sequence, rc.err)
	}
	if batches := j.committed(); len(batches) != 2 || len(batches[1]) != 2 {
		t.Fatalf("batches %v, want [1 2]", batchSizes(batches))
	}
	got := drain(sub)
	if len(got) != 3 || got[1].Payload.(map[string]any)["text"] != "dup" || got[2].Sequence != 3 {
		t.Fatalf("delivered %v, want x, dup, other", got)
	}
}

// A batch member whose client id is already in history answers its stored
// sequence; the rest of the batch commits densely in one retry.
func TestGroupCommitClientMsgAlreadyInHistory(t *testing.T) {
	chat := store.NewMemoryChat()
	ctx := tenant.NewContext(context.Background(), testTenantID)
	if err := chat.AppendRoomMessage(ctx, store.RoomMessage{RoomID: "r", Seq: 1, Kind: wire.KindChatMessage, ClientMsgID: "old", Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	g := newGate()
	j := &testJournal{fn: func([]store.RoomMessage) error { g.wait(); return nil }, inner: chat}
	h := NewHub(64, 64)
	h.SeedSeq = func(room string) (int64, error) { return chat.MaxRoomSeq(ctx, room) }
	sub := h.SubscribeLive("r")
	defer h.Unsubscribe("r", sub)

	first := publishAsync(h, chatEvent("r", "a"), chatDurable(j, "a"))
	<-g.entered
	b := publishAsync(h, chatEvent("r", "b"), chatDurable(j, "b"))
	waitQueued(t, h, "r", 1)
	old := publishAsync(h, chatEvent("r", "old"), chatDurable(j, "old"))
	waitQueued(t, h, "r", 2)
	c := publishAsync(h, chatEvent("r", "c"), chatDurable(j, "c"))
	waitQueued(t, h, "r", 3)
	close(g.release)
	if r := <-first; r.err != nil || r.ev.Sequence != 2 {
		t.Fatalf("first seq=%d err=%v, want 2", r.ev.Sequence, r.err)
	}
	rb, rold, rc := <-b, <-old, <-c
	if !errors.Is(rold.err, store.ErrDuplicateClientMsg) || rold.ev.Sequence != 1 {
		t.Fatalf("history repeat seq=%d err=%v, want the stored 1 with ErrDuplicateClientMsg", rold.ev.Sequence, rold.err)
	}
	if rb.err != nil || rb.ev.Sequence != 3 || rc.err != nil || rc.ev.Sequence != 4 {
		t.Fatalf("rest of the batch: b=%d/%v c=%d/%v, want 3 and 4 (dense)", rb.ev.Sequence, rb.err, rc.ev.Sequence, rc.err)
	}
	feed, _ := chat.RoomFeed(ctx, "r", 0, 10)
	if len(feed) != 4 {
		t.Fatalf("stored %d rows, want 4", len(feed))
	}
	got := drain(sub)
	if len(got) != 3 || got[0].Sequence != 2 || got[1].Sequence != 3 || got[2].Sequence != 4 {
		t.Fatalf("delivered %v, want seqs 2, 3, 4", got)
	}
}

// The scale-out fence: another replica wrote the room's next slots, so the
// batch hits the (room, seq) key. Every member fails (its idempotent retry
// goes to the real owner), nothing is delivered, and the counter re-seeds.
func TestGroupCommitFenceFailsEveryMember(t *testing.T) {
	chat := store.NewMemoryChat()
	ctx := tenant.NewContext(context.Background(), testTenantID)
	g := newGate()
	j := &testJournal{fn: func([]store.RoomMessage) error { g.wait(); return nil }, inner: chat}
	h := NewHub(64, 64)
	h.SeedSeq = func(room string) (int64, error) { return chat.MaxRoomSeq(ctx, room) }
	sub := h.SubscribeLive("r")
	defer h.Unsubscribe("r", sub)

	first := publishAsync(h, chatEvent("r", "a"), chatDurable(j, "a"))
	<-g.entered
	var members []<-chan result
	for i := 0; i < 3; i++ {
		members = append(members, publishAsync(h, chatEvent("r", fmt.Sprintf("m%d", i)), chatDurable(j, fmt.Sprintf("m%d", i))))
		waitQueued(t, h, "r", i+1)
	}
	// Meanwhile another replica (briefly owning the room) stores seq 3.
	if err := chat.AppendRoomMessage(ctx, store.RoomMessage{RoomID: "r", Seq: 3, Kind: wire.KindChatMessage, ClientMsgID: "elsewhere", Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	close(g.release)
	if r := <-first; r.err != nil || r.ev.Sequence != 1 {
		t.Fatalf("first seq=%d err=%v, want 1", r.ev.Sequence, r.err)
	}
	for i, ch := range members {
		if r := <-ch; !errors.Is(r.err, store.ErrDuplicateSeq) || r.ev.Sequence != 0 {
			t.Fatalf("member %d: seq=%d err=%v, want ErrDuplicateSeq and no sequence", i, r.ev.Sequence, r.err)
		}
	}
	if got := drain(sub); len(got) != 1 {
		t.Fatalf("delivered %d events, want only the first", len(got))
	}
	ev, err := h.PublishCommit(chatEvent("r", "m0"), chatDurable(j, "m0"))
	if err != nil || ev.Sequence != 4 {
		t.Fatalf("after the fence seq=%d err=%v, want 4 (re-seeded from the durable max 3)", ev.Sequence, err)
	}
}

// Transient events never wait on a commit: presence is delivered while a
// durable commit of the same room is in flight.
func TestTransientDoesNotWaitOnCommit(t *testing.T) {
	g := newGate()
	j := &testJournal{fn: func([]store.RoomMessage) error { g.wait(); return nil }}
	h := NewHub(64, 64)
	sub := h.SubscribeLive("r")
	defer h.Unsubscribe("r", sub)
	chat := publishAsync(h, chatEvent("r", "slow"), chatDurable(j, "slow"))
	<-g.entered
	done := make(chan struct{})
	go func() {
		h.Publish(wire.Event{RoomID: "r", Kind: "presence_typing"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a transient publish waited on the room's commit")
	}
	if ev := <-sub.Events(); ev.Kind != "presence_typing" || ev.Sequence != 0 {
		t.Fatalf("first delivery = %s seq=%d, want the unsequenced typing event", ev.Kind, ev.Sequence)
	}
	close(g.release)
	if r := <-chat; r.err != nil || r.ev.Sequence != 1 {
		t.Fatalf("chat seq=%d err=%v", r.ev.Sequence, r.err)
	}
}

// A revocation queues behind the room's in-flight durable events: the chat
// committed before room_deleted is delivered first and never replays into a
// later room that reuses the id.
func TestRevocationWaitsForInflightCommit(t *testing.T) {
	g := newGate()
	j := &testJournal{fn: func([]store.RoomMessage) error { g.wait(); return nil }}
	h := NewHub(64, 64)
	sub := h.SubscribeLive("r")
	defer h.Unsubscribe("r", sub)
	chat := publishAsync(h, chatEvent("r", "last words"), chatDurable(j, "last"))
	<-g.entered
	del := publishAsync(h, wire.Event{RoomID: "r", Kind: wire.EventRoomDeleted}, Durable{})
	waitQueued(t, h, "r", 1)
	if got := drain(sub); len(got) != 0 {
		t.Fatalf("delivered %v before the in-flight commit finished", got)
	}
	close(g.release)
	<-chat
	if r := <-del; r.err != nil || r.ev.Sequence != 0 {
		t.Fatalf("room_deleted seq=%d err=%v", r.ev.Sequence, r.err)
	}
	got := drain(sub)
	if len(got) != 2 || got[0].Kind != wire.KindChatMessage || got[1].Kind != wire.EventRoomDeleted {
		t.Fatalf("delivered %v, want the chat then room_deleted", got)
	}
	if late := h.Subscribe("r"); len(late.Events()) != 0 {
		t.Fatalf("a deleted room replays %d events", len(late.Events()))
	}
}

// A handoff import shares the room's queue and counter: its row commits in
// the batch with live publishes on the same dense sequence, and is never
// delivered.
func TestCommitHistorySharesTheBatch(t *testing.T) {
	g := newGate()
	j := &testJournal{fn: func([]store.RoomMessage) error { g.wait(); return nil }}
	h := NewHub(64, 64)
	sub := h.SubscribeLive("r")
	defer h.Unsubscribe("r", sub)
	first := publishAsync(h, chatEvent("r", "live"), chatDurable(j, "live"))
	<-g.entered
	imported := make(chan result, 1)
	go func() {
		ev, err := h.CommitHistory(wire.Event{RoomID: "r", Kind: wire.KindChatMessage, AtUnixMs: 7}, Durable{Journal: j, Row: store.RoomMessage{TenantID: testTenantID, Payload: []byte(`{"imported":true}`)}})
		imported <- result{ev, err}
	}()
	waitQueued(t, h, "r", 1)
	live := publishAsync(h, chatEvent("r", "live 2"), chatDurable(j, "live2"))
	waitQueued(t, h, "r", 2)
	close(g.release)
	<-first
	ri, rl := <-imported, <-live
	if ri.err != nil || ri.ev.Sequence != 2 || rl.err != nil || rl.ev.Sequence != 3 {
		t.Fatalf("import seq=%d/%v live seq=%d/%v, want 2 and 3", ri.ev.Sequence, ri.err, rl.ev.Sequence, rl.err)
	}
	if b := j.committed(); len(b) != 2 || len(b[1]) != 2 || b[1][0].CreatedAtMs != 7 {
		t.Fatalf("batches %v, want the import and the live publish in one commit", batchSizes(b))
	}
	got := drain(sub)
	if len(got) != 2 || got[0].Sequence != 1 || got[1].Sequence != 3 {
		t.Fatalf("delivered %v, want seqs 1 and 3 (the import is history only)", got)
	}
	if _, err := h.CommitHistory(wire.Event{RoomID: "r", Kind: wire.KindChatMessage}, Durable{}); err == nil {
		t.Fatal("CommitHistory without a journal accepted")
	}
}

// A queue longer than the batch bound is committed in bounded batches, in
// order.
func TestGroupCommitBatchIsBounded(t *testing.T) {
	g := newGate()
	j := &testJournal{fn: func([]store.RoomMessage) error { g.wait(); return nil }}
	h := NewHub(4, maxBatchWrites*2+8)
	sub := h.SubscribeLive("r")
	defer h.Unsubscribe("r", sub)
	first := publishAsync(h, chatEvent("r", "0"), chatDurable(j, ""))
	<-g.entered
	n := maxBatchWrites + 10
	var rest []<-chan result
	for i := 0; i < n; i++ {
		rest = append(rest, publishAsync(h, chatEvent("r", fmt.Sprint(i+1)), chatDurable(j, "")))
		waitQueued(t, h, "r", i+1)
	}
	close(g.release)
	<-first
	for i, ch := range rest {
		if r := <-ch; r.err != nil || r.ev.Sequence != int64(i+2) {
			t.Fatalf("publish %d seq=%d err=%v, want %d", i, r.ev.Sequence, r.err, i+2)
		}
	}
	sizes := batchSizes(j.committed())
	if len(sizes) != 3 || sizes[1] != maxBatchWrites || sizes[2] != 10 {
		t.Fatalf("batch sizes %v, want [1 %d 10]", sizes, maxBatchWrites)
	}
	if got := drain(sub); len(got) != n+1 {
		t.Fatalf("delivered %d, want %d", len(got), n+1)
	}
}

// Concurrent senders on one room (run with -race): every delivery arrives in
// strict sequence order with no gap, each sender's messages keep its send
// order, every caller's returned sequence is the one its event was delivered
// and stored with, and commits really batch.
func TestGroupCommitOrderingUnderConcurrentSenders(t *testing.T) {
	const senders, perSender = 16, 25
	chat := store.NewMemoryChat()
	ctx := tenant.NewContext(context.Background(), testTenantID)
	j := &testJournal{
		fn: func([]store.RoomMessage) error {
			time.Sleep(time.Duration(rand.Intn(300)) * time.Microsecond)
			return nil
		},
		inner: chat,
	}
	h := NewHub(senders*perSender, 2*senders*perSender)
	h.SeedSeq = func(room string) (int64, error) { return chat.MaxRoomSeq(ctx, room) }
	sub := h.SubscribeLive("r")
	defer h.Unsubscribe("r", sub)
	other := h.SubscribeLive("other")
	defer h.Unsubscribe("other", other)

	type sent struct {
		sender, i int
		seq       int64
	}
	results := make(chan sent, senders*perSender)
	var wg sync.WaitGroup
	for s := 0; s < senders; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			for i := 0; i < perSender; i++ {
				text := fmt.Sprintf("%d/%d", s, i)
				ev, err := h.PublishCommit(chatEvent("r", text), chatDurable(j, "c"+text))
				if err != nil {
					t.Errorf("sender %d msg %d: %v", s, i, err)
					return
				}
				results <- sent{s, i, ev.Sequence}
				if i%5 == 0 { // transient traffic and another room interleave
					h.Publish(wire.Event{RoomID: "r", Kind: "presence_typing"})
					h.Publish(chatEvent("other", text))
				}
			}
		}(s)
	}
	wg.Wait()
	close(results)

	bySeq := map[int64]string{}
	lastOf := map[int]int64{}
	for r := range results {
		text := fmt.Sprintf("%d/%d", r.sender, r.i)
		if prev, dup := bySeq[r.seq]; dup {
			t.Fatalf("seq %d returned to both %s and %s", r.seq, prev, text)
		}
		bySeq[r.seq] = text
		if r.seq <= lastOf[r.sender] {
			t.Fatalf("sender %d: msg %d got seq %d after %d (send order lost)", r.sender, r.i, r.seq, lastOf[r.sender])
		}
		lastOf[r.sender] = r.seq
	}
	var want int64 = 1
	for _, ev := range drain(sub) {
		if ev.Kind != wire.KindChatMessage {
			if ev.Sequence != 0 {
				t.Fatalf("transient %s carried seq %d", ev.Kind, ev.Sequence)
			}
			continue
		}
		if ev.Sequence != want {
			t.Fatalf("delivered seq %d, want %d (strict order, no gap)", ev.Sequence, want)
		}
		if text := ev.Payload.(map[string]any)["text"]; text != bySeq[ev.Sequence] {
			t.Fatalf("seq %d delivered %v but was returned to %s", ev.Sequence, text, bySeq[ev.Sequence])
		}
		want++
	}
	if want != senders*perSender+1 {
		t.Fatalf("delivered %d chat events, want %d", want-1, senders*perSender)
	}
	feed, err := chat.RoomFeed(ctx, "r", 0, senders*perSender+1)
	if err != nil || len(feed) != senders*perSender {
		t.Fatalf("stored %d rows (err %v), want %d", len(feed), err, senders*perSender)
	}
	for _, row := range feed {
		if "c"+bySeq[row.Seq] != row.ClientMsgID {
			t.Fatalf("stored seq %d is %s, but seq %d was returned to %s", row.Seq, row.ClientMsgID, row.Seq, bySeq[row.Seq])
		}
	}
	if commits := j.calls(); commits >= senders*perSender {
		t.Fatalf("%d commits for %d messages: concurrent publishes never batched", commits, senders*perSender)
	} else {
		t.Logf("%d messages in %d commits", senders*perSender, commits)
	}
}

// A panicking journal fails its batch instead of leaving the room without a
// leader forever.
func TestGroupCommitSurvivesJournalPanic(t *testing.T) {
	panicky := &testJournal{fn: func([]store.RoomMessage) error { panic("driver bug") }}
	h := NewHub(8, 8)
	if _, err := h.PublishCommit(chatEvent("r", "x"), chatDurable(panicky, "x")); err == nil {
		t.Fatal("a panicking commit succeeded")
	}
	ok := &testJournal{}
	ev, err := h.PublishCommit(chatEvent("r", "y"), chatDurable(ok, "y"))
	if err != nil || ev.Sequence != 1 {
		t.Fatalf("after the panic seq=%d err=%v, want 1 (the room still has a leader)", ev.Sequence, err)
	}
}

// The batch commit runs to its real outcome: its context carries no
// deadline and no cancellation (a caller's request ending, or a timeout,
// would abandon an insert the server may still commit).
func TestGroupCommitContextNeverAbandonsTheInsert(t *testing.T) {
	var deadline, cancellable bool
	j := &testJournalCtx{fn: func(ctx context.Context) {
		_, deadline = ctx.Deadline()
		cancellable = ctx.Done() != nil
	}}
	h := NewHub(8, 8)
	if _, err := h.PublishCommit(chatEvent("r", "x"), Durable{Journal: j, Row: store.RoomMessage{TenantID: testTenantID}}); err != nil {
		t.Fatal(err)
	}
	if deadline || cancellable {
		t.Fatalf("commit context: deadline=%v cancellable=%v, want neither", deadline, cancellable)
	}
}

type testJournalCtx struct{ fn func(context.Context) }

func (j *testJournalCtx) AppendRoomMessages(ctx context.Context, _ []store.RoomMessage) error {
	j.fn(ctx)
	return nil
}

// The Committed hook (the search index updater) sees exactly the rows each
// successful commit stored — live publishes and history imports, with the
// tenant and sequence the store got, minus repeats answered from history —
// and nothing of a failed commit or a store-less publish.
func TestCommittedHookSeesStoredRows(t *testing.T) {
	chat := store.NewMemoryChat()
	ctx := tenant.NewContext(context.Background(), testTenantID)
	if err := chat.AppendRoomMessage(ctx, store.RoomMessage{RoomID: "r", Seq: 1, Kind: wire.KindChatMessage, ClientMsgID: "old", Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	fail := errors.New("disk full")
	var failing bool
	var mu sync.Mutex
	j := &testJournal{inner: chat, fn: func([]store.RoomMessage) error {
		mu.Lock()
		defer mu.Unlock()
		if failing {
			return fail
		}
		return nil
	}}
	h := NewHub(64, 64)
	h.SeedSeq = func(room string) (int64, error) { return chat.MaxRoomSeq(ctx, room) }
	var seen []store.RoomMessage
	h.Committed = func(rows []store.RoomMessage) { seen = append(seen, rows...) }

	if _, err := h.PublishCommit(chatEvent("r", "a"), chatDurable(j, "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.PublishCommit(chatEvent("r", "again"), chatDurable(j, "old")); !errors.Is(err, store.ErrDuplicateClientMsg) {
		t.Fatalf("repeat err = %v", err)
	}
	if _, err := h.CommitHistory(chatEvent("r", "imported"), chatDurable(j, "")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	failing = true
	mu.Unlock()
	if _, err := h.PublishCommit(chatEvent("r", "lost"), chatDurable(j, "")); !errors.Is(err, fail) {
		t.Fatalf("failed commit err = %v", err)
	}
	h.Publish(chatEvent("r2", "no journal"))
	if len(seen) != 2 || seen[0].Seq != 2 || seen[1].Seq != 3 || seen[0].TenantID != testTenantID || seen[0].RoomID != "r" {
		t.Fatalf("hook saw %+v, want seqs 2 and 3 of room r under %s", seen, testTenantID)
	}
}
