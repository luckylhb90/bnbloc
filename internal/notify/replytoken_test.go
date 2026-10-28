package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Mentions answer to the room (human chat); agent outcomes answer to the
// agent exactly as the request addressed it; borrow requests take no reply.
func TestObserverSetsReplyAddressing(t *testing.T) {
	f := newFixture(t)
	f.install(t, "n1", fakeNotifier(t, nil).URL+"/hook", "")
	o := NewObserver(f.emitter, fakeMembership{"r1/bob": true}, 8)
	o.Observe(tctx(), wire.Event{RoomID: "r1", ChannelID: "ch1", Sequence: 9, Kind: "chat_message", Payload: map[string]any{"userId": "alice", "text": "@bob ping"}})
	o.Observe(tctx(), wire.Event{RoomID: "r1", ChannelID: "ch1", Kind: "agent_job_updated", Payload: store.AgentJob{
		JobID: "job_r", RequesterUserID: "carol", OwnerUserID: "bob", Core: "local", Provider: "codex", ChannelID: "ch1",
		MessageText: "run tests", Status: store.AgentJobFailed, FailureReason: store.FailureBorrowNotAuthorized,
	}})
	for len(o.queue) > 0 {
		o.process(context.Background(), <-o.queue)
	}
	m, ok := f.payload(t, wire.NotificationMention, "msg:r1:9", "n1")
	if !ok || m.Reply == nil || m.Reply.ChannelID != "ch1" || m.Reply.Recipient != nil {
		t.Fatalf("mention reply = %+v ok=%v", m.Reply, ok)
	}
	failed, ok := f.payload(t, wire.NotificationAgentFailed, "job:job_r", "n1")
	want := &wire.NotificationReply{ChannelID: "ch1", Recipient: &wire.NotificationReplyRecipient{OwnerID: "bob", Provider: "codex", Core: "local"}}
	if !ok || !reflect.DeepEqual(failed.Reply, want) {
		t.Fatalf("agent_failed reply = %+v ok=%v", failed.Reply, ok)
	}
	br, ok := f.payload(t, wire.NotificationBorrowRequest, "borrow:job_r", "n1")
	if !ok || br.Reply != nil {
		t.Fatalf("borrow_request reply = %+v ok=%v", br.Reply, ok)
	}
}

// The worker mints one reply token per recipient at send time: never in the
// outbox, resolvable to the member, room and addressing, expiring after
// ReplyTokenTTL; a notification without Reply carries none.
func TestWorkerMintsReplyTokensPerRecipient(t *testing.T) {
	var mu sync.Mutex
	var sent []wire.NotificationPayload
	srv := fakeNotifier(t, func(w http.ResponseWriter, r *http.Request) {
		var p wire.NotificationPayload
		_ = json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		sent = append(sent, p)
		mu.Unlock()
	})
	f := newFixture(t)
	f.install(t, "n1", srv.URL+"/hook", "")
	replies := store.NewMemoryNotificationReplies()
	w := f.worker()
	w.Replies = &ReplyTokens{Store: replies, Now: f.clock.Now}

	reply := agentReplyTo("ch1", wire.AgentJob{OwnerUserID: "carol", Provider: "codex", Core: "local"})
	_, _ = f.emitter.Emit(tctx(), Event{Type: wire.NotificationAgentFinished, RoomID: "r1", Recipients: []string{"alice", "bob"}, Actor: "carol/codex", DedupKey: "job:9", Reply: reply})
	_, _ = f.emitter.Emit(tctx(), Event{Type: wire.NotificationBorrowRequest, RoomID: "r1", Recipients: []string{"carol"}, Actor: "alice", DedupKey: "borrow:9"})
	if it, _ := f.mem.Item(tid, EventID(tid, wire.NotificationAgentFinished, "job:9"), "n1"); strings.Contains(string(it.EventJSON), replyTokenPrefix) {
		t.Fatalf("outbox row carries a reply token: %s", it.EventJSON)
	}
	if _, err := w.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("sent %d payloads, want 2", len(sent))
	}
	var fin, br wire.NotificationPayload
	for _, p := range sent {
		if p.Type == wire.NotificationAgentFinished {
			fin = p
		} else {
			br = p
		}
	}
	if br.Reply != nil || br.Recipients[0].ReplyToken != "" {
		t.Fatalf("borrow request carries a reply: %+v", br)
	}
	if !reflect.DeepEqual(fin.Reply, reply) || len(fin.Recipients) != 2 {
		t.Fatalf("agent_finished = %+v", fin)
	}
	a, b := fin.Recipients[0].ReplyToken, fin.Recipients[1].ReplyToken
	if !strings.HasPrefix(a, replyTokenPrefix) || !strings.HasPrefix(b, replyTokenPrefix) || a == b {
		t.Fatalf("reply tokens = %q %q", a, b)
	}
	row, err := w.Replies.Resolve(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if row.UserID != "bob" || row.RoomID != "r1" || row.ChannelID != "ch1" || row.TenantID != tid || row.Notifier != "n1" ||
		row.EventID != fin.ID || row.ReplyOwnerID != "carol" || row.ReplyProvider != "codex" || row.ReplyCore != "local" ||
		row.ExpiresAtMs != f.clock.Now().Add(ReplyTokenTTL).UnixMilli() || row.TokenHash == b {
		t.Fatalf("token row = %+v", row)
	}
	if _, err := w.Replies.Resolve(context.Background(), b+"x"); !errors.Is(err, ErrReplyTokenUnknown) {
		t.Fatalf("unknown token err = %v", err)
	}
	f.clock.Add(ReplyTokenTTL + time.Millisecond)
	if _, err := w.Replies.Resolve(context.Background(), a); !errors.Is(err, ErrReplyTokenExpired) {
		t.Fatalf("expired token err = %v", err)
	}
	if n, _ := w.Replies.Prune(context.Background()); n != 2 {
		t.Fatalf("pruned %d tokens, want 2", n)
	}
}

func TestReplyClientMsgIDIsStableAndScoped(t *testing.T) {
	a := ReplyClientMsgID("n1", "alice", "msg-1")
	if a != ReplyClientMsgID("n1", "alice", "msg-1") || len(a) > 128 {
		t.Fatalf("unstable or too long: %q", a)
	}
	for _, other := range []string{ReplyClientMsgID("n1", "bob", "msg-1"), ReplyClientMsgID("n2", "alice", "msg-1"), ReplyClientMsgID("n1", "alice", "msg-2")} {
		if other == a {
			t.Fatal("client message ids collide across notifier/member/key")
		}
	}
}

// A mention notifies only a reader of its channel (Observer.MayRead, which
// realtime sets from store.MayReadChannel): a member named in a members-only
// section's channel they do not belong to is not told (channels PRD C11).
func TestMentionNotifiesReadersOnly(t *testing.T) {
	f := newFixture(t)
	f.install(t, "n1", fakeNotifier(t, nil).URL+"/hook", "")
	o := NewObserver(f.emitter, fakeMembership{"r1/bob": true, "r1/carol": true}, 8)
	o.MayRead = func(_ context.Context, roomID, channelID, userID string) bool {
		return !(channelID == "ch_secret" && userID == "bob")
	}
	o.Observe(tctx(), wire.Event{RoomID: "r1", ChannelID: "ch_secret", Sequence: 9, Kind: "chat_message", Payload: map[string]any{"userId": "alice", "text": "@bob @carol look"}})
	for len(o.queue) > 0 {
		o.process(context.Background(), <-o.queue)
	}
	events := o.mentionEvents(observation{tenantID: "t", kind: "chat_message", roomID: "r1", channelID: "ch_secret", seq: 9, author: "alice", text: "@bob @carol look"})
	if len(events) != 1 || len(events[0].Recipients) != 1 || events[0].Recipients[0] != "carol" {
		t.Fatalf("mention recipients = %+v, want carol only", events)
	}
}
